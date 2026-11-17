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
	"cmp"
	"context"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
)

// A device or host argument takes an ID or a name. The server refuses a
// UUID-shaped name at create, so an argument that parses as a UUID is always
// an ID and is used without a lookup. Any other argument is a name, looked up
// with the list endpoint's exact-match filter, which never matches a deleted
// row. There is no prompt and no tie-break, so a script gets the same answer
// every time.

// nameMatch is one live resource that carries the looked-up name.
type nameMatch struct {
	id string
	// where is the device's host ID, or the host's zone.
	where     string
	status    string
	createdBy string
	createdAt time.Time
}

// nameNotFoundError reports a name that no live resource carries.
type nameNotFoundError struct {
	kind string
	name string
}

func (e *nameNotFoundError) Error() string {
	return fmt.Sprintf("no %s is named %s (list them with 'cfctl %s list')", e.kind, e.name, e.kind)
}

// ExitCode implements kong.ExitCoder.
func (e *nameNotFoundError) ExitCode() int { return ExitNotFound }

// ambiguousNameError reports a name that several live resources carry. The
// message ends with a table of the matches, so it reaches stderr with the
// error.
type ambiguousNameError struct {
	kind string
	name string
	// whereColumn heads the nameMatch.where column.
	whereColumn string
	matches     []nameMatch
}

func (e *ambiguousNameError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d %ss are named %s; name one by its ID:\n\n", len(e.matches), e.kind, e.name)
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "ID\t%s\tSTATUS\tCREATED BY\tCREATED\n", e.whereColumn)
	for _, m := range e.matches {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", m.id, cmp.Or(m.where, "-"), m.status,
			cmp.Or(m.createdBy, "-"), m.createdAt.Local().Format(time.DateTime))
	}
	// A strings.Builder never fails a write.
	_ = tw.Flush()
	return strings.TrimRight(b.String(), "\n")
}

// ExitCode implements kong.ExitCoder.
func (e *ambiguousNameError) ExitCode() int { return ExitConflict }

// isResourceID reports whether ref has the shape of a device or host ID.
func isResourceID(ref string) bool {
	return uuid.Validate(ref) == nil
}

// pickNamed returns the ID of the only match, or the error for none or
// several.
func pickNamed(e *ambiguousNameError) (string, error) {
	switch len(e.matches) {
	case 0:
		return "", &nameNotFoundError{kind: e.kind, name: e.name}
	case 1:
		return e.matches[0].id, nil
	default:
		return "", e
	}
}

// resolveDeviceArg replaces *ref, a device ID or name, with the device ID. An
// empty ref stays empty.
func resolveDeviceArg(ctx context.Context, client *cco.Client, ref *string) error {
	if *ref == "" || isResourceID(*ref) {
		return nil
	}
	devices, err := client.ListDevices(ctx, cco.DeviceListOptions{Name: *ref})
	if err != nil {
		return fmt.Errorf("looking up device %s: %w", *ref, err)
	}
	e := &ambiguousNameError{kind: kindDevice, name: *ref, whereColumn: "HOST"}
	for i := range devices {
		d := &devices[i]
		e.matches = append(e.matches, nameMatch{
			id: d.ID, where: d.HostID, status: d.Status, createdBy: d.CreatedBy, createdAt: d.CreatedAt,
		})
	}
	id, err := pickNamed(e)
	if err != nil {
		return err
	}
	*ref = id
	return nil
}

// resolveHostArg replaces *ref, a host ID or name, with the host ID. An empty
// ref stays empty.
func resolveHostArg(ctx context.Context, client *cco.Client, ref *string) error {
	if *ref == "" || isResourceID(*ref) {
		return nil
	}
	hosts, err := client.ListHosts(ctx, cco.HostListOptions{Name: *ref})
	if err != nil {
		return fmt.Errorf("looking up host %s: %w", *ref, err)
	}
	e := &ambiguousNameError{kind: kindHost, name: *ref, whereColumn: "ZONE"}
	for i := range hosts {
		h := &hosts[i]
		e.matches = append(e.matches, nameMatch{
			id: h.ID, where: h.Zone, status: h.Status, createdBy: h.CreatedBy, createdAt: h.CreatedAt,
		})
	}
	id, err := pickNamed(e)
	if err != nil {
		return err
	}
	*ref = id
	return nil
}
