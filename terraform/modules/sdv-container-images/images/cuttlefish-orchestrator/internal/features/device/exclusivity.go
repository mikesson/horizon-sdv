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

package devicefeature

import (
	"context"
	"fmt"

	"cloud.google.com/go/spanner"

	deviceDomain "cloud-cuttlefish-orchestrator/internal/domain/device"
	hostDomain "cloud-cuttlefish-orchestrator/internal/domain/host"
	spannerplatform "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// hostOccupancy is the part of a host the exclusivity rules read.
type hostOccupancy struct {
	HostID    string
	Exclusive bool
	// Slots is current_device_count. It counts error rows, whose CVD groups
	// stay on the host as evidence, so a host holding only failed devices is
	// still occupied.
	Slots int64
	// CarriesGroup reports an undeleted multi-instance device on the host.
	// Only read for an occupied exclusive host, the only kind a
	// multi-instance device can be admitted to.
	CarriesGroup bool
}

// needsGroupRead reports whether checkHostExclusivity needs CarriesGroup for
// a create of instances onto h, so callers read the host's devices only then.
func (h hostOccupancy) needsGroupRead(instances int) bool {
	return instances == 1 && h.Exclusive && h.Slots > 0
}

// carriesGroup reports whether any undeleted device in devices runs more than
// one instance.
func carriesGroup(devices []*deviceDomain.Device) bool {
	for _, d := range devices {
		if d.Status != deviceDomain.StatusDeleted && d.DesiredInstanceCount() > 1 {
			return true
		}
	}
	return false
}

// checkHostExclusivityInTxn runs checkHostExclusivity for a create inside txn,
// reading the host's device rows in the same transaction when the verdict
// depends on them.
func checkHostExclusivityInTxn(ctx context.Context, txn *spanner.ReadWriteTransaction, h hostOccupancy, instances int) error {
	if h.needsGroupRead(instances) {
		devices, err := spannerplatform.QueryAll(
			txn.Read(ctx, "devices", spanner.Key{h.HostID}.AsPrefix(), deviceColumns()),
			"devices on host "+h.HostID, (*models.DeviceRow).ToDomain)
		if err != nil {
			return err
		}
		h.CarriesGroup = carriesGroup(devices)
	}
	return checkHostExclusivity(h, instances)
}

// checkHostExclusivity refuses a create that would share a host with a
// multi-instance device. Such a device shares the host's radio medium and
// network bridges with everything on it, so it needs an exclusive host with
// nothing else on it, and a host that carries one takes no other device. An
// exclusive host with no multi-instance device on it stays fillable by
// host_id, which is what a user-reserved host is for.
//
// Placement never offers such a host, so only a create naming its host by id
// reaches a refusal.
func checkHostExclusivity(h hostOccupancy, instances int) error {
	const instead = "Create the device with requirements and no host_id to provision an exclusive host, " +
		"or target an empty exclusive host"
	if instances > 1 {
		switch {
		case !h.Exclusive:
			return &hostDomain.InvalidStateError{Message: fmt.Sprintf(
				"host %s is shared, and a multi-instance device needs a host to itself. %s", h.HostID, instead)}
		case h.Slots > 0:
			return &hostDomain.InvalidStateError{Message: fmt.Sprintf(
				"host %s already carries a device, and a multi-instance device needs an empty host. %s",
				h.HostID, instead)}
		}
		return nil
	}
	if h.CarriesGroup {
		return &hostDomain.InvalidStateError{Message: fmt.Sprintf(
			"host %s carries a multi-instance device, which keeps the host to itself. "+
				"Target another host, or create the device with requirements and no host_id", h.HostID)}
	}
	return nil
}
