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

package web

import (
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/operation"
)

// Problem converts an arbitrary handler error into the stored problem document
// an operation records on failure, so an async failure reads identically to the
// synchronous response the same error would have produced.
//
// It exists because MapError alone is not enough: MapError's default branch
// returns the ORIGINAL error (see its last line), and Huma, not MapError,
// renders an unclassified error as a 500 problem document. A
// caller that marshaled MapError's result directly would therefore store a raw
// Go error for exactly the class where the detail matters least and the shape
// matters most.
//
// Most verb failures already carry a status (hoclient wraps an unreachable host
// or an open breaker as 502 and an exhausted wait as 504), so the fallback below
// covers the residue: context aborts, repository errors, invalid responses.
//
// The correctness of this function depends on the fallback. huma.NewError is
// an overridable package var returning the StatusError *interface*, so
// *huma.ErrorModel is what the default implementation happens to return. The
// type system does not guarantee it, and code assuming the assertion succeeds
// would break silently if this project ever installed a custom error model.
//
// The return type is the DOMAIN struct, not Huma's model, because the orphan
// sweep must produce the same shape and it runs in internal/jobs, which may not
// reach into the transport layer.
func Problem(err error) *operation.Problem {
	if err == nil {
		return nil
	}

	mapped := MapError(err)

	var model *huma.ErrorModel
	if errors.As(mapped, &model) {
		p := &operation.Problem{
			Type:   model.Type,
			Title:  model.Title,
			Status: model.Status,
			Detail: model.Detail,
		}
		if p.Status == 0 {
			p.Status = statusOf(mapped)
		}
		if p.Title == "" {
			p.Title = http.StatusText(p.Status)
		}
		return p
	}

	status := statusOf(mapped)
	detail := mapped.Error()
	if status >= http.StatusInternalServerError {
		// An error that reached here with a 5xx was classified by nothing, so
		// its text is an internal chain. The stored document is served to
		// clients, and RespondError and the Huma error model already withhold
		// exactly this from the synchronous half. Withholding it costs no
		// diagnosis: the task worker logs the same error and records it on the
		// task row before this document is ever written.
		detail = genericServerError
	}
	return &operation.Problem{
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
	}
}

// statusOf reads an error's HTTP status, defaulting to 500, the status Huma
// would have applied to an error carrying none.
func statusOf(err error) int {
	var statusErr StatusError
	if errors.As(err, &statusErr) && statusErr.GetStatus() != 0 {
		return statusErr.GetStatus()
	}
	return http.StatusInternalServerError
}
