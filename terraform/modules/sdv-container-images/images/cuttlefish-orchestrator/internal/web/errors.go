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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// AppError represents a structured API error response.
//
// It encapsulates the error type, user-facing message, and HTTP status code,
// ensuring consistent error formatting across all API endpoints.
type AppError struct {
	Type    string `json:"error"`
	Message string `json:"message,omitempty"`
	Code    int    `json:"-"`
}

func (e *AppError) Error() string {
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

// GetStatus returns the HTTP status code for Huma compatibility.
// This implements the huma.StatusError interface.
func (e *AppError) GetStatus() int {
	return e.Code
}

// genericServerError is the 5xx message: never an internal error's text.
const genericServerError = "An unexpected error occurred"

// RespondError writes an error response. Errors that are not already an
// AppError go through MapError, so the raw chi handlers (streaming, signaling)
// classify domain and storage sentinels exactly like the Huma operations do.
// A missing object is a 404 here too, never a masked 500.
func RespondError(w http.ResponseWriter, err error) {
	appErr, ok := errors.AsType[*AppError](err)
	if !ok {
		code, message := http.StatusInternalServerError, genericServerError
		mapped := MapError(err)
		// Stays errors.As: StatusError does not embed error, so it cannot
		// satisfy errors.AsType's constraint.
		var statusErr StatusError
		if errors.As(mapped, &statusErr) {
			code = statusErr.GetStatus()
			if code < http.StatusInternalServerError {
				// 4xx serves the MAPPED error's message: decided
				// classifications, including MapError's fixed details (the 401
				// "Authentication required"), never the raw internal chain.
				// 5xx keeps the generic message so internals never leak.
				message = mapped.Error()
			}
		}
		appErr = &AppError{Type: typeForStatus(code), Message: message, Code: code}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(appErr.Code)
	// Best effort: if encoding fails, client likely disconnected; nothing to do
	_ = json.NewEncoder(w).Encode(appErr) //nolint:errchkjson // Best effort response
}

// typeForStatus names the machine-readable error type for a mapped status,
// matching the New*Error constructors' vocabulary.
func typeForStatus(code int) string {
	switch code {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusUnprocessableEntity:
		return "unprocessable_entity"
	case http.StatusTooManyRequests:
		return "too_many_requests"
	case http.StatusServiceUnavailable:
		return "service_unavailable"
	case http.StatusInternalServerError:
		return "internal_server_error"
	default:
		return "error"
	}
}

// The constructors below take only the client-facing message. They carry no
// cause on purpose: an AppError is a decided response, and MapError renders it
// by calling Error(), so anything attached here would reach the wire. A caller
// that has a cause worth keeping logs it before constructing the response.
// That also keeps the internal chain and the client's message independent
// instead of one being derived from the other.

// NewNotFoundError creates a 404 error.
func NewNotFoundError(msg string) *AppError {
	return &AppError{
		Type:    "not_found",
		Message: msg,
		Code:    http.StatusNotFound,
	}
}

// NewBadRequestError creates a 400 error.
func NewBadRequestError(msg string) *AppError {
	return &AppError{
		Type:    "bad_request",
		Message: msg,
		Code:    http.StatusBadRequest,
	}
}

// NewUnauthorizedError creates a 401 error.
func NewUnauthorizedError(msg string) *AppError {
	return &AppError{
		Type:    "unauthorized",
		Message: msg,
		Code:    http.StatusUnauthorized,
	}
}
