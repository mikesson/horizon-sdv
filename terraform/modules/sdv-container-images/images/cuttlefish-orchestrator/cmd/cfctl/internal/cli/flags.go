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

// WaitFlags is the shared --wait flag for commands where waiting only blocks.
// A command where waiting also changes the output declares its own Wait.
type WaitFlags struct {
	Wait bool `help:"Block until the operation completes (combine with --timeout to bound)"`
}

// DownloadFlags is the shared flag set for artifact-producing commands. It
// downloads to a file, or leaves the artifact in the bucket with --no-download.
type DownloadFlags struct {
	Output     string `short:"o" placeholder:"FILE" help:"Download to this file (default: the artifact's base name; use --no-download to skip)"`
	NoDownload bool   `name:"no-download" help:"Store in the artifact bucket only; print the gs:// URI without downloading"`
}

// IdempotencyFlags is the shared --idempotency-key flag for every create the
// server deduplicates. A test holds this set equal to the operations that
// accept the header. The client mints a key per invocation; a stable key
// makes a rerun return the original resource. The server key space is global,
// so an ambient CFCTL_IDEMPOTENCY_KEY would make unrelated creates collide.
type IdempotencyFlags struct {
	IdempotencyKey string `name:"idempotency-key" placeholder:"KEY" help:"Makes the create retry-safe across reruns: a repeat with the same key returns the original resource instead of creating a second"`
}
