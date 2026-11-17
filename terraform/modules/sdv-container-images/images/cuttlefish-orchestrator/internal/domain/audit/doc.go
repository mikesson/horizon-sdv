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

// Package audit provides the audit-trail domain: the action vocabulary
// (actions.go), the entry and its builder (entry.go), and the ports
// (ports.go). Sink is the fire-and-forget write port every feature consumes,
// and Repository is the batch-persistence port behind it. Audit is
// observability, so entries are droppable under pressure, and reads go
// straight to the storage repository (the admin feature's AuditReader),
// never through the sink.
package audit
