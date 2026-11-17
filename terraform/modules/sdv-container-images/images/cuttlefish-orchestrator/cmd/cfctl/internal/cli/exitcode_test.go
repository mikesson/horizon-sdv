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

package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"google.golang.org/api/googleapi"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/auth"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
)

func TestExitCodeClassification(t *testing.T) {
	t.Parallel()
	wrap := func(err error) error { return fmt.Errorf("failed to get host: %w", err) }
	cases := []struct {
		name        string
		err         error
		interrupted bool
		want        int
	}{
		{"plain error", errors.New("boom"), false, ExitGeneral},
		{"auth error", wrap(&auth.Error{Message: "no service account"}), false, ExitAuth},
		{"401", wrap(&cco.Error{Status: http.StatusUnauthorized}), false, ExitAuth},
		{"403", wrap(&cco.Error{Status: http.StatusForbidden}), false, ExitAuth},
		{"IAM 403", wrap(&googleapi.Error{Code: http.StatusForbidden}), false, ExitAuth},
		{"404", wrap(&cco.Error{Status: http.StatusNotFound}), false, ExitNotFound},
		{"409", wrap(&cco.Error{Status: http.StatusConflict}), false, ExitConflict},
		{"412", wrap(&cco.Error{Status: http.StatusPreconditionFailed}), false, ExitConflict},
		{"400", wrap(&cco.Error{Status: http.StatusBadRequest}), false, ExitGeneral},
		{"429", wrap(&cco.Error{Status: http.StatusTooManyRequests}), false, ExitTransient},
		{"503", wrap(&cco.Error{Status: http.StatusServiceUnavailable}), false, ExitTransient},
		{"wait failure", wrap(&cco.WaitFailedError{State: "error", Target: "running"}), false, ExitOperationFailed},
		{"regression", wrap(&cco.WaitFailedError{State: "stopped", Target: "running", Regressed: true}), false, ExitOperationFailed},
		{"network", wrap(&url.Error{Op: "Get", URL: "https://x", Err: &net.OpError{Op: "dial", Err: errors.New("refused")}}), false, ExitTransient},
		{"timeout", wrap(context.DeadlineExceeded), false, ExitTransient},
		{"interrupted beats the cause", wrap(&cco.Error{Status: http.StatusNotFound}), true, ExitInterrupted},
		{"interrupted cancel", wrap(context.Canceled), true, ExitInterrupted},
	}
	for _, tc := range cases {
		err := WithExitCode(tc.err, tc.interrupted)
		var coder kong.ExitCoder
		if !errors.As(err, &coder) {
			t.Fatalf("%s: %T does not implement kong.ExitCoder", tc.name, err)
		}
		if got := coder.ExitCode(); got != tc.want {
			t.Errorf("%s: exit code = %d, want %d", tc.name, got, tc.want)
		}
		if err.Error() != tc.err.Error() {
			t.Errorf("%s: message changed to %q", tc.name, err)
		}
	}
	if WithExitCode(nil, true) != nil {
		t.Error("a nil error must stay nil")
	}
}

// Kong's parse errors carry their own code, which must survive.
func TestExitCodeKeepsKongParseCode(t *testing.T) {
	var c CLI
	parser, err := kong.New(&c, kong.Vars{"version": "test"}, kong.Exit(func(int) {}))
	if err != nil {
		t.Fatal(err)
	}
	_, perr := parser.Parse([]string{"no-such-command"})
	if perr == nil {
		t.Fatal("want a parse error")
	}
	var coder kong.ExitCoder
	if !errors.As(WithExitCode(perr, false), &coder) || coder.ExitCode() != 80 {
		t.Errorf("parse error lost Kong's usage code")
	}
}

func TestRootHelpDocumentsExitCodes(t *testing.T) {
	out := renderHelp(t, "--help")
	for _, want := range []string{"Exit codes:", "3 authentication", "6 operation", "7 transient", "130 interrupted"} {
		if !strings.Contains(out, want) {
			t.Errorf("root help missing %q:\n%s", want, out)
		}
	}
}
