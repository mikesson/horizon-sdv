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
	"net"
	"net/http"

	"github.com/alecthomas/kong"
	"google.golang.org/api/googleapi"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/auth"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
)

// Exit codes. Kong's own parse and usage errors keep its code 80. Scripts
// depend on these values, so they never change meaning.
const (
	ExitGeneral         = 1
	ExitAuth            = 3
	ExitNotFound        = 4
	ExitConflict        = 5
	ExitOperationFailed = 6
	ExitTransient       = 7
	ExitInterrupted     = 130
)

// exitCodeHelp is the exit-code table in the root help.
const exitCodeHelp = `Exit codes: 1 error, 3 authentication or permission, 4 not found,
5 conflict or failed precondition, 6 operation or --wait ended in failure,
7 transient (retry), 80 usage, 130 interrupted.`

// exitError carries the exit code Kong's FatalIfErrorf reads through
// kong.ExitCoder.
type exitError struct {
	err  error
	code int
}

func (e *exitError) Error() string { return e.err.Error() }

func (e *exitError) Unwrap() error { return e.err }

// ExitCode implements kong.ExitCoder.
func (e *exitError) ExitCode() int { return e.code }

// WithExitCode classifies a command error and attaches its exit code.
// interrupted reports that a signal canceled the command, which takes
// precedence over whatever error the cancellation produced.
func WithExitCode(err error, interrupted bool) error {
	if err == nil {
		return nil
	}
	return &exitError{err: err, code: exitCodeFor(err, interrupted)}
}

func exitCodeFor(err error, interrupted bool) int {
	if interrupted {
		return ExitInterrupted
	}
	var coder kong.ExitCoder
	if errors.As(err, &coder) {
		return coder.ExitCode()
	}
	if _, ok := errors.AsType[*auth.Error](err); ok {
		return ExitAuth
	}
	if _, ok := errors.AsType[*cco.WaitFailedError](err); ok {
		return ExitOperationFailed
	}
	if apiErr, ok := errors.AsType[*cco.Error](err); ok {
		if code, ok := exitCodeForStatus(apiErr.Status); ok {
			return code
		}
		return ExitGeneral
	}
	if gErr, ok := errors.AsType[*googleapi.Error](err); ok {
		if code, ok := exitCodeForStatus(gErr.Code); ok {
			return code
		}
		return ExitGeneral
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ExitTransient
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return ExitTransient
	}
	return ExitGeneral
}

// exitCodeForStatus maps an HTTP status to an exit code, and reports false
// for a status with no specific code.
func exitCodeForStatus(status int) (int, bool) {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ExitAuth, true
	case status == http.StatusNotFound:
		return ExitNotFound, true
	case status == http.StatusConflict || status == http.StatusPreconditionFailed:
		return ExitConflict, true
	case status == http.StatusTooManyRequests || status >= http.StatusInternalServerError:
		return ExitTransient, true
	default:
		return 0, false
	}
}
