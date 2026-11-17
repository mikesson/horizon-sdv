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

// Package authtest provides service-account key fixtures for cfctl tests.
//
// A generated key file selects the local self-signed-JWT auth path, which
// mints tokens without any network access, so the production client stack can
// run end-to-end against an httptest backend with zero mocks.
package authtest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

// SAEmail is the service account written by WriteKeyFile.
const SAEmail = "cfctl-test@example.iam.gserviceaccount.com"

// WriteKeyFile writes a service-account key JSON file with a fresh RSA key
// and returns its path along with the key for signature verification.
func WriteKeyFile(t *testing.T) (string, *rsa.PrivateKey) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating RSA key: %v", err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshaling key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})

	saKey, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"client_email":   SAEmail,
		"private_key":    string(keyPEM),
		"private_key_id": "kid-123",
	})
	if err != nil {
		t.Fatalf("marshaling SA key: %v", err)
	}
	keyPath := filepath.Join(t.TempDir(), "key.json")
	if err := os.WriteFile(keyPath, saKey, 0o600); err != nil {
		t.Fatalf("writing key file: %v", err)
	}
	return keyPath, key
}
