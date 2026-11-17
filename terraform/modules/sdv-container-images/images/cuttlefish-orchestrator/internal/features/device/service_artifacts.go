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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/audit"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// ArtifactUploader streams content into artifact storage. Satisfied by the
// GCS platform client, wired in main.
type ArtifactUploader interface {
	UploadObject(ctx context.Context, bucket, name string, content io.Reader, contentType string) error
}

// PolicyReader reads operator policy (system_config), here the per-artifact
// bucket overrides. The Spanner SystemConfigRepository satisfies it.
type PolicyReader interface {
	Get(ctx context.Context, key string) (*models.SystemConfig, error)
}

// artifactDeps is the optional artifact-capture dependency set: storage,
// the per-type bucket policy, and the deployment-default bucket.
type artifactDeps struct {
	store         ArtifactUploader
	policy        PolicyReader
	defaultBucket string
}

// WithArtifacts enables artifact capture (bugreports; later recordings).
// With both store and policy nil (the disabled Deps state) it is a no-op
// and Bugreport fails as unconfigured (400); a partial set is a wiring bug
// the constructor rejects.
func WithArtifacts(store ArtifactUploader, policy PolicyReader, defaultBucket string) DeviceServiceOption {
	return func(s *DeviceService) {
		if store == nil && policy == nil {
			return
		}
		if store == nil || policy == nil {
			s.wireErr = errors.Join(s.wireErr,
				errors.New("artifact capture requires both the uploader and the policy reader"))
			return
		}
		s.artifacts = &artifactDeps{store: store, policy: policy, defaultBucket: defaultBucket}
	}
}

// artifactBucket resolves the bucket for one artifact type. The system_config
// override wins, else the deployment default. With neither set it returns a
// device.ValidationError that names both settings, so the caller sees a
// configuration error instead of a 500.
func (s *DeviceService) artifactBucket(ctx context.Context, key string) (string, error) {
	cfg, err := s.artifacts.policy.Get(ctx, key)
	if err != nil && !errors.Is(err, cspanner.ErrKeyNotFound) {
		return "", fmt.Errorf("reading artifact bucket policy %q: %w", key, err)
	}
	if cfg != nil && cfg.AsString() != "" {
		return cfg.AsString(), nil
	}
	if s.artifacts.defaultBucket != "" {
		return s.artifacts.defaultBucket, nil
	}
	return "", &device.ValidationError{Message: fmt.Sprintf(
		"no artifact bucket is configured: set the %s admin setting or deploy with CCO_ARTIFACTS_BUCKET", key)}
}

// Bugreport identifiers for the audit trail: an operator-requested capture vs
// one the failed-capture detector took automatically for an error device.
const (
	bugreportTriggerOnDemand = "on_demand"
	bugreportTriggerAuto     = "auto_on_error"
)

// errArtifactMalformed reports an artifact body that is not what the host was
// asked for. It is a sentinel so the capture-on-error path can be tested for
// refusing to mark a device handled on one.
var errArtifactMalformed = errors.New("host did not serve the expected artifact")

// Leading bytes every artifact of each kind carries.
//
// The ZIP magic is only the two-byte "PK" and not the four-byte
// local-file-header PK\x03\x04. An archive with no entries is framed PK\x05\x06
// (end-of-central-directory), which is a legitimate capture and what the Host
// Orchestrator fake serves. Two bytes is the widest check admitting every valid
// archive. The recording magic is the EBML header, since screen recordings are
// Matroska/WebM, confirmed against a stored artifact rather than assumed from
// the .webm suffix.
var (
	zipMagic  = []byte("PK")
	webmMagic = []byte{0x1A, 0x45, 0xDF, 0xA3}
)

// requireFraming returns a reader over the same bytes, having first confirmed
// the stream is non-empty and carries the expected leading bytes.
//
// The transport only rejects HTTP failures, so without this ANY 200 body is
// stored under a confident content type and only discovered when someone opens
// the artifact, during a failure investigation, which is the worst possible
// moment to learn the evidence is unreadable. The host client tries fallback
// ports, so this happens in practice. A different service answering 200
// on a fallback port would otherwise be filed as a capture.
//
// The body is streamed and never buffered. An adb-inclusive bugreport runs to
// hundreds of MB, so only the prefix is held and the rest is handed straight
// to the uploader.
func requireFraming(r io.Reader, magic []byte, kind string) (io.Reader, error) {
	head := make([]byte, len(magic))
	switch _, err := io.ReadFull(r, head); {
	case errors.Is(err, io.EOF):
		return nil, fmt.Errorf("%w: the %s response body was empty", errArtifactMalformed, kind)
	case errors.Is(err, io.ErrUnexpectedEOF):
		return nil, fmt.Errorf("%w: the %s response body was under %d bytes",
			errArtifactMalformed, kind, len(magic))
	case err != nil:
		return nil, fmt.Errorf("reading the %s artifact: %w", kind, err)
	}
	if !bytes.Equal(head, magic) {
		return nil, fmt.Errorf("%w: the %s body begins %x, not %x",
			errArtifactMalformed, kind, head, magic)
	}
	return io.MultiReader(bytes.NewReader(head), r), nil
}

// Bugreport captures a bugreport for the device's CVD group on its host and
// streams it into the configured GCS bucket, returning where it landed.
//
// It is NOT gated on the device being running. An errored or stopped device
// is exactly the triage case, and the host-side capture only needs the CVD
// group to exist and the Host Orchestrator to answer. The host-side copy is
// deleted after upload (best-effort; the host reaper removes any leftover).
func (s *DeviceService) Bugreport(ctx context.Context, id string, includeADB bool) (*device.BugreportArtifact, error) {
	if s.artifacts == nil {
		return nil, &device.ValidationError{Message: "artifact capture is not enabled on this deployment"}
	}

	d, err := liveDevice(ctx, s.deviceRepo, id)
	if err != nil {
		return nil, fmt.Errorf("device not found: %w", err)
	}
	group, _ := device.ParseCVDID(d.CVDID)
	if d.CVDID == "" || group == "" {
		return nil, fmt.Errorf("device %s has no CVD to capture a bugreport from: %w", d.ID, device.ErrInvalidState)
	}

	h, err := s.hostRepo.Get(ctx, d.HostID)
	if err != nil {
		return nil, fmt.Errorf("failed to get host: %w", err)
	}
	hoURL := h.HostOrchestratorURL()
	if hoURL == "" {
		return nil, fmt.Errorf("host %s has no IP address", h.ID)
	}

	bucket, err := s.artifactBucket(ctx, models.ConfigKeyBugreportBucket)
	if err != nil {
		return nil, err
	}
	return s.storeBugreport(ctx, &bugreportTarget{device: d, group: group, hoURL: hoURL, bucket: bucket}, includeADB, bugreportTriggerOnDemand)
}

// bugreportTarget is a device's resolved CVD group, Host Orchestrator URL, and
// destination bucket, the common prerequisite of a bugreport capture.
type bugreportTarget struct {
	device *device.Device
	group  string
	hoURL  string
	bucket string
}

// storeBugreport runs the host-side capture for an already-resolved target,
// streams the ZIP into the bucket, deletes the host-side copy, and records the
// audit action tagged with the trigger. Shared by the on-demand and
// capture-on-error paths.
func (s *DeviceService) storeBugreport(ctx context.Context, t *bugreportTarget, includeADB bool, trigger string) (*device.BugreportArtifact, error) {
	client := s.hoClientFactory.ClientForHost(t.hoURL)
	bugreportID, err := client.CreateBugreport(ctx, t.group, includeADB)
	if err != nil {
		return nil, fmt.Errorf("capturing bugreport: %w", err)
	}

	rc, err := client.DownloadBugreport(ctx, bugreportID)
	if err != nil {
		return nil, fmt.Errorf("downloading bugreport: %w", err)
	}
	defer rc.Close()

	// Checked BEFORE the upload, so a bad capture fails loudly instead of
	// landing in the bucket wearing an application/zip label.
	body, err := requireFraming(rc, zipMagic, "bugreport")
	if err != nil {
		return nil, fmt.Errorf("bugreport for device %s: %w", t.device.ID, err)
	}

	object := fmt.Sprintf("artifacts/%s-%s/bugreports/%s.zip",
		t.device.Name, t.device.ID, time.Now().UTC().Format("20060102-150405"))
	if err := s.artifacts.store.UploadObject(ctx, t.bucket, object, body, "application/zip"); err != nil {
		return nil, fmt.Errorf("storing bugreport: %w", err)
	}

	// The durable copy exists; the host-side one is now garbage. This is
	// best-effort, since a leftover dies with the TTL-reaped host anyway.
	if err := client.DeleteBugreport(ctx, bugreportID); err != nil {
		s.logger.Warn("failed to delete host-side bugreport after upload",
			slog.String("device_id", t.device.ID), slog.String("bugreport_id", bugreportID), slog.Any("error", err))
	}

	audit.LogChange(ctx, s.auditLogger, audit.ActionDeviceBugreport, audit.ResourceDevice, t.device.ID, map[string]any{
		"bucket": t.bucket, "object": object, "include_adb": includeADB, "trigger": trigger,
	})
	return &device.BugreportArtifact{Bucket: t.bucket, Object: object}, nil
}

// CaptureBugreportOnError captures a bugreport for a device that reached the
// error state. The device controller's failed-capture detector files these when
// the artifacts.capture_on_error policy is on. It is idempotent and
// self-limiting. It stamps devices.bugreport_captured_at on a successful
// capture OR when the device can never be captured (recovered, no recorded CVD,
// host gone or not serving), so the detector never re-enqueues a handled row.
// Only a transient host/storage failure returns an error for the worker to
// retry.
//
// Only devices that reached a live CVD (a recorded CVDID) are capturable. A
// create that dead-lettered before boot left no group on the host to
// bugreport, and its evidence lives in the create-task logs, not a `cvd
// bugreport`.
func (s *DeviceService) CaptureBugreportOnError(ctx context.Context, id string) error {
	if s.artifacts == nil {
		return nil // capture not wired on this deployment
	}
	d, err := s.deviceRepo.Get(ctx, id)
	if err != nil {
		if errors.Is(err, device.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("loading device %s for capture: %w", id, err)
	}
	// Recovered (error -> creating/running) or already handled between the
	// detector's scan and this attempt.
	if d.Status != device.StatusError || d.BugreportCapturedAt != nil {
		return nil
	}

	group, _ := device.ParseCVDID(d.CVDID)
	if d.CVDID == "" || group == "" {
		return s.markBugreportCaptured(ctx, id) // No CVD to capture from, and there never will be.
	}
	h, err := s.hostRepo.Get(ctx, d.HostID)
	if err != nil {
		if errors.Is(err, host.ErrNotFound) {
			return s.markBugreportCaptured(ctx, id) // host gone; the CVD went with it
		}
		return fmt.Errorf("loading host %s for capture: %w", d.HostID, err)
	}
	hoURL := h.HostOrchestratorURL()
	if h.Status != host.StatusRunning || hoURL == "" {
		return s.markBugreportCaptured(ctx, id) // host not serving; CVD reclaimed with it
	}
	bucket, err := s.artifactBucket(ctx, models.ConfigKeyBugreportBucket)
	if err != nil {
		// Capture is enabled but no bucket is configured, which is an operator
		// misconfiguration. Mark handled so this does not re-enqueue every
		// cycle; the warning surfaces the gap, and fresh errors capture once a
		// bucket is set.
		s.logger.Warn("capture-on-error enabled but no artifact bucket configured; skipping",
			slog.String("device_id", id), slog.Any("error", err))
		return s.markBugreportCaptured(ctx, id)
	}

	// A failed device has no responsive Android, so only the fast host-only
	// flavor (includeADB=false) can succeed.
	if _, err := s.storeBugreport(ctx, &bugreportTarget{device: d, group: group, hoURL: hoURL, bucket: bucket}, false, bugreportTriggerAuto); err != nil {
		return fmt.Errorf("auto-capturing bugreport for device %s: %w", id, err)
	}
	return s.markBugreportCaptured(ctx, id)
}

// markBugreportCaptured stamps the capture-on-error dedup marker so the
// failed-capture detector stops revisiting the row.
func (s *DeviceService) markBugreportCaptured(ctx context.Context, id string) error {
	_, err := s.deviceRepo.Mutate(ctx, id, func(cur *device.Device) error {
		now := time.Now()
		cur.BugreportCapturedAt = &now
		return nil
	})
	if err != nil {
		return fmt.Errorf("marking bugreport captured for device %s: %w", id, err)
	}
	return nil
}

// recordingTarget is a device's resolved CVD group/name and Host Orchestrator
// URL, the common prerequisite of the recording list/fetch operations.
type recordingTarget struct {
	device *device.Device
	group  string
	name   string
	hoURL  string
}

// recordingHost resolves one instance's recording target. Like Bugreport it
// does not require the device to be running (a stopped device's recordings are
// still on the host), only that a CVD exists.
//
// Recordings are per instance on the host, because one screen belongs to one
// Android VM, so the instance is named rather than derived from the CVD ID.
func (s *DeviceService) recordingHost(ctx context.Context, id, instance string) (*recordingTarget, error) {
	d, err := liveDevice(ctx, s.deviceRepo, id)
	if err != nil {
		return nil, fmt.Errorf("device not found: %w", err)
	}
	group, _ := device.ParseCVDID(d.CVDID)
	if d.CVDID == "" || group == "" {
		return nil, fmt.Errorf("device %s has no CVD to record: %w", d.ID, device.ErrInvalidState)
	}
	inst, err := findInstance(d, instance)
	if err != nil {
		return nil, err
	}
	name := inst.Name
	h, err := s.hostRepo.Get(ctx, d.HostID)
	if err != nil {
		return nil, fmt.Errorf("failed to get host: %w", err)
	}
	hoURL := h.HostOrchestratorURL()
	if hoURL == "" {
		return nil, fmt.Errorf("host %s has no IP address", h.ID)
	}
	return &recordingTarget{device: d, group: group, name: name, hoURL: hoURL}, nil
}

// ListRecordings returns the names of screen recordings the host holds for one
// instance (host-local until fetched).
func (s *DeviceService) ListRecordings(ctx context.Context, id, instance string) ([]string, error) {
	t, err := s.recordingHost(ctx, id, instance)
	if err != nil {
		return nil, err
	}
	recordings, err := s.hoClientFactory.ClientForHost(t.hoURL).ListScreenRecordings(ctx, t.group, t.name)
	if err != nil {
		return nil, fmt.Errorf("listing screen recordings: %w", err)
	}
	return recordings, nil
}

// FetchRecording archives a host-local screen recording to the configured GCS
// bucket, returning where it landed. The host copy is left in place, because
// the Host Orchestrator has no delete endpoint for recordings, so it dies with
// the TTL-reaped host.
func (s *DeviceService) FetchRecording(ctx context.Context, id, instance, recording string) (*device.RecordingArtifact, error) {
	if s.artifacts == nil {
		return nil, &device.ValidationError{Message: "artifact capture is not enabled on this deployment"}
	}
	t, err := s.recordingHost(ctx, id, instance)
	if err != nil {
		return nil, err
	}
	bucket, err := s.artifactBucket(ctx, models.ConfigKeyRecordingBucket)
	if err != nil {
		return nil, err
	}

	rc, err := s.hoClientFactory.ClientForHost(t.hoURL).DownloadScreenRecording(ctx, t.group, t.name, recording)
	if err != nil {
		return nil, fmt.Errorf("downloading screen recording: %w", err)
	}
	defer rc.Close()

	// Same guard as a bugreport, for the same reason. Without it any 200 body is
	// archived as video/webm and the failure surfaces only when someone tries to
	// play back the evidence of a bug they can no longer reproduce.
	body, err := requireFraming(rc, webmMagic, "screen recording")
	if err != nil {
		return nil, fmt.Errorf("recording %s for device %s: %w", recording, t.device.ID, err)
	}

	object := fmt.Sprintf("artifacts/%s-%s/recordings/%s", t.device.Name, t.device.ID, recording)
	if err := s.artifacts.store.UploadObject(ctx, bucket, object, body, "video/webm"); err != nil {
		return nil, fmt.Errorf("storing screen recording: %w", err)
	}

	audit.LogChange(ctx, s.auditLogger, audit.ActionDeviceRecordFetch, audit.ResourceDevice, t.device.ID, map[string]any{
		"bucket": bucket, "object": object, "recording": recording, "instance": t.name,
	})
	return &device.RecordingArtifact{Bucket: bucket, Object: object}, nil
}
