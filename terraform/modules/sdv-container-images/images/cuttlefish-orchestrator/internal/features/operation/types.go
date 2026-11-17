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

package operationfeature

import "cloud-cuttlefish-orchestrator/internal/domain/operation"

// GetOperationInput identifies one operation.
//
// The path parameter is the bare id even though the serialized name field is the
// relative resource name ("operations/{id}"): the URL is already scoped by the
// collection, so repeating it would be redundant.
type GetOperationInput struct {
	ID string `path:"id" doc:"Operation ID"`
}

// GetOperationOutput returns one operation.
type GetOperationOutput struct {
	Body operation.Operation
}

// ListOperationsInput filters and pages the collection.
//
// limit/offset instead of a filter expression, the house convention on every
// other list here. Pagination matters here: the resource_id filter rides the
// one operations index that is not null-filtered, so it spans the full 90-day
// retention.
type ListOperationsInput struct {
	ResourceID string `query:"resource_id" doc:"Only operations acting on this resource"`
	// Done is tri-state, expressed as an enumerated STRING and not a *bool:
	// Huma panics at registration on a pointer query parameter, so a pointer here
	// would take the router down at boot rather than fail a request.
	Done   string `query:"done" enum:"true,false" doc:"Filter by completion; omit for either"`
	Limit  int    `query:"limit" minimum:"1" maximum:"100" default:"20" doc:"Maximum operations to return"`
	Offset int    `query:"offset" minimum:"0" default:"0" doc:"Number of operations to skip"`
}

// DoneFilter resolves the tri-state query parameter, returning nil for "either".
func (i *ListOperationsInput) DoneFilter() *bool {
	switch i.Done {
	case "true":
		return new(true)
	case "false":
		return new(false)
	default:
		return nil
	}
}

// ListOperationsOutput returns a page of operations, newest first.
type ListOperationsOutput struct {
	Body struct {
		Operations []operation.Operation `json:"operations"`
		Count      int                   `json:"count" doc:"Operations in this page"`
	}
}
