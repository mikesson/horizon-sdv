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

package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/grpc/codes"

	builddomain "cloud-cuttlefish-orchestrator/internal/domain/build"
	devicedomain "cloud-cuttlefish-orchestrator/internal/domain/device"
	hostdomain "cloud-cuttlefish-orchestrator/internal/domain/host"
	buildfeature "cloud-cuttlefish-orchestrator/internal/features/build"
	devicefeature "cloud-cuttlefish-orchestrator/internal/features/device"
	hostfeature "cloud-cuttlefish-orchestrator/internal/features/host"
)

// The affinity arm's pinned registered build. Every instance registers its
// own registry row, but all rows share this Android build id and target, so
// they share ONE fetch key, completed creates ledger it, and the placement
// checker can replay a pick against it.
const (
	harnessPinnedBuildID = "87654321"
	harnessPinnedTarget  = "aosp_cf_x86_64_only_phone-userdebug"
)

// Op kinds as written to the ledgers. The checkers key evidence and
// acked-create conservation on these, so they are named once here rather
// than matched as free strings there.
const (
	opHostCreate         = "host_create"
	opHostCreateAuto     = "host_create_auto"
	opDeviceCreate       = "device_create"
	opDeviceCreateAuto   = "device_create_auto"
	opDeviceCreateAffine = "device_create_affine"
	opDeviceCreateGroup  = "device_create_group"
	opDeviceDelete       = "device_delete"
	opHostDelete         = "host_delete"
)

// workload drives weighted random lifecycle operations through the real
// services, recording every op to an append-only ledger. Operation errors are
// data rather than failures: capacity rejections, races with the reaper, and
// not-found after a concurrent delete are all legitimate under chaos. The
// invariant checkers judge the final state and the ledger explains it.
type workload struct {
	cfg     Cfg
	devices *devicefeature.DeviceService
	hosts   *hostfeature.HostService
	builds  *buildfeature.BuildService
	rng     *rand.Rand

	pinnedOnce sync.Once
	pinnedID   string

	mu      sync.Mutex
	f       *os.File
	hostIDs []string
	devIDs  []string
	// groupDevIDs is the dedicated-host scenario's own device pool, kept apart
	// from devIDs above. Its members are handed from one group create to the
	// next and to nothing else: a pin would make their host protected and a host
	// delete would remove the row the keeper is meant to reclaim, and both are
	// legitimate outcomes that would simply make the scenario vacuous at random.
	groupDevIDs []string
	seq         int
}

func newWorkload(cfg Cfg, devices *devicefeature.DeviceService, hosts *hostfeature.HostService, builds *buildfeature.BuildService) (*workload, error) {
	path := filepath.Join(cfg.RunDir, fmt.Sprintf("ops-%d-%d.log", cfg.InstanceIndex, os.Getpid()))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening ops ledger: %w", err)
	}
	// Seeded per instance: same seed + index replays the same random stream,
	// but three workers draw from it concurrently, so which draw feeds which
	// decision is scheduling-dependent. A seed reproduces the op MIX and never
	// the op schedule.
	seed := cfg.Seed + uint64(cfg.InstanceIndex)*1_000_003 //nolint:gosec // small non-negative ordinal
	return &workload{
		cfg:     cfg,
		devices: devices,
		hosts:   hosts,
		builds:  builds,
		rng:     rand.New(rand.NewPCG(seed, seed^0xcafef00d)), //nolint:gosec // workload determinism
		f:       f,
	}, nil
}

// run drives ops until the window closes or ctx dies. It blocks.
func (w *workload) run(ctx context.Context, window time.Duration) {
	wctx, cancel := context.WithTimeout(ctx, window)
	defer cancel()
	var wg sync.WaitGroup
	for range w.cfg.WorkloadWorkers {
		wg.Go(func() {
			for wctx.Err() == nil {
				w.step(wctx)
				jitter := 0.5 + w.rngFloat()
				wait := time.Duration(float64(time.Second) / w.cfg.OpsRate * jitter)
				select {
				case <-wctx.Done():
				case <-time.After(wait):
				}
			}
		})
	}
	wg.Wait()
}

func (w *workload) rngFloat() float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.rng.Float64()
}

func (w *workload) rngN(n int) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.rng.IntN(n)
}

// workloadArm is one weighted op: upTo is the cumulative percentage bound.
type workloadArm struct {
	upTo int
	run  func(context.Context)
}

// groupArmWeight is the dedicated-host arm's share, and it is APPENDED past the
// hundred-point table rather than carved out of it: enabling the scenario adds
// ops instead of displacing them, so every other arm keeps the share it has
// always had and a baseline seed still reproduces the mix it always did.
//
// Eight because the arm needs to fire repeatedly PER INSTANCE, not merely often
// across the run. Each firing ends the group the previous one started, so an
// instance that fires once leaves a dedicated host still occupied and hands the
// keeper nothing. At three points the first run of this scenario fired three
// times across two instances, produced exactly one emptied host, and reported a
// single collapse. That was technically a pass, and one unlucky draw from
// being a vacuous one.
const groupArmWeight = 8

// arms is the weighted op table. Reads and lifecycle verbs are in the mix on
// purpose: their outcomes are data like everything else, but they walk the
// read and EXCLUSIVE-operation admission paths under the same chaos as the
// creates, and a panic there is caught as an uncommanded instance exit.
func (w *workload) arms() []workloadArm {
	arms := w.baseArms()
	if w.cfg.GroupCollapse {
		arms = append(arms, workloadArm{armSpan + groupArmWeight, w.deviceCreateGroup})
	}
	return arms
}

// armSpan is the ordinary table's width; the draw covers whatever the enabled
// arms span.
const armSpan = 100

func (w *workload) drawSpan() int {
	if w.cfg.GroupCollapse {
		return armSpan + groupArmWeight
	}
	return armSpan
}

func (w *workload) baseArms() []workloadArm {
	return []workloadArm{
		{10, w.hostCreate},
		{17, w.hostCreateAuto}, // ranked placement, failover-eligible
		{33, func(ctx context.Context) { w.deviceCreate(ctx, false) }},
		{37, w.deviceCreateAffine},                                    // declarative with a pinned build: artifact affinity
		{46, w.deviceCreateAuto},                                      // declarative: placement or auto-provision
		{52, func(ctx context.Context) { w.deviceCreate(ctx, true) }}, // pinned
		{66, w.deviceDelete},
		{73, w.hostDelete},
		{77, w.pin},
		{80, w.unpin},
		{83, w.extend},
		{87, w.deviceGet},
		{89, w.deviceList},
		{92, w.deviceStop},
		{95, w.deviceStart},
		{97, w.deviceRestart},
		{100, w.idempotencyProbe},
	}
}

// step runs one weighted random operation.
func (w *workload) step(ctx context.Context) {
	n := w.rngN(w.drawSpan())
	for _, arm := range w.arms() {
		if n < arm.upTo {
			arm.run(ctx)
			return
		}
	}
}

func (w *workload) nextSeq() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq++
	return w.seq
}

func (w *workload) log(kind, id string, err error) {
	outcome := "ok"
	if err != nil {
		outcome = "err"
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		fmt.Fprintf(w.f, "op %s %s %s %d %q\n", kind, outcome, id, time.Now().UnixMicro(), err.Error())
		return
	}
	fmt.Fprintf(w.f, "op %s %s %s %d\n", kind, outcome, id, time.Now().UnixMicro())
}

// logDeviceCreate records an acknowledged device create with the host it
// landed on. The acked-create checker needs the host: user host deletes
// remove rows physically and take their interleaved device rows with them,
// so a device row's absence is excused by a delete attempt on its host.
func (w *workload) logDeviceCreate(kind, id, hostID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fmt.Fprintf(w.f, "op %s ok %s %d host=%s\n", kind, id, time.Now().UnixMicro(), hostID)
}

// logMarker records a fact about a host that its row cannot be asked for later.
func (w *workload) logMarker(marker, hostID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fmt.Fprintf(w.f, "%s %s\n", marker, hostID)
}

// fencedEveryNth makes some of the workload's own hosts EXCLUSIVE in the
// dedicated-host scenario: a host somebody fenced for their own test, which is
// what that flag is for on the user path.
//
// It is the decoy the collapse must not take. Exclusive, empty, idle, and aged
// past the shorten floor by the nemesis, it satisfies every term of the
// collapse's predicate except its origin, and a class keyed on exclusivity
// alone would take it away between the very runs it was fenced for.
const fencedEveryNth = 4

func (w *workload) hostCreate(ctx context.Context) {
	seq := w.nextSeq()
	name := fmt.Sprintf("h-%d-%d", w.cfg.InstanceIndex, seq)
	fenced := w.cfg.GroupCollapse && seq%fencedEveryNth == 0
	h, err := w.hosts.Create(ctx, hostdomain.CreateRequest{
		Name:        name,
		Zone:        harnessZone,
		ZonePinned:  true, // the imperative path: a named zone is a pin
		MachineType: harnessMachineType,
		MaxDevices:  4,
		TTLHours:    1,
		Exclusive:   fenced,
		// Spot so the drift nemesis's "preempt" exercises the real preemption ->
		// recovery path; on a Standard host a vanished VM is (correctly) lost,
		// but the drift-outcome checker's "preempt must recover" contract then
		// false-flags it.
		ProvisioningModel: hostdomain.ProvisioningModelSpot,
	}, harnessUser, hostdomain.OriginUser)
	if err != nil {
		w.log(opHostCreate, name, err)
		return
	}
	w.mu.Lock()
	w.hostIDs = append(w.hostIDs, h.ID)
	w.mu.Unlock()
	w.log(opHostCreate, h.ID, nil)
	if fenced {
		w.logMarker(markerFencedHost, h.ID)
	}
}

// hostCreateAuto omits the zone: the service ranks one, and the host stays
// failover-eligible. Under a zone stockout the provision ladder must move it
// to the surviving zone, never strand or dead-letter it on capacity.
func (w *workload) hostCreateAuto(ctx context.Context) {
	name := fmt.Sprintf("ha-%d-%d", w.cfg.InstanceIndex, w.nextSeq())
	h, err := w.hosts.Create(ctx, hostdomain.CreateRequest{
		Name:              name,
		MachineType:       harnessMachineType,
		MaxDevices:        4,
		TTLHours:          1,
		ProvisioningModel: hostdomain.ProvisioningModelSpot,
	}, harnessUser, hostdomain.OriginUser)
	if err != nil {
		w.log(opHostCreateAuto, name, err)
		return
	}
	w.mu.Lock()
	w.hostIDs = append(w.hostIDs, h.ID)
	w.mu.Unlock()
	w.log(opHostCreateAuto, h.ID, nil)
}

// groupEveryNth makes some creates multi-instance devices. Without them the
// conservation invariant cannot fail: a fleet of single-instance devices has
// Σ desired instances identically equal to the live row count, so a checker
// still comparing against rows would agree with a correct one on every run.
// Capacity rejections from the doubled cost are expected data here, like every
// other rejection this workload provokes.
const (
	groupEveryNth = 3
	// groupInstances keeps the group small. What matters is that Σ desired
	// instances stops equalling the row count, which two already achieves,
	// while a wider group would mostly just exhaust host capacity.
	groupInstances = 2
	// instanceNameKey is the env_config key naming an instance.
	instanceNameKey = "name"
)

func (w *workload) deviceCreate(ctx context.Context, pinned bool) {
	hostID, ok := w.pick(&w.hostIDs, false)
	if !ok {
		return
	}
	seq := w.nextSeq()
	req := devicedomain.CreateRequest{
		HostID:   hostID,
		Name:     fmt.Sprintf("d-%d-%d", w.cfg.InstanceIndex, seq),
		BuildID:  "12345678",
		TTLHours: 1,
	}
	if seq%groupEveryNth == 0 {
		instances := make([]any, groupInstances)
		for i := range instances {
			instances[i] = map[string]any{instanceNameKey: fmt.Sprintf("i%d", i)}
		}
		req.Mode = "advanced"
		req.EnvConfig = map[string]any{"instances": instances}
	}
	if pinned {
		req.Pinned = true
		req.PinnedReason = devicedomain.PinReasonDemoPreparation
	}
	d, err := w.devices.Create(ctx, req, harnessUser)
	if err != nil {
		w.log(opDeviceCreate, req.Name, err)
		return
	}
	w.mu.Lock()
	w.devIDs = append(w.devIDs, d.ID)
	w.mu.Unlock()
	w.logDeviceCreate(opDeviceCreate, d.ID, d.HostID)
}

// deviceCreateAuto exercises the declarative path: the platform resolves the
// build (auto-registering the tracking default on first use, which is a
// concurrency probe), places onto existing capacity (including hosts still
// provisioning), or auto-provisions a host. Provisioned hosts join the
// workload's host pool so deletes and nemeses hit them like any other.
func (w *workload) deviceCreateAuto(ctx context.Context) {
	req := devicedomain.CreateRequest{
		Name:     fmt.Sprintf("da-%d-%d", w.cfg.InstanceIndex, w.nextSeq()),
		TTLHours: 1,
		Requirements: &devicedomain.Requirements{
			Interaction: devicedomain.InteractionHeadless,
		},
	}
	d, res, err := w.devices.CreateDeclarative(ctx, req, harnessUser)
	if err != nil {
		w.log(opDeviceCreateAuto, req.Name, err)
		return
	}
	w.mu.Lock()
	w.devIDs = append(w.devIDs, d.ID)
	if res.HostAction == devicedomain.HostActionProvision {
		w.hostIDs = append(w.hostIDs, res.HostID)
	}
	w.mu.Unlock()
	w.logDeviceCreate(opDeviceCreateAuto, d.ID, d.HostID)
}

// The dedicated-host scenario's group shape. The scenario depends on both
// numbers.
//
// FIVE instances, because that is the count no existing host can take. The
// capacity query admits a host only when max_devices − current_device_count
// covers the group, and every host this workload builds carries four, so five
// misses placement by construction and the create always reaches the
// auto-provision branch, which is the only production path that fences a host.
// Three would place whenever some host happened to be idle, and the run would
// produce dedicated hosts at random rather than on purpose.
//
// TWO gigabytes each, because the shape's memory is the SUM. At the 8 GB
// per-instance default this group asks for 44 GB with the host reserve, the
// resolver goes looking for a bigger type in the same family, the harness
// catalog holds exactly one, and the create fails before any host exists. The
// scenario would then report zero dedicated hosts and look like a keeper that
// never ran.
const (
	dedicatedGroupInstances = 5
	dedicatedGroupMemoryMB  = 2048
)

// Ledger markers for hosts whose provenance the checkers cannot recover from
// the database. Host rows hard-delete and the lease trail carries no origin, so
// which hosts were fenced BY A USER and which were provisioned FOR A GROUP is
// knowable at quiesce only from the client's own record of asking.
const (
	markerGroupHost  = "group-host"
	markerFencedHost = "fenced-host"
)

// deviceCreateGroup drives dedicated-host creation, and ends the previous group
// on its way in.
//
// The create is declarative and too large to place, which is the only way a
// dedicated host comes to exist. The instances of a group share their host's
// netsim and rootcanal medium, so the resolver fences the host it provisions for
// them, and a fenced host is one placement can never use again.
//
// Ending the previous group is the scenario's precondition rather than tidiness.
// A dedicated host is reclaimable only once its group is gone, and leaving that
// to the random delete arm empties one only sometimes, which is the difference
// between a scenario and a coincidence. The delete goes through the real
// service so the CVD teardown and the orphan census stay accurate; only the
// certainty is the harness's.
func (w *workload) deviceCreateGroup(ctx context.Context) {
	if prev, ok := w.pick(&w.groupDevIDs, true); ok {
		w.log(opDeviceDelete, prev, w.devices.Delete(ctx, prev))
	}
	instances := make([]any, dedicatedGroupInstances)
	for i := range instances {
		instances[i] = map[string]any{
			instanceNameKey: fmt.Sprintf("i%d", i),
			"vm":            map[string]any{"memory_mb": dedicatedGroupMemoryMB},
		}
	}
	req := devicedomain.CreateRequest{
		Name:      fmt.Sprintf("dg-%d-%d", w.cfg.InstanceIndex, w.nextSeq()),
		TTLHours:  1,
		Mode:      "advanced",
		EnvConfig: map[string]any{"instances": instances},
		Requirements: &devicedomain.Requirements{
			Interaction: devicedomain.InteractionHeadless,
		},
	}
	d, res, err := w.devices.CreateDeclarative(ctx, req, harnessUser)
	// Recorded even when the create failed after provisioning, because the row
	// exists either way and the keeper can reach it. A collapse of a host the
	// ledger never heard of reads as reclaiming somebody else's, so a host this
	// arm caused must be named whether or not its device survived.
	if res != nil && res.HostID != "" && res.HostAction == devicedomain.HostActionProvision {
		w.logMarker(markerGroupHost, res.HostID)
	}
	if err != nil {
		w.log(opDeviceCreateGroup, req.Name, err)
		return
	}
	w.mu.Lock()
	w.groupDevIDs = append(w.groupDevIDs, d.ID)
	w.mu.Unlock()
	w.logDeviceCreate(opDeviceCreateGroup, d.ID, d.HostID)
}

// pinnedBuild lazily registers this instance's pinned registered build. A
// registration failure is data (logged through the arm's op ledger on first
// use) and simply disables the arm for the run.
func (w *workload) pinnedBuild(ctx context.Context) (string, bool) {
	w.pinnedOnce.Do(func() {
		b, err := w.builds.Register(ctx, builddomain.CreateRequest{
			Name:      fmt.Sprintf("harness-pinned-%d", w.cfg.InstanceIndex),
			Source:    builddomain.SourceAB,
			BuildID:   harnessPinnedBuildID,
			Target:    harnessPinnedTarget,
			CreatedBy: harnessUser,
		})
		if err != nil {
			w.log("build_register", "harness-pinned", err)
			return
		}
		w.pinnedID = b.ID
	})
	return w.pinnedID, w.pinnedID != ""
}

// deviceCreateAffine drives artifact affinity with a declarative create of the
// pinned registered build. Completed creates write ledger rows through the
// production write point, and later picks order warm hosts first, under the
// same chaos as everything else.
func (w *workload) deviceCreateAffine(ctx context.Context) {
	id, ok := w.pinnedBuild(ctx)
	if !ok {
		return
	}
	req := devicedomain.CreateRequest{
		Name:     fmt.Sprintf("df-%d-%d", w.cfg.InstanceIndex, w.nextSeq()),
		TTLHours: 1,
		Requirements: &devicedomain.Requirements{
			Interaction: devicedomain.InteractionHeadless,
			Build:       devicedomain.BuildRequirement{Registered: id},
		},
	}
	d, res, err := w.devices.CreateDeclarative(ctx, req, harnessUser)
	if err != nil {
		w.log(opDeviceCreateAffine, req.Name, err)
		return
	}
	w.mu.Lock()
	w.devIDs = append(w.devIDs, d.ID)
	if res.HostAction == devicedomain.HostActionProvision {
		w.hostIDs = append(w.hostIDs, res.HostID)
	}
	w.mu.Unlock()
	w.logDeviceCreate(opDeviceCreateAffine, d.ID, d.HostID)
}

func (w *workload) deviceDelete(ctx context.Context) {
	id, ok := w.pick(&w.devIDs, true)
	if !ok {
		return
	}
	err := w.devices.Delete(ctx, id)
	// Delete is idempotent, and a real client (cfctl's keyed-POST retry)
	// retries an outcome-unknown commit; an op that gives up on the first
	// DeadlineExceeded models a client that exists nowhere.
	if err != nil && spanner.ErrCode(err) == codes.DeadlineExceeded {
		err = w.devices.Delete(ctx, id)
	}
	w.log(opDeviceDelete, id, err)
}

func (w *workload) hostDelete(ctx context.Context) {
	id, ok := w.pick(&w.hostIDs, true)
	if !ok {
		return
	}
	w.log(opHostDelete, id, w.hosts.Delete(ctx, id))
}

func (w *workload) pin(ctx context.Context) {
	id, ok := w.pick(&w.devIDs, false)
	if !ok {
		return
	}
	until := time.Now().Add(time.Duration(5+w.rngN(20)) * time.Second)
	_, err := w.devices.Pin(ctx, id, devicedomain.PinRequest{
		Reason:      devicedomain.PinReasonDemoPreparation,
		PinnedUntil: &until,
	}, harnessUser)
	w.log("pin", id, err)
}

func (w *workload) unpin(ctx context.Context) {
	id, ok := w.pick(&w.devIDs, false)
	if !ok {
		return
	}
	_, err := w.devices.Unpin(ctx, id, harnessUser)
	w.log("unpin", id, err)
}

func (w *workload) extend(ctx context.Context) {
	id, ok := w.pick(&w.devIDs, false)
	if !ok {
		return
	}
	_, err := w.devices.Extend(ctx, id, devicedomain.ExtendRequest{Seconds: 1800})
	w.log("extend", id, err)
}

// deviceGet reads one known device through the service (host enrichment
// included). NotFound after a concurrent delete is data.
func (w *workload) deviceGet(ctx context.Context) {
	id, ok := w.pick(&w.devIDs, false)
	if !ok {
		return
	}
	_, err := w.devices.Get(ctx, id)
	w.log("device_get", id, err)
}

// deviceList walks the unfiltered list path under whatever the fleet looks
// like mid-chaos.
func (w *workload) deviceList(ctx context.Context) {
	_, err := w.devices.List(ctx, devicedomain.ListFilter{})
	w.log("device_list", "-", err)
}

// deviceStop / deviceStart / deviceRestart drive the lifecycle verbs.
// Restart is an EXCLUSIVE operation: admission writes an operations row the
// open-operations checker reconciles at quiesce. Wrong-state refusals are
// data; what is under test is the admission machinery under concurrency and
// chaos.
func (w *workload) deviceStop(ctx context.Context) {
	id, ok := w.pick(&w.devIDs, false)
	if !ok {
		return
	}
	w.log("device_stop", id, w.devices.Stop(ctx, id))
}

func (w *workload) deviceStart(ctx context.Context) {
	id, ok := w.pick(&w.devIDs, false)
	if !ok {
		return
	}
	w.log("device_start", id, w.devices.Start(ctx, id))
}

// Restart addresses one instance, so the name has to come off the row: a
// harness device is single-instance, and a device with no instances is one the
// chaos already took away. That refusal is data, like every other wrong-state
// refusal here.
func (w *workload) deviceRestart(ctx context.Context) {
	id, ok := w.pick(&w.devIDs, false)
	if !ok {
		return
	}
	d, err := w.devices.Get(ctx, id)
	if err != nil {
		w.log("device_restart", id, err)
		return
	}
	instance := ""
	if len(d.Instances) > 0 {
		instance = d.Instances[0].Name
	}
	_, err = w.devices.Restart(ctx, id, instance)
	w.log("device_restart", id, err)
}

// idempotencyProbe alternates between the in-process race (two truly
// concurrent creates, one key) and the cross-instance shared-key probe.
func (w *workload) idempotencyProbe(ctx context.Context) {
	if w.rngN(2) == 0 {
		w.idempotencySharedProbe(ctx)
		return
	}
	w.idempotencyRaceProbe(ctx)
}

// idempotencySharedProbe drives the cross-process half of the idempotency
// contract: every instance derives the IDENTICAL declarative request from a
// wall-clock bucket, with no instance index anywhere, and declarative creates
// carry no host, so the fingerprints match byte-for-byte. Probes landing in
// the same bucket on different instances share the key, and the grouped
// checker asserts one device per key no matter which process committed
// first. Missed alignment costs nothing: the line still validates like any
// other idem record.
func (w *workload) idempotencySharedProbe(ctx context.Context) {
	bucket := time.Now().Unix() / 16
	key := fmt.Sprintf("idem-x-%d", bucket)
	req := devicedomain.CreateRequest{
		Name:           fmt.Sprintf("dx-%d", bucket),
		TTLHours:       1,
		IdempotencyKey: key,
		Requirements: &devicedomain.Requirements{
			Interaction: devicedomain.InteractionHeadless,
		},
	}
	d, res, err := w.devices.CreateDeclarative(ctx, req, harnessUser)
	if err != nil {
		w.log("idem_probe", key, err)
		return
	}
	w.remember(d.ID)
	w.mu.Lock()
	defer w.mu.Unlock()
	if res.HostAction == devicedomain.HostActionProvision {
		w.hostIDs = append(w.hostIDs, res.HostID)
	}
	fmt.Fprintf(w.f, "idem %s %s %s host=%s\n", key, d.ID, d.ID, d.HostID)
}

// idempotencyRaceProbe fires two concurrent creates with the same key; the
// checker asserts they resolved to one resource.
func (w *workload) idempotencyRaceProbe(ctx context.Context) {
	hostID, ok := w.pick(&w.hostIDs, false)
	if !ok {
		return
	}
	seq := w.nextSeq()
	key := fmt.Sprintf("idem-%d-%d", w.cfg.InstanceIndex, seq)
	req := devicedomain.CreateRequest{
		HostID:         hostID,
		Name:           fmt.Sprintf("di-%d-%d", w.cfg.InstanceIndex, seq),
		BuildID:        "12345678",
		TTLHours:       1,
		IdempotencyKey: key,
	}
	type res struct {
		id  string
		err error
	}
	results := make(chan res, 2)
	for range 2 {
		go func() {
			d, err := w.devices.Create(ctx, req, harnessUser)
			if err != nil {
				results <- res{err: err}
				return
			}
			results <- res{id: d.ID}
		}()
	}
	a, b := <-results, <-results
	var ids []string
	for _, r := range []res{a, b} {
		if r.err == nil {
			ids = append(ids, r.id)
			w.remember(r.id)
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	switch len(ids) {
	case 2:
		fmt.Fprintf(w.f, "idem %s %s %s host=%s\n", key, ids[0], ids[1], hostID)
	case 1:
		fmt.Fprintf(w.f, "idem %s %s %s host=%s\n", key, ids[0], ids[0], hostID)
	default:
		fmt.Fprintf(w.f, "op idem_probe err %s %d %q\n", key, time.Now().UnixMicro(), a.err.Error())
	}
}

func (w *workload) remember(deviceID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if slices.Contains(w.devIDs, deviceID) {
		return
	}
	w.devIDs = append(w.devIDs, deviceID)
}

// pick returns a random known ID, removing it when take is set.
func (w *workload) pick(ids *[]string, take bool) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(*ids)
	if n == 0 {
		return "", false
	}
	i := w.rng.IntN(n)
	id := (*ids)[i]
	if take {
		(*ids)[i] = (*ids)[n-1]
		*ids = (*ids)[:n-1]
	}
	return id, true
}

func (w *workload) close() error {
	if err := w.f.Close(); err != nil {
		return fmt.Errorf("closing ops ledger: %w", err)
	}
	return nil
}
