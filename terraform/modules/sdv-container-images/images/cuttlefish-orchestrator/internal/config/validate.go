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

package config

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

// deploymentPattern is the Terraform variable's validation. The value is a GCE
// label value and the prefix of service account IDs and a bucket name, which
// is what bounds its length.
var deploymentPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,9}$`)

// Validate checks the configuration for required values
func (c *Config) Validate() error {
	// device_ingress selects the topology. It must be set explicitly (no
	// silent default) and valid.
	if !c.DeviceIngress.IsValid() {
		return fmt.Errorf("invalid device_ingress %q: must be 'public', 'private', or 'relay'", c.DeviceIngress)
	}

	// relay routes all device media through a TURN server, so a relay
	// deployment without TURN cannot stream at all.
	if c.DeviceIngress == DeviceIngressRelay {
		if len(c.TURN.Servers) == 0 {
			return errors.New("device_ingress = \"relay\" requires TURN servers to be configured")
		}
		if c.TURN.SharedSecret == "" {
			return errors.New("device_ingress = \"relay\" requires TURN shared_secret to be configured")
		}
	}

	// Auth mode gates the front door; default to iap when unset.
	if c.Auth.Mode != "" && !c.Auth.Mode.IsValid() {
		return fmt.Errorf("invalid auth mode %q: must be 'iap', 'network', or 'proxy'", c.Auth.Mode)
	}
	if c.Auth.Mode == AuthModeProxy {
		if c.Auth.DevMode {
			return errors.New(`dev_mode cannot be enabled when auth mode is "proxy"`)
		}
		if err := validateTrustedProxies(c.Auth.TrustedProxies); err != nil {
			return err
		}
		if strings.IndexFunc(c.Auth.ProxyDefaultUser, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			return errors.New("CCO_DEFAULT_USER must not contain control characters")
		}
	}

	// Spanner is the only backing store; anything else would wire a nil
	// database client and panic at runtime.
	if c.Database.Type != DatabaseTypeSpanner {
		return fmt.Errorf("unsupported database type %q: only %q is supported", c.Database.Type, DatabaseTypeSpanner)
	}
	if c.Database.Spanner.Project == "" {
		return errors.New("spanner project is required")
	}
	if c.Database.Spanner.Instance == "" {
		return errors.New("spanner instance is required")
	}
	if c.Database.Spanner.Database == "" {
		return errors.New("spanner database is required")
	}

	if c.GCP.ProjectID == "" {
		return errors.New("GCP project_id is required")
	}
	if c.Deployment == "" {
		return errors.New("deployment is required (CCO_DEPLOYMENT): the name this install labels its host VMs with")
	}
	if !deploymentPattern.MatchString(c.Deployment) {
		return fmt.Errorf("invalid deployment %q (CCO_DEPLOYMENT): must match %s", c.Deployment, deploymentPattern)
	}
	if c.GCP.HostImageProject == "" {
		return errors.New("host image project is required (CCO_HOST_IMAGE_PROJECT): the GCP project the host image families resolve in")
	}

	return nil
}

func validateTrustedProxies(raw []string) error {
	if len(raw) == 0 {
		return errors.New("auth mode \"proxy\" requires CCO_TRUSTED_PROXIES to be configured")
	}
	for _, entry := range raw {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			return errors.New("CCO_TRUSTED_PROXIES contains an empty entry")
		}
		prefix, err := parseTrustedProxyEntry(trimmed)
		if err != nil {
			return fmt.Errorf("invalid CCO_TRUSTED_PROXIES entry %q: %w", entry, err)
		}
		if prefix.Bits() == 0 {
			return fmt.Errorf("CCO_TRUSTED_PROXIES entry %q must not match all addresses (/0)", entry)
		}
		if prefix.Bits() == 1 {
			return fmt.Errorf("CCO_TRUSTED_PROXIES entry %q must not match half the address space (/1)", entry)
		}
	}
	return nil
}

func parseTrustedProxyEntry(raw string) (netip.Prefix, error) {
	if strings.Contains(raw, "/") {
		p, err := netip.ParsePrefix(raw)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("parsing CIDR prefix: %w", err)
		}
		if p.Addr().Zone() != "" {
			return netip.Prefix{}, fmt.Errorf("IPv6 zone IDs are not permitted in CIDR prefix %q", raw)
		}
		if p.Bits() <= 1 {
			return p, nil
		}
		if p.Addr().Is4In6() {
			if p.Bits() < 96 {
				return netip.Prefix{}, fmt.Errorf("IPv4-mapped IPv6 prefix %q has prefix length < 96", raw)
			}
			return netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96).Masked(), nil
		}
		return p.Masked(), nil
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("parsing IP address: %w", err)
	}
	if addr.Zone() != "" {
		return netip.Prefix{}, fmt.Errorf("IPv6 zone IDs are not permitted in address %q", raw)
	}
	unmapped := addr.Unmap()
	return netip.PrefixFrom(unmapped, unmapped.BitLen()), nil
}
