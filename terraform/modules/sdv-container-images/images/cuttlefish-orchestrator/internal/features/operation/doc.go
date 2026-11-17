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

// Package operationfeature serves the read side of the operation handles the
// device verbs return: GET /v2/operations/{id} and GET /v2/operations.
//
// Read-only by design. An operation is created by the verb that admits it (the
// device slice) and settled by the worker that actuates it, so there is nothing
// here to mutate. There is no cancel: the Host Orchestrator cannot stop an
// in-flight `cvd` command, so a cancel endpoint could only stop CCO waiting, it
// could not stop the device rebooting, and a verb that lies about what it does
// is worse than its absence.
//
// There is also no delete. Retention is automatic (90 days), and a client cannot
// free storage it does not own.
//
// The slice is thin, handler straight to a narrow repository port with no
// service layer, because it has no business logic. Every rule about an
// operation lives where it is admitted or actuated.
package operationfeature
