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

package adminfeature

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
)

// reclaimableBucketLimit caps each reclaimable bucket; a full bucket means
// "at least this many". The panel is a cleanup worklist and needs no complete
// count.
const reclaimableBucketLimit = 50

// expiringSoonWindow is how far ahead the expiring-hosts bucket looks.
const expiringSoonWindow = time.Hour

// provisioningWindow is the lookback for the provisioning-success lens.
const provisioningWindow = 7 * 24 * time.Hour

// DeviceLens provides the device reads behind the operator lenses; the
// device feature repository satisfies it.
type DeviceLens interface {
	List(ctx context.Context, filter device.ListFilter) ([]*device.Device, error)
	ListPinnedActive(ctx context.Context, limit int) ([]*device.Device, error)
	CountOutcomesSince(ctx context.Context, since time.Time) (total, errored int64, err error)
}

// HostLens provides the host reads behind the operator lenses; the host
// feature repository satisfies it.
type HostLens interface {
	List(ctx context.Context, filter host.ListFilter) ([]*host.Host, error)
	ListExpiringWithin(ctx context.Context, window time.Duration, limit int) ([]*host.Host, error)
	CountOutcomesSince(ctx context.Context, since time.Time) (total, errored int64, err error)
}

// ReclaimableDevice is a device row in the reclaimable-capacity panel.
type ReclaimableDevice struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	HostID       string     `json:"host_id"`
	Status       string     `json:"status"`
	PinnedBy     string     `json:"pinned_by,omitempty"`
	PinnedAt     *time.Time `json:"pinned_at,omitempty"`
	PinnedReason string     `json:"pinned_reason,omitempty"`
	PinnedUntil  *time.Time `json:"pinned_until,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// ReclaimableHost is a host row in the reclaimable-capacity panel.
type ReclaimableHost struct {
	ID                 string     `json:"id"`
	Name               string     `json:"name"`
	Zone               string     `json:"zone"`
	Status             string     `json:"status"`
	CurrentDeviceCount int64      `json:"current_device_count"`
	ExpiresAt          *time.Time `json:"expires_at,omitempty"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// ReclaimableInput is the input for the reclaimable-capacity read.
type ReclaimableInput struct{}

// ReclaimableOutput lists resources an operator can reclaim, each bucket
// capped at 50 rows (a full bucket means at least that many).
type ReclaimableOutput struct {
	Body struct {
		Total          int64               `json:"total" doc:"Rows across the four buckets; each is capped at 50, so a full bucket makes this a floor"`
		PinnedDevices  []ReclaimableDevice `json:"pinned_devices" doc:"Live pinned devices, oldest pin first"`
		StoppedDevices []ReclaimableDevice `json:"stopped_devices" doc:"Stopped-but-not-deleted devices"`
		StoppedHosts   []ReclaimableHost   `json:"stopped_hosts" doc:"Stopped-but-not-deleted hosts (still billed)"`
		ExpiringHosts  []ReclaimableHost   `json:"expiring_hosts" doc:"Hosts whose lease ends within the hour"`
	}
}

// ProvisioningStatsInput is the input for the provisioning-success read.
type ProvisioningStatsInput struct{}

// ProvisioningOutcome is one resource type's recent provisioning record.
type ProvisioningOutcome struct {
	Created int64 `json:"created" doc:"Resources created in the window"`
	Errored int64 `json:"errored" doc:"Of those, resources now in error"`
}

// ProvisioningStatsOutput reports recent provisioning outcomes; the error
// counts are current status, so failures later healed by retry don't count.
type ProvisioningStatsOutput struct {
	Body struct {
		WindowDays int                 `json:"window_days"`
		Devices    ProvisioningOutcome `json:"devices"`
		Hosts      ProvisioningOutcome `json:"hosts"`
	}
}

func toReclaimableDevice(d *device.Device) ReclaimableDevice {
	rd := ReclaimableDevice{
		ID:          d.ID,
		Name:        d.Name,
		HostID:      d.HostID,
		Status:      string(d.Status),
		PinnedAt:    d.Lifecycle.PinnedAt,
		PinnedUntil: d.Lifecycle.PinnedUntil,
		UpdatedAt:   d.UpdatedAt,
	}
	if d.Lifecycle.PinnedBy != nil {
		rd.PinnedBy = *d.Lifecycle.PinnedBy
	}
	if d.Lifecycle.PinnedReason != nil {
		rd.PinnedReason = string(*d.Lifecycle.PinnedReason)
	}
	return rd
}

func toReclaimableHost(h *host.Host) ReclaimableHost {
	return ReclaimableHost{
		ID:                 h.ID,
		Name:               h.Name,
		Zone:               h.Zone,
		Status:             string(h.Status),
		CurrentDeviceCount: h.CurrentDeviceCount,
		ExpiresAt:          h.Lifecycle.ExpiresAt,
		UpdatedAt:          h.UpdatedAt,
	}
}

// Reclaimable returns the reclaimable-capacity buckets for the Capacity page.
func (h *AdminHandler) Reclaimable(ctx context.Context, _ *ReclaimableInput) (*ReclaimableOutput, error) {
	if h.deviceLens == nil || h.hostLens == nil {
		return nil, huma.Error500InternalServerError("capacity lenses are not configured")
	}

	out := &ReclaimableOutput{}
	out.Body.PinnedDevices = []ReclaimableDevice{}
	out.Body.StoppedDevices = []ReclaimableDevice{}
	out.Body.StoppedHosts = []ReclaimableHost{}
	out.Body.ExpiringHosts = []ReclaimableHost{}

	pinned, err := h.deviceLens.ListPinnedActive(ctx, reclaimableBucketLimit)
	if err != nil {
		h.logger.Warn("failed to list pinned devices", slog.Any("error", err))
	}
	for _, d := range pinned {
		out.Body.PinnedDevices = append(out.Body.PinnedDevices, toReclaimableDevice(d))
	}

	stoppedDeviceStatus := device.StatusStopped
	stoppedDevices, err := h.deviceLens.List(ctx, device.ListFilter{Status: &stoppedDeviceStatus, Limit: reclaimableBucketLimit})
	if err != nil {
		h.logger.Warn("failed to list stopped devices", slog.Any("error", err))
	}
	for _, d := range stoppedDevices {
		out.Body.StoppedDevices = append(out.Body.StoppedDevices, toReclaimableDevice(d))
	}

	stoppedHostStatus := host.StatusStopped
	stoppedHosts, err := h.hostLens.List(ctx, host.ListFilter{Status: &stoppedHostStatus, Limit: reclaimableBucketLimit})
	if err != nil {
		h.logger.Warn("failed to list stopped hosts", slog.Any("error", err))
	}
	for _, hh := range stoppedHosts {
		out.Body.StoppedHosts = append(out.Body.StoppedHosts, toReclaimableHost(hh))
	}

	expiring, err := h.hostLens.ListExpiringWithin(ctx, expiringSoonWindow, reclaimableBucketLimit)
	if err != nil {
		h.logger.Warn("failed to list expiring hosts", slog.Any("error", err))
	}
	for _, hh := range expiring {
		out.Body.ExpiringHosts = append(out.Body.ExpiringHosts, toReclaimableHost(hh))
	}

	out.Body.Total = int64(len(out.Body.PinnedDevices) + len(out.Body.StoppedDevices) +
		len(out.Body.StoppedHosts) + len(out.Body.ExpiringHosts))
	return out, nil
}

// ProvisioningStats reports 7-day provisioning outcomes for the Operations page.
//
// Created counts come from the audit log: resource rows are hard-deleted on
// teardown (devices cascade away with their host), so counting surviving rows
// erases history on any fleet that gets torn down. Audit events persist.
// Errored still counts rows currently in error. It is a "needs attention now"
// number, not a historical failure rate.
func (h *AdminHandler) ProvisioningStats(ctx context.Context, _ *ProvisioningStatsInput) (*ProvisioningStatsOutput, error) {
	if h.deviceLens == nil || h.hostLens == nil {
		return nil, huma.Error500InternalServerError("capacity lenses are not configured")
	}

	since := time.Now().Add(-provisioningWindow)
	out := &ProvisioningStatsOutput{}
	out.Body.WindowDays = int(provisioningWindow / (24 * time.Hour))

	deviceRows, deviceErrored, err := h.deviceLens.CountOutcomesSince(ctx, since)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to count device outcomes", err)
	}
	hostRows, hostErrored, err := h.hostLens.CountOutcomesSince(ctx, since)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to count host outcomes", err)
	}

	deviceCreates, err := h.countAuditedCreates(ctx, audit.ActionDeviceCreate, since)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to count device creates", err)
	}
	hostCreates, err := h.countAuditedCreates(ctx, audit.ActionHostCreate, since)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to count host creates", err)
	}

	// Surviving rows are the floor: they cover history from before audit
	// logging as well as an audit-less deployment (nil reader counts 0).
	out.Body.Devices = ProvisioningOutcome{Created: max(deviceCreates, deviceRows), Errored: deviceErrored}
	out.Body.Hosts = ProvisioningOutcome{Created: max(hostCreates, hostRows), Errored: hostErrored}

	return out, nil
}

// countAuditedCreates counts create events of one action since the cutoff;
// without an audit reader it reports zero and the caller falls back to rows.
func (h *AdminHandler) countAuditedCreates(ctx context.Context, action audit.Action, since time.Time) (int64, error) {
	if h.auditReader == nil {
		return 0, nil
	}
	n, err := h.auditReader.Count(ctx, audit.ListFilter{Action: &action, StartTime: &since})
	if err != nil {
		return 0, fmt.Errorf("counting %s audit events: %w", action, err)
	}
	return n, nil
}
