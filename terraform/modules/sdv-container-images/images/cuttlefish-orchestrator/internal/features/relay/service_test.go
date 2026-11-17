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

package relayfeature

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	devicedomain "cloud-cuttlefish-orchestrator/internal/domain/device"
	hostdomain "cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/platform/turn"
)

const (
	testSecret = "test-shared-secret"
	testRealm  = "cco.test"
	testDevice = "d1e2f3a4-0000-0000-0000-000000000001"
	testHost   = "a1b2c3d4-0000-0000-0000-000000000002"
	testEmail  = "user@example.com"
)

// stubDevices returns a fixed device, or an error.
type stubDevices struct {
	status devicedomain.Status
	err    error
}

func (s stubDevices) Get(_ context.Context, id string) (*devicedomain.Device, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &devicedomain.Device{ID: id, Status: s.status}, nil
}

// stubHosts returns a fixed host, or an error.
type stubHosts struct {
	status hostdomain.Status
	err    error
}

func (s stubHosts) Get(_ context.Context, id string) (*hostdomain.Host, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &hostdomain.Host{ID: id, Status: s.status}, nil
}

// unusedHosts fails the test if a device credential ever reaches it.
type unusedHosts struct{ t *testing.T }

func (u unusedHosts) Get(context.Context, string) (*hostdomain.Host, error) {
	u.t.Helper()
	u.t.Error("a device credential resolved against the host repository")
	return nil, errors.New("must not be called")
}

func newTestService(t *testing.T, devices DeviceLookup, now time.Time) *Service {
	t.Helper()
	return newTestServiceWith(t, devices, unusedHosts{t: t}, now)
}

func newTestServiceWith(t *testing.T, devices DeviceLookup, hosts HostLookup, now time.Time) *Service {
	t.Helper()
	creds := turn.NewCredentialService(testSecret, time.Hour)
	svc := NewService(creds, devices, hosts, testRealm, slog.New(slog.DiscardHandler))
	svc.now = func() time.Time { return now }
	return svc
}

func usernameAt(expiry time.Time) string {
	return usernameFor(expiry, turn.DeviceSubject(testDevice))
}

func usernameFor(expiry time.Time, subject turn.Subject) string {
	return fmt.Sprintf("%d:%s:%s", expiry.Unix(), testEmail, subject)
}

// wantKey recomputes the expected long-term key independently of the
// production path, so a change to either side shows up as a mismatch.
func wantKey(t *testing.T, username, realm string, algorithm turn.PasswordAlgorithm) []byte {
	t.Helper()
	mac := hmac.New(sha1.New, []byte(testSecret))
	mac.Write([]byte(username))
	password := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	material := username + ":" + realm + ":" + password
	switch algorithm {
	case turn.AlgorithmSHA256:
		sum := sha256.Sum256([]byte(material))
		return sum[:]
	default:
		sum := md5.Sum([]byte(material))
		return sum[:]
	}
}

func TestAuthorizeAcceptsLiveCredential(t *testing.T) {
	now := time.Unix(1_769_000_000, 0)
	svc := newTestService(t, stubDevices{status: devicedomain.StatusRunning}, now)
	username := usernameAt(now.Add(30 * time.Minute))

	for _, algorithm := range []turn.PasswordAlgorithm{turn.AlgorithmMD5, turn.AlgorithmSHA256} {
		t.Run(string(algorithm), func(t *testing.T) {
			got, err := svc.Authorize(t.Context(), AuthorizeRequest{
				Username: username, Realm: testRealm, Algorithm: algorithm,
			})
			if err != nil {
				t.Fatalf("Authorize() error = %v, want nil", err)
			}
			want := wantKey(t, username, testRealm, algorithm)
			if !hmac.Equal(got.Key, want) {
				t.Errorf("Authorize() key = %x, want %x", got.Key, want)
			}
			if !got.ExpiresAt.Equal(now.Add(30 * time.Minute)) {
				t.Errorf("Authorize() expiresAt = %v, want %v", got.ExpiresAt, now.Add(30*time.Minute))
			}
		})
	}
}

// The reason this slice exists: the relay does not check the expiry, so an
// expired credential must die here or it never dies at all.
func TestAuthorizeRejectsExpiredCredential(t *testing.T) {
	now := time.Unix(1_769_000_000, 0)
	svc := newTestService(t, stubDevices{status: devicedomain.StatusRunning}, now)

	for name, expiry := range map[string]time.Time{
		"long past":     now.Add(-48 * time.Hour),
		"just lapsed":   now.Add(-time.Second),
		"exactly now":   now,
		"one ago":       now.Add(-time.Nanosecond),
		"barely future": now.Add(time.Second),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Authorize(t.Context(), AuthorizeRequest{
				Username: usernameAt(expiry), Realm: testRealm, Algorithm: turn.AlgorithmMD5,
			})
			// Unix-second truncation means sub-second offsets land on `now`.
			lapsed := !expiry.Truncate(time.Second).After(now)
			switch {
			case lapsed && !errors.Is(err, ErrCredentialExpired):
				t.Errorf("Authorize() error = %v, want ErrCredentialExpired", err)
			case !lapsed && err != nil:
				t.Errorf("Authorize() error = %v, want nil", err)
			}
		})
	}
}

func TestAuthorizeRejectsRealmMismatch(t *testing.T) {
	now := time.Unix(1_769_000_000, 0)
	svc := newTestService(t, stubDevices{status: devicedomain.StatusRunning}, now)

	_, err := svc.Authorize(t.Context(), AuthorizeRequest{
		Username: usernameAt(now.Add(time.Hour)), Realm: "someone-elses-realm", Algorithm: turn.AlgorithmMD5,
	})
	if !errors.Is(err, ErrRealmMismatch) {
		t.Fatalf("Authorize() error = %v, want ErrRealmMismatch", err)
	}
}

func TestAuthorizeRejectsMalformedUsername(t *testing.T) {
	now := time.Unix(1_769_000_000, 0)
	svc := newTestService(t, stubDevices{status: devicedomain.StatusRunning}, now)

	for _, username := range []string{
		"",
		"not-a-username",
		"missing:device",
		"notanumber:user@example.com:device:dev-1",
		":user@example.com:device:dev-1",
		"1769000000::device:dev-1",
		"1769000000:user@example.com:device:",
		// The pre-subject format: a credential minted before the kind existed
		// names no repository, so it cannot be resolved.
		"1769000000:user@example.com:dev-1",
		// A kind this deployment does not mint must not fall through to one
		// that it does.
		"1769000000:user@example.com:batch:b-1",
	} {
		t.Run(username, func(t *testing.T) {
			if _, err := svc.Authorize(t.Context(), AuthorizeRequest{
				Username: username, Realm: testRealm, Algorithm: turn.AlgorithmMD5,
			}); !errors.Is(err, turn.ErrMalformedUsername) {
				t.Errorf("Authorize(%q) error = %v, want ErrMalformedUsername", username, err)
			}
		})
	}
}

func TestAuthorizeRejectsTerminalDevice(t *testing.T) {
	now := time.Unix(1_769_000_000, 0)
	svc := newTestService(t, stubDevices{status: devicedomain.StatusDeleted}, now)

	_, err := svc.Authorize(t.Context(), AuthorizeRequest{
		Username: usernameAt(now.Add(time.Hour)), Realm: testRealm, Algorithm: turn.AlgorithmMD5,
	})
	if !errors.Is(err, ErrSubjectGone) {
		t.Fatalf("Authorize() error = %v, want ErrSubjectGone", err)
	}
}

// The legacy surface mints per host, because its infra_config path names no
// device. Such a credential must resolve against hosts, not devices.
func TestAuthorizeAcceptsHostCredential(t *testing.T) {
	now := time.Unix(1_769_000_000, 0)
	svc := newTestServiceWith(t,
		stubDevices{err: errors.New("a host credential must not reach the device repository")},
		stubHosts{status: hostdomain.StatusRunning}, now)
	username := usernameFor(now.Add(30*time.Minute), turn.HostSubject(testHost))

	got, err := svc.Authorize(t.Context(), AuthorizeRequest{
		Username: username, Realm: testRealm, Algorithm: turn.AlgorithmMD5,
	})
	if err != nil {
		t.Fatalf("Authorize() error = %v, want nil", err)
	}
	// The key derivation is over the whole username, so a host credential and
	// a device credential of the same shape must not collide.
	if want := wantKey(t, username, testRealm, turn.AlgorithmMD5); !hmac.Equal(got.Key, want) {
		t.Errorf("Authorize() key = %x, want %x", got.Key, want)
	}
}

func TestAuthorizeRejectsTerminalHost(t *testing.T) {
	now := time.Unix(1_769_000_000, 0)
	svc := newTestServiceWith(t, stubDevices{}, stubHosts{status: hostdomain.StatusDeleted}, now)

	_, err := svc.Authorize(t.Context(), AuthorizeRequest{
		Username:  usernameFor(now.Add(time.Hour), turn.HostSubject(testHost)),
		Realm:     testRealm,
		Algorithm: turn.AlgorithmMD5,
	})
	if !errors.Is(err, ErrSubjectGone) {
		t.Fatalf("Authorize() error = %v, want ErrSubjectGone", err)
	}
}

func TestAuthorizePropagatesHostLookupFailure(t *testing.T) {
	now := time.Unix(1_769_000_000, 0)
	svc := newTestServiceWith(t, stubDevices{}, stubHosts{err: hostdomain.ErrNotFound}, now)

	_, err := svc.Authorize(t.Context(), AuthorizeRequest{
		Username:  usernameFor(now.Add(time.Hour), turn.HostSubject(testHost)),
		Realm:     testRealm,
		Algorithm: turn.AlgorithmMD5,
	})
	if !errors.Is(err, hostdomain.ErrNotFound) {
		t.Fatalf("Authorize() error = %v, want host.ErrNotFound", err)
	}
}

func TestAuthorizePropagatesDeviceLookupFailure(t *testing.T) {
	now := time.Unix(1_769_000_000, 0)
	svc := newTestService(t, stubDevices{err: devicedomain.ErrNotFound}, now)

	_, err := svc.Authorize(t.Context(), AuthorizeRequest{
		Username: usernameAt(now.Add(time.Hour)), Realm: testRealm, Algorithm: turn.AlgorithmMD5,
	})
	if !errors.Is(err, devicedomain.ErrNotFound) {
		t.Fatalf("Authorize() error = %v, want device.ErrNotFound", err)
	}
}

// A credential is checked before the database is touched, so a flood of stale
// credentials cannot turn into a flood of reads.
func TestAuthorizeChecksCredentialBeforeDeviceLookup(t *testing.T) {
	now := time.Unix(1_769_000_000, 0)
	svc := newTestService(t, stubDevices{err: errors.New("must not be called")}, now)

	if _, err := svc.Authorize(t.Context(), AuthorizeRequest{
		Username: usernameAt(now.Add(-time.Hour)), Realm: testRealm, Algorithm: turn.AlgorithmMD5,
	}); !errors.Is(err, ErrCredentialExpired) {
		t.Fatalf("Authorize() error = %v, want ErrCredentialExpired", err)
	}
}

func TestAuthorizeRejectsUnknownAlgorithm(t *testing.T) {
	now := time.Unix(1_769_000_000, 0)
	svc := newTestService(t, stubDevices{status: devicedomain.StatusRunning}, now)

	if _, err := svc.Authorize(t.Context(), AuthorizeRequest{
		Username: usernameAt(now.Add(time.Hour)), Realm: testRealm, Algorithm: "crc32",
	}); !errors.Is(err, turn.ErrUnknownAlgorithm) {
		t.Fatalf("Authorize() error = %v, want ErrUnknownAlgorithm", err)
	}
}
