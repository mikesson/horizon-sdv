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

// Package gcs is the Google Cloud Storage adapter: bucket and object
// operations (list, stream up/download, delete, access probing) that accept
// and return storage domain types, with SDK errors classified into the
// domain sentinels so a missing object is a 404 and a denied bucket a 403,
// never a masked 500.
//
// Client is consumed whole by the storage feature's handler and, through the
// device feature's narrow ArtifactUploader port, by artifact capture; main
// builds one Client and shares it.
package gcs
