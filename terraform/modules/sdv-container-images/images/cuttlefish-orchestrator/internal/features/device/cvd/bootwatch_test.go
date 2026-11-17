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

package cvd

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/device/devicetest"
	"cloud-cuttlefish-orchestrator/internal/domain/host"
	"cloud-cuttlefish-orchestrator/internal/domain/host/hosttest"
	"cloud-cuttlefish-orchestrator/internal/platform/hoclient"
)

// loopingKernelLog crosses the panic threshold within one tail; the counts
// mirror the measured incident shape (attempts that die in the kernel).
func loopingKernelLog() string {
	var b strings.Builder
	for range device.LoopThresholdKernelPanics + 1 {
		b.WriteString("U-Boot SPL 2023.07\nU-Boot 2023.07\n")
		b.WriteString("[    0.000000] Linux version 6.6.30\n")
		b.WriteString("[    0.730000] Kernel panic - not syncing: Attempted to kill init!\n")
	}
	return b.String()
}

func healthyKernelLog() string {
	return "U-Boot SPL 2023.07\nU-Boot 2023.07\nStarting kernel ...\nU-Boot handoff\n" +
		"[    0.000000] Linux version 6.6.30\n" +
		"[   61.000000] init: VIRTUAL_DEVICE_BOOT_COMPLETED\n"
}

// dwellServed is an observation whose dwell and enrichment budget are already
// spent. That is the shape a resumed create reads from its row, and what lets
// these tests exercise enrichment without waiting out the ten-minute window.
func dwellServed() *device.BootObservation {
	since := time.Now().Add(-(bootEnrichDwell + time.Minute))
	return &device.BootObservation{TransientSince: &since, ObservedAt: since}
}

// startingGroup is the listing a mid-launch single-instance group produces.
// It is the tick's own input, since it reads the names and the states off it.
func startingGroup(names ...string) []*hoclient.CVD {
	group := make([]*hoclient.CVD, len(names))
	for i, name := range names {
		group[i] = &hoclient.CVD{Group: "cvddeadbeef", Name: name, Status: device.CVDStateStarting}
	}
	return group
}

// bootLoopHO fakes the HO for a create whose guest loops: the adopt-guard
// listing is empty once, every later listing shows the group Starting, the
// launch operation's :wait never completes (the launcher hangs mid-load),
// and the logs routes serve the given tails. A DELETE fails the test, because
// the evidence must never be torn down on the verdict.
func bootLoopHO(t *testing.T, kernelLog, launcherLog string) *httptest.Server {
	t.Helper()
	listings := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			t.Error("the boot verdict must never tear the group down; the logs are the diagnosis")
			w.Write([]byte(`{}`))
		case r.URL.Path == "/cvds" && r.Method == http.MethodGet:
			listings++
			if listings == 1 {
				w.Write([]byte(`{"cvds": []}`)) // adopt guard: no prior group
				return
			}
			w.Write([]byte(`{"cvds":[{"group":"cvddeadbeef","name":"dev-1","status":"Starting"}]}`))
		case r.URL.Path == "/cvds" && r.Method == http.MethodPost:
			w.Write([]byte(`{"name": "op-create"}`))
		case strings.HasSuffix(r.URL.Path, "/:wait"):
			<-r.Context().Done() // hung; only the row-poll ends the wait
		case strings.HasSuffix(r.URL.Path, "/logs/kernel.log"):
			w.Write([]byte(kernelLog))
		case strings.HasSuffix(r.URL.Path, "/logs/launcher.log"):
			w.Write([]byte(launcherLog))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func actuatorForHO(t *testing.T, repo *syncDeviceRepo, hoURL string) (*Actuator, *hosttest.FakeRepo) {
	t.Helper()
	u, _ := url.Parse(hoURL)
	port, _ := strconv.ParseInt(u.Port(), 10, 64)
	hostRepo := hosttest.NewFakeRepo()
	hostRepo.Hosts["h"] = &host.Host{
		ID: "h", Status: host.StatusRunning, MaxDevices: 4,
		IPAddress: "127.0.0.1", HostOrchestratorPort: port,
	}
	act := NewActuator(repo, hostRepo, nopBuilds{},
		hoclient.NewFactory(hoclient.DefaultClientConfig()), slog.Default(),
		WithCreateCancelPoll(5*time.Millisecond))
	act.loadSample = 5 * time.Millisecond
	return act, hostRepo
}

// The incident, end to end: a guest that panic-loops during create must
// settle the row to error with the evidence, stamp the group name so
// capture-on-error can archive the logs, complete the task (nil), and leave
// the group untouched.
func TestRunCreateCVD_BootLoopSettlesAndCompletes(t *testing.T) {
	t.Parallel()
	ho := bootLoopHO(t, loopingKernelLog(), "secure_env detected guest reboot, restarting.\n")

	repo := newSyncDeviceRepo()
	repo.put(&device.Device{
		ID: "deadbeef-dev", HostID: "h", Name: "dev-1",
		Status: device.StatusPending, BootObservation: dwellServed(),
	})
	act, _ := actuatorForHO(t, repo, ho.URL)

	errCh := make(chan error, 1)
	go func() { errCh <- act.RunCreateCVD(t.Context(), "deadbeef-dev", nil) }()

	select {
	case err := <-errCh:
		require.NoError(t, err, "a settled verdict completes the task; retrying re-loops the same build")
	case <-time.After(10 * time.Second):
		t.Fatal("the create wait never stood down after the verdict settle")
	}

	got, err := repo.Get(t.Context(), "deadbeef-dev")
	require.NoError(t, err)
	assert.Equal(t, device.StatusError, got.Status)
	assert.Equal(t, "cvddeadbeef/dev-1", got.CVDID,
		"the settle must stamp the deterministic group so capture-on-error has a target")
	assert.Contains(t, got.ErrorMessage, "guest boot loop",
		"the terminal message is the promoted evidence")
	assert.Contains(t, got.ErrorMessage, "Kernel panic - not syncing",
		"the panic line is what names the bug")
	require.NotNil(t, got.BootObservation)
	assert.Equal(t, device.BootVerdictLooping, got.BootObservation.Verdict)

	// Redelivery exits nil at the already-handled guard, which is the
	// crash-safety contract for a settle written before the task completed.
	require.NoError(t, act.RunCreateCVD(t.Context(), "deadbeef-dev", nil))
	assert.Equal(t, device.StatusError, repo.getStatus("deadbeef-dev"))
}

// The launch-in-flight postpone path is the other feed point: a journal-less
// redelivery that finds the group transient must run the same evaluation
// instead of postponing blind for up to launchWaitCap.
func TestRunCreateCVD_InFlightPostponeEvaluates(t *testing.T) {
	t.Parallel()
	ho := bootLoopHO(t, loopingKernelLog(), "")
	// Make the FIRST listing show the transient group too: this create never
	// reaches a wait, only the postpone ladder.
	repo := newSyncDeviceRepo()
	repo.put(&device.Device{
		ID: "deadbeef-dev", HostID: "h", Name: "dev-1",
		Status: device.StatusCreating, BootObservation: dwellServed(),
	})
	act, _ := actuatorForHO(t, repo, ho.URL)

	// Prime the listing counter past the adopt-guard empty answer.
	client := hoclient.NewFactory(hoclient.DefaultClientConfig()).ClientForHost(ho.URL)
	_, _ = client.ListCVDs(t.Context())

	err := act.RunCreateCVD(t.Context(), "deadbeef-dev", nil)
	require.NoError(t, err, "a settle on the postpone path completes the task")
	assert.Equal(t, device.StatusError, repo.getStatus("deadbeef-dev"))
}

// In the create window "booted" must mean NO ACTION: the HO operation wait is
// the authority, and a sampler-side settle-to-running would brick healthy
// creates (running → running is not a legal transition for the create's own
// commit). The tick records the verdict and does nothing else.
func TestBootWatchTick_BootedTakesNoAction(t *testing.T) {
	t.Parallel()
	ho := bootLoopHO(t, healthyKernelLog(), "")
	repo := newSyncDeviceRepo()
	d := &device.Device{
		ID: "deadbeef-dev", HostID: "h", Name: "dev-1",
		Status: device.StatusCreating, BootObservation: dwellServed(),
	}
	repo.put(d)
	act, _ := actuatorForHO(t, repo, ho.URL)
	client := hoclient.NewFactory(hoclient.DefaultClientConfig()).ClientForHost(ho.URL)

	settled := act.bootWatchTick(t.Context(), client, d, "cvddeadbeef", startingGroup("dev-1"), nil)
	assert.False(t, settled, "booted is not a settle")
	assert.Equal(t, device.StatusCreating, repo.getStatus("deadbeef-dev"),
		"the row must stay with its owner, the create task")
	require.NotNil(t, d.BootObservation)
	assert.Equal(t, device.BootVerdictBooted, d.BootObservation.Verdict)
}

// A fetch failure changes nothing: with the log endpoint failing, the
// observation stays un-enriched and the wait continues exactly as today. The
// safety is the shape (no enrichment → the rule cannot match), not error
// handling remembered at every call site.
func TestBootWatchTick_FetchFailureDegradesToToday(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	repo := newSyncDeviceRepo()
	prev := dwellServed()
	d := &device.Device{
		ID: "deadbeef-dev", HostID: "h", Name: "dev-1",
		Status: device.StatusCreating, BootObservation: prev,
	}
	repo.put(d)
	act, _ := actuatorForHO(t, repo, srv.URL)
	client := hoclient.NewFactory(hoclient.DefaultClientConfig()).ClientForHost(srv.URL)

	settled := act.bootWatchTick(t.Context(), client, d, "cvddeadbeef", startingGroup("dev-1"), nil)
	assert.False(t, settled)
	assert.Same(t, prev, d.BootObservation, "a failed fetch must not fabricate a sample")
	assert.Equal(t, device.StatusCreating, repo.getStatus("deadbeef-dev"))
}

// namedInstanceHO fakes the HO's logs routes for a group whose instances carry
// names of the caller's choosing: kernelLogs is keyed by instance name, an
// instance it does not name 404s BOTH its files, and every requested path is
// recorded. The 404 matters here, because it is what the real host answers
// for a logs directory that does not exist, so a fetch at the wrong instance
// reads here exactly as blind as it is in production.
func namedInstanceHO(t *testing.T, kernelLogs map[string]string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /cvds/{group}/{name}/logs/{file}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 5 || parts[3] != "logs" {
			w.Write([]byte(`{}`))
			return
		}
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		kernel, known := kernelLogs[parts[2]]
		switch {
		case !known:
			http.NotFound(w, r)
		case parts[4] == device.KernelLogFile:
			w.Write([]byte(kernel))
		default:
			w.Write(nil) // launcher.log: present and quiet
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(paths)
	}
}

// The watch reads the logs of the instances the HOST named, never the device
// row's own name. An advanced create names its instances, and the row's name
// is then a logs directory that does not exist. Both halves are asserted:
// which paths were fetched, and that a loop in the SECOND instance still
// settles the device, because cvd resets the whole group when one fails.
func TestBootWatchTick_ReadsTheInstancesTheHostNamed(t *testing.T) {
	t.Parallel()
	ho, requested := namedInstanceHO(t, map[string]string{
		"phone": healthyKernelLog(),
		"watch": loopingKernelLog(),
	})

	repo := newSyncDeviceRepo()
	d := &device.Device{
		ID: "deadbeef-dev", HostID: "h", Name: "dev-a",
		Status: device.StatusCreating, BootObservation: dwellServed(),
	}
	repo.put(d)
	act, _ := actuatorForHO(t, repo, ho.URL)
	client := hoclient.NewFactory(hoclient.DefaultClientConfig()).ClientForHost(ho.URL)

	members := []*hoclient.CVD{
		{Group: "cvddeadbeef", Name: "phone", Status: device.CVDStateRunning},
		{Group: "cvddeadbeef", Name: "watch", Status: device.CVDStateStarting},
	}
	settled := act.bootWatchTick(t.Context(), client, d, "cvddeadbeef", members, nil)
	assert.True(t, settled, "a loop in any instance is the group's loop; cvd resets all of them")

	// The requested paths are the assertion, not the absence of an error: a
	// watch addressed at "dev-a" 404s in silence, which is exactly how this
	// went unnoticed.
	paths := requested()
	assert.Contains(t, paths, "/cvds/cvddeadbeef/phone/logs/kernel.log")
	assert.Contains(t, paths, "/cvds/cvddeadbeef/watch/logs/kernel.log")
	for _, p := range paths {
		assert.NotContains(t, p, "/dev-a/", "the device row's name is not an instance")
	}

	got, err := repo.Get(t.Context(), "deadbeef-dev")
	require.NoError(t, err)
	assert.Equal(t, device.StatusError, got.Status)
	assert.Equal(t, "cvddeadbeef/phone", got.CVDID,
		"the stamped id names the instance the host listed first, never the device")
	assert.Contains(t, got.ErrorMessage, "Kernel panic - not syncing",
		"the evidence is the looping instance's own panic line")
}

// The enum recorded beside the observation is the fold over the whole group.
// Read off the first member, a phone that came up early reports the device
// Running while its watch is still booting.
func TestBootWatchTick_RecordsTheFoldedGroupState(t *testing.T) {
	t.Parallel()
	ho, _ := namedInstanceHO(t, map[string]string{
		"phone": healthyKernelLog(),
		"watch": healthyKernelLog(),
	})

	repo := newSyncDeviceRepo()
	d := &device.Device{
		ID: "deadbeef-dev", HostID: "h", Name: "dev-a",
		Status: device.StatusCreating, BootObservation: dwellServed(),
	}
	repo.put(d)
	act, _ := actuatorForHO(t, repo, ho.URL)
	client := hoclient.NewFactory(hoclient.DefaultClientConfig()).ClientForHost(ho.URL)

	settled := act.bootWatchTick(t.Context(), client, d, "cvddeadbeef", []*hoclient.CVD{
		{Group: "cvddeadbeef", Name: "phone", Status: device.CVDStateRunning},
		{Group: "cvddeadbeef", Name: "watch", Status: device.CVDStateStarting},
	}, nil)
	assert.False(t, settled)

	got, err := repo.Get(t.Context(), "deadbeef-dev")
	require.NoError(t, err)
	assert.Equal(t, device.CVDStateStarting, got.CVDStatus,
		"a group is only as far along as its least-far instance")
}

// transitionHO fakes the HO for a load whose listing walks the given status
// sequence, one step per probe, sticking on the last. "absent" serves an
// empty listing, meaning the group is not yet registered.
func transitionHO(t *testing.T, sequence []string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	probes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cvds" || r.Method != http.MethodGet {
			w.Write([]byte(`{}`))
			return
		}
		mu.Lock()
		status := sequence[min(probes, len(sequence)-1)]
		probes++
		mu.Unlock()
		if status == "absent" {
			w.Write([]byte(`{"cvds": []}`))
			return
		}
		fmt.Fprintf(w, `{"cvds":[{"group":"cvddeadbeef","name":"dev-1","status":%q}]}`, status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The trail's status stands for the whole group, not its first member: the
// trail exists to place the fetch→boot boundary, and a phone that came up
// while its watch was still booting would stamp that leg as over.
func TestGroupLoadStatus(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "absent", groupLoadStatus(nil),
		"a group the launcher has not registered is not in any CVD state")
	assert.Equal(t, device.CVDStateStarting, groupLoadStatus([]*hoclient.CVD{
		{Name: "phone", Status: device.CVDStateRunning},
		{Name: "watch", Status: device.CVDStateStarting},
	}))
	assert.Equal(t, device.CVDStateRunning, groupLoadStatus([]*hoclient.CVD{
		{Name: "phone", Status: device.CVDStateRunning},
		{Name: "watch", Status: device.CVDStateRunning},
	}), "Running is reached only unanimously")
}

// trailStatuses projects the persisted trail for order assertions.
func trailStatuses(trail []device.BootTransition) []string {
	statuses := make([]string, len(trail))
	for i, tr := range trail {
		statuses[i] = tr.Status
	}
	return statuses
}

// The load-status trail must reach the ROW for a healthy create: the whole
// point is measuring the fetch/boot split of waits that finish inside the
// enrichment dwell, where the transition writes are the only persistence.
// Every recorded leg is asserted, so dropping a stamp from the persisted set,
// or the mid-dwell write itself, goes red here.
func TestSampleCVDLoad_PersistsTransitionTrail(t *testing.T) {
	t.Parallel()
	ho := transitionHO(t, []string{"absent", "Starting", "Running"})
	repo := newSyncDeviceRepo()
	repo.put(&device.Device{
		ID: "deadbeef-dev", HostID: "h", Name: "dev-1", Status: device.StatusCreating,
	})
	act, _ := actuatorForHO(t, repo, ho.URL)
	client := hoclient.NewFactory(hoclient.DefaultClientConfig()).ClientForHost(ho.URL)
	d, err := repo.Get(t.Context(), "deadbeef-dev") // the sampler's own copy, as in production
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); act.sampleCVDLoad(ctx, client, d, "cvddeadbeef") }()
	require.Eventually(t, func() bool {
		got, err := repo.Get(t.Context(), "deadbeef-dev")
		return err == nil && got.BootObservation != nil && len(got.BootObservation.Transitions) >= 3
	}, 10*time.Second, 5*time.Millisecond, "the trail never reached the row")
	cancel()
	<-done

	got, err := repo.Get(t.Context(), "deadbeef-dev")
	require.NoError(t, err)
	trail := got.BootObservation.Transitions
	assert.Equal(t, []string{"absent", "Starting", "Running"}, trailStatuses(trail))
	for i, tr := range trail {
		assert.False(t, tr.At.IsZero(), "every leg carries its wall-clock")
		if i > 0 {
			assert.False(t, tr.At.Before(trail[i-1].At), "stamps are monotonic")
		}
	}
}

// A resumed wait seeds from the row and EXTENDS the prior attempt's trail;
// restarting it would clobber the fetch leg's stamp and re-stamp a transition
// that happened in the previous attempt.
func TestSampleCVDLoad_ResumeExtendsTrail(t *testing.T) {
	t.Parallel()
	ho := transitionHO(t, []string{"Starting", "Running"})
	seedAt := time.Now().Add(-time.Minute)
	since := time.Now().Add(-2 * time.Minute) // mid-dwell: only transition writes persist
	repo := newSyncDeviceRepo()
	repo.put(&device.Device{
		ID: "deadbeef-dev", HostID: "h", Name: "dev-1", Status: device.StatusCreating,
		BootObservation: &device.BootObservation{
			TransientSince: &since, ObservedAt: since,
			Transitions: []device.BootTransition{{Status: "absent", At: seedAt}},
		},
	})
	act, _ := actuatorForHO(t, repo, ho.URL)
	client := hoclient.NewFactory(hoclient.DefaultClientConfig()).ClientForHost(ho.URL)
	d, err := repo.Get(t.Context(), "deadbeef-dev")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); act.sampleCVDLoad(ctx, client, d, "cvddeadbeef") }()
	require.Eventually(t, func() bool {
		got, err := repo.Get(t.Context(), "deadbeef-dev")
		return err == nil && len(got.BootObservation.Transitions) >= 3
	}, 10*time.Second, 5*time.Millisecond, "the resumed trail never grew")
	cancel()
	<-done

	got, err := repo.Get(t.Context(), "deadbeef-dev")
	require.NoError(t, err)
	trail := got.BootObservation.Transitions
	assert.Equal(t, []string{"absent", "Starting", "Running"}, trailStatuses(trail))
	assert.True(t, trail[0].At.Equal(seedAt), "the prior attempt's stamp survives the resume")
}

// An enriched sample from a trail-less feed point (the postpone path passes
// nil) must carry the row's trail forward, not clobber it.
func TestBootWatchTick_EnrichmentCarriesTrailForward(t *testing.T) {
	t.Parallel()
	ho := bootLoopHO(t, healthyKernelLog(), "")
	prev := dwellServed()
	prev.Transitions = []device.BootTransition{{Status: "absent", At: time.Now().Add(-time.Hour)}}
	repo := newSyncDeviceRepo()
	d := &device.Device{
		ID: "deadbeef-dev", HostID: "h", Name: "dev-1",
		Status: device.StatusCreating, BootObservation: prev,
	}
	repo.put(d)
	act, _ := actuatorForHO(t, repo, ho.URL)
	client := hoclient.NewFactory(hoclient.DefaultClientConfig()).ClientForHost(ho.URL)

	settled := act.bootWatchTick(t.Context(), client, d, "cvddeadbeef", startingGroup("dev-1"), nil)
	assert.False(t, settled)
	require.NotNil(t, d.BootObservation)
	assert.Equal(t, device.BootVerdictBooted, d.BootObservation.Verdict, "the sample was enriched")
	assert.Equal(t, prev.Transitions, d.BootObservation.Transitions,
		"an enriched write without sampler state keeps the persisted trail")
}

// The success commit clears the transient observation but must KEEP the
// transition trail. The trail exists for the creates that succeed, and the
// wipe that took the whole observation deleted the only durable record of the
// fetch/boot split.
func TestCreateSuccessKeepsTransitionTrail(t *testing.T) {
	t.Parallel()
	act, deviceRepo := newInFlightGroupActuator(t, "Running")
	since := time.Now().Add(-time.Minute)
	deviceRepo.Devices["deadbeef-dev"].BootObservation = &device.BootObservation{
		TransientSince: &since,
		ObservedAt:     since,
		Verdict:        device.BootVerdictNoMarkers,
		Transitions: []device.BootTransition{
			{Status: "absent", At: since},
			{Status: "Running", At: time.Now()},
		},
	}

	require.NoError(t, act.RunCreateCVD(t.Context(), "deadbeef-dev", nil))
	d := deviceRepo.Devices["deadbeef-dev"]
	assert.Equal(t, device.StatusRunning, d.Status)
	require.NotNil(t, d.BootObservation, "the trail must outlive the success commit")
	assert.Equal(t, []string{"absent", "Running"}, trailStatuses(d.BootObservation.Transitions))
	assert.Nil(t, d.BootObservation.TransientSince, "the dwell clock is transient state and must clear")
	assert.Empty(t, d.BootObservation.Verdict, "a stale verdict must not outlive the boot")
}

// Loop attribution: distinct builds looping on one host indict the host; a
// single build (however many devices) must not.
func TestRecordLoopAttribution(t *testing.T) {
	t.Parallel()
	looping := func(id, buildID string) *device.Device {
		return &device.Device{
			ID: id, HostID: "h", Name: "d" + id, Status: device.StatusError, BuildID: buildID,
			BootObservation: &device.BootObservation{Verdict: device.BootVerdictLooping},
		}
	}

	t.Run("distinct builds degrade the host", func(t *testing.T) {
		t.Parallel()
		hostRepo := hosttest.NewFakeRepo()
		hostRepo.Hosts["h"] = &host.Host{ID: "h", Status: host.StatusRunning}
		repo := devicetest.NewFakeRepo(looping("a", "B1"), looping("b", "B2"))
		act := newTestActuator(t, repo, hostRepo)

		act.recordLoopAttribution(t.Context(), looping("a", "B1"))
		assert.NotZero(t, hostRepo.Hosts["h"].ConsecutiveCreateFails,
			"two builds looping on one host is host evidence")
	})

	t.Run("one build does not poison the host", func(t *testing.T) {
		t.Parallel()
		hostRepo := hosttest.NewFakeRepo()
		hostRepo.Hosts["h"] = &host.Host{ID: "h", Status: host.StatusRunning}
		repo := devicetest.NewFakeRepo(looping("a", "B1"), looping("b", "B1"))
		act := newTestActuator(t, repo, hostRepo)

		act.recordLoopAttribution(t.Context(), looping("a", "B1"))
		assert.Zero(t, hostRepo.Hosts["h"].ConsecutiveCreateFails,
			"one build looping across devices indicts the build, not the host")
	})

	t.Run("a failed boot counts beside a loop", func(t *testing.T) {
		t.Parallel()
		hostRepo := hosttest.NewFakeRepo()
		hostRepo.Hosts["h"] = &host.Host{ID: "h", Status: host.StatusRunning}
		failed := looping("b", "B2")
		failed.BootObservation.Verdict = device.BootVerdictFailed
		repo := devicetest.NewFakeRepo(looping("a", "B1"), failed)
		act := newTestActuator(t, repo, hostRepo)

		act.recordLoopAttribution(t.Context(), looping("a", "B1"))
		assert.NotZero(t, hostRepo.Hosts["h"].ConsecutiveCreateFails,
			"a launcher timeout on a second build is host evidence too")
		assert.Equal(t, "boot failures across 2 distinct builds", hostRepo.Hosts["h"].LastCreateFailReason)
	})
}
