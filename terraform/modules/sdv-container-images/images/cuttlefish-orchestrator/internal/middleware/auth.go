// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package middleware

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"

	"google.golang.org/api/idtoken"

	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// AccessMode selects how the orchestrator's front door authenticates callers.
type AccessMode string

const (
	// AccessModeIAP authenticates via built-in Cloud Run IAP. IAP injects a
	// signed X-Goog-IAP-JWT-Assertion header that the middleware validates.
	AccessModeIAP AccessMode = "iap"

	// AccessModeProxy trusts identity headers injected by an upstream
	// authenticating reverse proxy (such as oauth2-proxy on GKE Gateway) when
	// the direct TCP peer IP (r.RemoteAddr) matches TrustedProxies.
	AccessModeProxy AccessMode = "proxy"

	// AccessModeNetwork performs no authentication: every request is
	// attributed to one fixed synthetic principal, and network reachability
	// (private ingress, VPC membership) is the entire access model. It is the
	// counterpart of upstream Cloud Orchestrator's username-only account
	// manager, for legacy clients that carry no credential (OmniLab ATS).
	//
	// No identity header or Authorization content is read, because echoing a
	// self-asserted name would put forgeable strings in the audit log while
	// driving no behavior. Tokens are not opportunistically validated either:
	// one mode, one behavior.
	AccessModeNetwork AccessMode = "network"
)

// NetworkPrincipal is the one identity every caller carries in network mode.
// The .internal TLD is reserved, so the email can never be a real account.
var NetworkPrincipal = auth.Principal{
	Email:       "anonymous@network.internal",
	DisplayName: "Network caller",
}

const (
	// iapJWTHeader carries the IAP-signed identity assertion.
	iapJWTHeader = "X-Goog-IAP-JWT-Assertion"

	// iapIssuer is the required issuer of IAP identity assertions.
	iapIssuer = "https://cloud.google.com/iap"

	// proxyEmailHeader carries the authenticated email set by an upstream reverse proxy.
	proxyEmailHeader = "X-Auth-Request-Email"

	// proxyUserHeader carries the authenticated username set by an upstream reverse proxy.
	proxyUserHeader = "X-Auth-Request-User"
)

// TokenValidator validates a JWT against an expected audience and returns
// its payload. It exists so tests can substitute the Google validator.
type TokenValidator func(ctx context.Context, token, audience string) (*idtoken.Payload, error)

// AuthConfig holds configuration for the auth middleware.
type AuthConfig struct {
	// Mode selects the authentication path. Defaults to AccessModeIAP.
	Mode AccessMode

	// IAPAudience is the expected audience for IAP JWT validation in iap
	// mode, in the direct Cloud Run form:
	// /projects/PROJECT_NUMBER/locations/REGION/services/SERVICE_NAME
	IAPAudience string

	// TrustedProxies lists peer IP prefixes permitted to supply proxy
	// identity headers when Mode is AccessModeProxy.
	TrustedProxies []netip.Prefix

	// ProxyDefaultUser is an optional fallback identity in AccessModeProxy
	// when a trusted proxy omits identity headers. Empty fails closed (401).
	ProxyDefaultUser string

	// DevMode trusts the X-Goog-Authenticated-User-Email header instead of
	// validating a signed token.
	// WARNING: Never enable in production. The header can be forged.
	DevMode bool

	// DevFallbackEmail is the identity assumed in DevMode when the request
	// carries no X-Goog-Authenticated-User-Email header (e.g., the local
	// Angular dev server, which sends no such header).
	DevFallbackEmail string

	// ValidateToken overrides the token validator. Nil means the Google
	// idtoken validator; tests substitute a fake.
	ValidateToken TokenValidator
}

// DefaultAuthConfig returns sensible defaults for auth configuration.
//
// It does not skip any paths: the router decides which routes are public and
// only wraps the rest with this middleware (see router.pathBasedAuth).
func DefaultAuthConfig() AuthConfig {
	return AuthConfig{Mode: AccessModeIAP}
}

// Auth creates middleware that extracts a verified identity and attaches a
// Principal to the request context.
//
// Authentication is the only gate. There is no role or permission model, so
// anyone who passes IAP has full access.
func Auth(cfg AuthConfig) Middleware {
	if cfg.Mode == "" {
		cfg.Mode = AccessModeIAP
	}
	if cfg.ValidateToken == nil {
		cfg.ValidateToken = idtoken.Validate
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			principal, err := extractIdentity(ctx, r, &cfg)
			if err != nil {
				slog.Warn("auth middleware: authentication failed",
					slog.String("path", r.URL.Path),
					slog.String("mode", string(cfg.Mode)),
					slog.Any("error", err),
				)
				web.RespondError(w, web.NewUnauthorizedError("Authentication required"))
				return
			}

			ctx = auth.ContextWithPrincipal(ctx, principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractIdentity resolves the caller's verified identity for the configured
// access mode. The returned Principal carries the verified email; that email
// is for audit and display only and grants no additional privileges.
func extractIdentity(ctx context.Context, r *http.Request, cfg *AuthConfig) (*auth.Principal, error) {
	if cfg.DevMode {
		return identityFromDevHeader(r, cfg)
	}

	switch cfg.Mode {
	case AccessModeIAP:
		return identityFromIAPAssertion(ctx, r, cfg)
	case AccessModeProxy:
		return identityFromProxy(r, cfg)
	case AccessModeNetwork:
		return new(NetworkPrincipal), nil
	default:
		return nil, fmt.Errorf("unsupported auth mode %q", cfg.Mode)
	}
}

// identityFromIAPAssertion validates the IAP-signed JWT assertion header.
//
// The assertion is an ES256 JWT issued by https://cloud.google.com/iap with
// the direct Cloud Run audience. IAP strips this header from external
// requests, so a valid signature proves the request passed through IAP.
func identityFromIAPAssertion(ctx context.Context, r *http.Request, cfg *AuthConfig) (*auth.Principal, error) {
	assertion := r.Header.Get(iapJWTHeader)
	if assertion == "" {
		return nil, fmt.Errorf("%w: missing %s header", auth.ErrUnauthenticated, iapJWTHeader)
	}
	if cfg.IAPAudience == "" {
		return nil, errors.New("IAP audience is not configured")
	}

	payload, err := cfg.ValidateToken(ctx, assertion, cfg.IAPAudience)
	if err != nil {
		return nil, fmt.Errorf("%w: validating IAP assertion: %w", auth.ErrInvalidToken, err)
	}
	if payload.Issuer != iapIssuer {
		return nil, fmt.Errorf("%w: unexpected issuer %q", auth.ErrInvalidToken, payload.Issuer)
	}

	email, ok := payload.Claims["email"].(string)
	if !ok || email == "" {
		return nil, fmt.Errorf("%w: IAP assertion has no email claim", auth.ErrInvalidToken)
	}

	return &auth.Principal{Email: email}, nil
}

// identityFromDevHeader trusts the plain IAP identity header for local
// development, falling back to a configured dev identity for clients that
// send no header (e.g., the local Angular dev server).
func identityFromDevHeader(r *http.Request, cfg *AuthConfig) (*auth.Principal, error) {
	email := strings.TrimPrefix(r.Header.Get("X-Goog-Authenticated-User-Email"), "accounts.google.com:")
	if email == "" {
		email = cfg.DevFallbackEmail
	}
	if email == "" {
		return nil, fmt.Errorf("%w: no dev identity header and no fallback configured", auth.ErrUnauthenticated)
	}
	return &auth.Principal{Email: email}, nil
}

// ParseTrustedProxies parses IP addresses and CIDR prefixes for proxy auth mode,
// unmapping IPv4-mapped IPv6 entries and rejecting empty lists or all-zero (/0) prefixes.
func ParseTrustedProxies(raw []string) ([]netip.Prefix, error) {
	if len(raw) == 0 {
		return nil, errors.New("trusted proxy list must not be empty")
	}
	prefixes := make([]netip.Prefix, 0, len(raw))
	for _, entry := range raw {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			return nil, errors.New("trusted proxy list contains an empty entry")
		}
		var prefix netip.Prefix
		if strings.Contains(trimmed, "/") {
			p, err := netip.ParsePrefix(trimmed)
			if err != nil {
				return nil, fmt.Errorf("invalid trusted proxy CIDR %q: %w", entry, err)
			}
			if p.Addr().Zone() != "" {
				return nil, fmt.Errorf("trusted proxy CIDR %q must not contain an IPv6 zone ID", entry)
			}
			if p.Bits() == 0 {
				return nil, fmt.Errorf("trusted proxy CIDR %q must not match all addresses (/0)", entry)
			}
			if p.Bits() == 1 {
				return nil, fmt.Errorf("trusted proxy CIDR %q must not match half the address space (/1)", entry)
			}
			if p.Addr().Is4In6() {
				if p.Bits() < 96 {
					return nil, fmt.Errorf("invalid IPv4-mapped IPv6 proxy CIDR %q: prefix length < 96", entry)
				}
				prefix = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96).Masked()
			} else {
				prefix = p.Masked()
			}
		} else {
			addr, err := netip.ParseAddr(trimmed)
			if err != nil {
				return nil, fmt.Errorf("invalid trusted proxy IP %q: %w", entry, err)
			}
			if addr.Zone() != "" {
				return nil, fmt.Errorf("trusted proxy IP %q must not contain an IPv6 zone ID", entry)
			}
			unmapped := addr.Unmap()
			prefix = netip.PrefixFrom(unmapped, unmapped.BitLen())
		}
		if prefix.Bits() == 0 {
			return nil, fmt.Errorf("trusted proxy CIDR %q must not match all addresses (/0)", entry)
		}
		if prefix.Bits() == 1 {
			return nil, fmt.Errorf("trusted proxy CIDR %q must not match half the address space (/1)", entry)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

// identityFromProxy authenticates requests forwarded by an upstream reverse proxy
// whose TCP peer IP (r.RemoteAddr) belongs to cfg.TrustedProxies.
func identityFromProxy(r *http.Request, cfg *AuthConfig) (*auth.Principal, error) {
	if len(cfg.TrustedProxies) == 0 {
		return nil, fmt.Errorf("%w: no trusted proxies configured", auth.ErrUnauthenticated)
	}
	peerIP, err := parsePeerIP(r.RemoteAddr)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", auth.ErrUnauthenticated, err)
	}
	trusted := false
	for _, p := range cfg.TrustedProxies {
		if p.Contains(peerIP) {
			trusted = true
			break
		}
	}
	if !trusted {
		return nil, fmt.Errorf("%w: peer %s is not in trusted proxies", auth.ErrUnauthenticated, peerIP)
	}

	email, err := singleCleanHeader(r.Header, proxyEmailHeader)
	if err != nil {
		return nil, err
	}
	user, err := singleCleanHeader(r.Header, proxyUserHeader)
	if err != nil {
		return nil, err
	}

	switch {
	case email != "":
		displayName := ""
		if user != "" && user != email {
			displayName = user
		}
		return &auth.Principal{Email: email, DisplayName: displayName}, nil
	case user != "":
		return &auth.Principal{Email: user}, nil
	case cfg.ProxyDefaultUser != "":
		if strings.IndexFunc(cfg.ProxyDefaultUser, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			return nil, fmt.Errorf("%w: invalid default user configuration", auth.ErrUnauthenticated)
		}
		return &auth.Principal{Email: cfg.ProxyDefaultUser}, nil
	default:
		return nil, fmt.Errorf("%w: missing %s or %s header", auth.ErrUnauthenticated, proxyEmailHeader, proxyUserHeader)
	}
}

func parsePeerIP(remoteAddr string) (netip.Addr, error) {
	if ap, err := netip.ParseAddrPort(remoteAddr); err == nil {
		return ap.Addr().Unmap(), nil
	}
	if addr, err := netip.ParseAddr(remoteAddr); err == nil {
		return addr.Unmap(), nil
	}
	return netip.Addr{}, fmt.Errorf("invalid peer address %q", remoteAddr)
}

func singleCleanHeader(h http.Header, name string) (string, error) {
	vals := h.Values(name)
	if len(vals) > 1 {
		return "", fmt.Errorf("%w: duplicate %s header", auth.ErrUnauthenticated, name)
	}
	if len(vals) == 0 {
		return "", nil
	}
	v := vals[0]
	if strings.IndexFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", fmt.Errorf("%w: invalid control character in %s header", auth.ErrUnauthenticated, name)
	}
	return strings.TrimSpace(v), nil
}

// UserFromContext retrieves the authenticated Principal from context.
func UserFromContext(ctx context.Context) (*auth.Principal, bool) {
	return auth.PrincipalFromContext(ctx)
}
