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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/auth/authtest"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/filelock"
)

const testServer = "https://cco.example"

// fakeAgents stands in for the agent processes. An agent is a lock the test
// holds, so liveness checks run against real locks without forking.
type fakeAgents struct {
	t     *testing.T
	store connectionStore

	mu       sync.Mutex
	nextPID  int
	locks    map[int]*os.File
	launched [][]string
	// onLaunch replaces the default launch, which plays a healthy agent.
	onLaunch func(args []string, log *os.File) (agentProcess, error)
	// attached sets how the default launch's agent ends its first attach
	// when it is started with --attach. Unset, adb sees the device online.
	attached func(st *connectionState)
	// ignoreTerminate plays an agent that only dies when killed.
	ignoreTerminate bool
	terminated      []int
	killed          []int
}

func newFakeAgents(t *testing.T) *fakeAgents {
	t.Helper()
	store, err := openConnectionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeAgents{t: t, store: store, nextPID: 1000, locks: map[int]*os.File{}}
	t.Cleanup(func() {
		for pid := range f.locks {
			f.release(pid)
		}
	})
	return f
}

// run makes a live agent for one instance and returns its state.
func (f *fakeAgents) run(deviceID, instance, address string) connectionState {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.store.paths(deviceID, instance)
	lock, err := filelock.TryLock(p.lock)
	if err != nil {
		f.t.Fatalf("locking %s: %v", p.lock, err)
	}
	f.nextPID++
	st := connectionState{
		PID: f.nextPID, DeviceID: deviceID, Instance: instance, Address: address,
		Server: testServer, StartedAt: time.Now().UTC(), LogFile: p.log,
	}
	if err := f.store.write(st); err != nil {
		f.t.Fatal(err)
	}
	f.locks[st.PID] = lock
	return st
}

// release plays the agent's death, in which the kernel drops its lock.
func (f *fakeAgents) release(pid int) {
	if l, ok := f.locks[pid]; ok {
		_ = l.Close()
		delete(f.locks, pid)
	}
}

func (f *fakeAgents) launch(_ context.Context, args []string, log *os.File) (agentProcess, error) {
	f.mu.Lock()
	f.launched = append(f.launched, args)
	f.mu.Unlock()
	if f.onLaunch != nil {
		return f.onLaunch(args, log)
	}
	deviceID, instance := agentTarget(f.t, args)
	st := f.run(deviceID, instance, "127.0.0.1:45001")
	if slices.Contains(args, "--attach") {
		f.finishAttach(st)
	}
	return agentProcess{pid: st.PID, exited: make(chan error)}, nil
}

// finishAttach plays an agent's first attach ending, as f.attached says.
func (f *fakeAgents) finishAttach(st connectionState) {
	f.t.Helper()
	st.Attach, st.AttachDone, st.ADBState, st.Online = true, true, adbOnline, true
	if f.attached != nil {
		f.attached(&st)
	}
	if err := f.store.write(st); err != nil {
		f.t.Fatal(err)
	}
}

// runNamed is run for a record that carries the device's name.
func (f *fakeAgents) runNamed(deviceID, name, instance, address string) connectionState {
	f.t.Helper()
	st := f.run(deviceID, instance, address)
	st.DeviceName = name
	if err := f.store.write(st); err != nil {
		f.t.Fatal(err)
	}
	return st
}

func (f *fakeAgents) terminate(pid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminated = append(f.terminated, pid)
	if !f.ignoreTerminate {
		f.release(pid)
	}
	return nil
}

func (f *fakeAgents) kill(pid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, pid)
	f.release(pid)
	return nil
}

// agentTarget reads the device and instance off an agent command line.
func agentTarget(t *testing.T, args []string) (deviceID, instance string) {
	t.Helper()
	i := slices.Index(args, "adb-agent")
	j := slices.Index(args, "--instance")
	if i < 0 || i+1 >= len(args) || j < 0 || j+1 >= len(args) {
		t.Fatalf("agent args %v lack adb-agent DEVICE_ID --instance NAME", args)
	}
	return args[i+1], args[j+1]
}

// recordingADB records every adb invocation. It answers get-state with states
// in turn, repeating the last, and with "device" when states is empty.
type recordingADB struct {
	mu     sync.Mutex
	calls  []string
	err    error
	states []string
}

func (r *recordingADB) run(_ context.Context, stdout, _ io.Writer, args ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, strings.Join(args, " "))
	if slices.Contains(args, "get-state") {
		state := adbOnline
		if len(r.states) > 0 {
			state = r.states[0]
			if len(r.states) > 1 {
				r.states = r.states[1:]
			}
		}
		fmt.Fprintln(stdout, state)
		return nil
	}
	return r.err
}

func (r *recordingADB) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

func newTestManager(t *testing.T) (*connectionManager, *fakeAgents, *recordingADB) {
	t.Helper()
	agents := newFakeAgents(t)
	adb := &recordingADB{}
	m := &connectionManager{
		store: agents.store, procs: agents, adb: adb.run,
		readyTimeout: 5 * time.Second, stopTimeout: 200 * time.Millisecond, poll: time.Millisecond,
	}
	return m, agents, adb
}

func TestConnectionStoreWritesOwnerOnlyState(t *testing.T) {
	_, agents, _ := newTestManager(t)
	want := agents.run("dev-1", "1", "127.0.0.1:40000")

	got, ok, err := agents.store.lookup("dev-1", "1")
	if err != nil || !ok {
		t.Fatalf("lookup() = %v, %v; want the live agent", ok, err)
	}
	if got.PID != want.PID || got.Address != want.Address || got.Server != testServer || !got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("lookup() = %+v, want %+v", got, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(agents.store.paths("dev-1", "1").state)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("state file mode = %o, want 600", mode)
		}
	}
	var decoded map[string]any
	data, _ := os.ReadFile(agents.store.paths("dev-1", "1").state)
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"pid", "device_id", "device_name", "instance", "address", "server", "started_at", "log_file",
		"attach", "attach_done", "adb_state", "online", "reattaches",
	} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("state file lacks %q: %s", key, data)
		}
	}
}

func TestConnectionStorePrunesDeadAgents(t *testing.T) {
	_, agents, _ := newTestManager(t)
	live := agents.run("dev-1", "1", "127.0.0.1:40000")
	dead := agents.run("dev-2", "1", "127.0.0.1:40001")
	deadPaths := agents.store.paths("dev-2", "1")
	if err := os.WriteFile(deadPaths.log, []byte("old log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agents.release(dead.PID)

	got, err := agents.store.list()
	if err != nil {
		t.Fatalf("list() error = %v", err)
	}
	if len(got) != 1 || got[0].PID != live.PID {
		t.Fatalf("list() = %+v, want only the live agent %d", got, live.PID)
	}
	for _, path := range []string{deadPaths.state, deadPaths.lock, deadPaths.log} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived the prune (stat err %v)", path, err)
		}
	}
	if _, err := os.Stat(agents.store.paths("dev-1", "1").state); err != nil {
		t.Errorf("the live agent's state was pruned: %v", err)
	}

	// A lookup prunes the same way.
	agents.release(live.PID)
	if _, ok, err := agents.store.lookup("dev-1", "1"); ok || err != nil {
		t.Errorf("lookup() of a dead agent = %v, %v; want false, nil", ok, err)
	}
	if _, err := os.Stat(agents.store.paths("dev-1", "1").state); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("lookup() left the dead agent's state (stat err %v)", err)
	}
}

func TestConnectionStoreListIsEmptyNotNil(t *testing.T) {
	_, agents, _ := newTestManager(t)
	got, err := agents.store.list()
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("list() = %#v, %v; want an empty non-nil slice", got, err)
	}
}

func TestFileSafeKeepsNamesInsideTheDirectory(t *testing.T) {
	for in, want := range map[string]string{
		"dev-1":    "dev-1",
		"../../x":  ".._.._x",
		"a b/c\\d": "a_b_c_d",
	} {
		if got := fileSafe(in); got != want {
			t.Errorf("fileSafe(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConnectStartsAnAgentAndAttaches(t *testing.T) {
	m, agents, adb := newTestManager(t)

	st, reused, err := m.connect(t.Context(), connectRequest{
		server: testServer, deviceID: "dev-1", instance: "1", attach: true, waitOnline: time.Minute,
	})
	if err != nil {
		t.Fatalf("connect() error = %v", err)
	}
	if reused {
		t.Error("connect() reported reuse with no agent running")
	}
	if st.Address != "127.0.0.1:45001" || !st.Online {
		t.Errorf("state = %+v, want the agent's address, online", st)
	}
	if len(agents.launched) != 1 {
		t.Fatalf("launched %d agents, want 1", len(agents.launched))
	}
	args := agents.launched[0]
	for _, want := range [][]string{{"--server", testServer}, {"--timeout", "0"}, {"--port", "0"}, {"--wait-online", "1m0s"}} {
		i := slices.Index(args, want[0])
		if i < 0 || i+1 >= len(args) || args[i+1] != want[1] {
			t.Errorf("agent args %v lack %s %s", args, want[0], want[1])
		}
	}
	if !slices.Contains(args, "--attach") {
		t.Errorf("agent args %v lack --attach", args)
	}
	// The agent owns the attach, so the parent runs no adb.
	if got := adb.got(); len(got) != 0 {
		t.Errorf("adb calls = %v, want none from the parent", got)
	}
}

func TestConnectWithoutAttachLeavesAdbToTheUser(t *testing.T) {
	m, agents, adb := newTestManager(t)
	if _, _, err := m.connect(t.Context(), connectRequest{server: testServer, deviceID: "dev-1", instance: "1"}); err != nil {
		t.Fatalf("connect() error = %v", err)
	}
	if args := agents.launched[0]; slices.Contains(args, "--attach") || slices.Contains(args, "--wait-online") {
		t.Errorf("agent args %v attach although the request does not", args)
	}
	if got := adb.got(); len(got) != 0 {
		t.Errorf("adb calls = %v, want none", got)
	}
}

func TestConnectReusesARunningAgent(t *testing.T) {
	m, agents, adb := newTestManager(t)
	running := agents.run("dev-1", "1", "127.0.0.1:40000")

	st, reused, err := m.connect(t.Context(), connectRequest{
		server: testServer, deviceID: "dev-1", instance: "1", port: 40000, attach: true,
	})
	if err != nil {
		t.Fatalf("connect() error = %v", err)
	}
	if !reused || st.PID != running.PID {
		t.Errorf("connect() = pid %d reused %v, want the running agent %d", st.PID, reused, running.PID)
	}
	if len(agents.launched) != 0 {
		t.Errorf("launched %v, want no new agent", agents.launched)
	}
	// A restarted adb server has forgotten the address, so reuse attaches again.
	if got := adb.got(); !slices.Equal(got, []string{"connect 127.0.0.1:40000"}) {
		t.Errorf("adb calls = %v, want one connect", got)
	}
}

func TestConnectRefusesAConflictingAgent(t *testing.T) {
	m, agents, _ := newTestManager(t)
	agents.run("dev-1", "1", "127.0.0.1:40000")

	for _, tc := range []struct {
		name string
		req  connectRequest
		want string
	}{
		{"other server", connectRequest{server: "https://other.example", deviceID: "dev-1", instance: "1"}, "already connected through"},
		{"other port", connectRequest{server: testServer, deviceID: "dev-1", instance: "1", port: 40001}, "already connected on 127.0.0.1:40000"},
	} {
		if _, _, err := m.connect(t.Context(), tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
	if len(agents.launched) != 0 {
		t.Errorf("launched %v, want none", agents.launched)
	}
}

func TestConnectNamesTheLogWhenTheAgentDies(t *testing.T) {
	m, agents, adb := newTestManager(t)
	agents.onLaunch = func(_ []string, log *os.File) (agentProcess, error) {
		fmt.Fprintln(log, "Error: failed to get device: device not found")
		exited := make(chan error, 1)
		exited <- errors.New("exit status 1")
		return agentProcess{pid: 4242, exited: exited}, nil
	}

	_, _, err := m.connect(t.Context(), connectRequest{server: testServer, deviceID: "dev-1", instance: "1", attach: true})
	logPath := agents.store.paths("dev-1", "1").log
	if err == nil || !strings.Contains(err.Error(), logPath) || !strings.Contains(err.Error(), "device not found") {
		t.Errorf("err = %v, want the log path %s and its last line", err, logPath)
	}
	if got := adb.got(); len(got) != 0 {
		t.Errorf("adb calls = %v, want none for a dead agent", got)
	}
}

func TestConnectTimesOutAndKillsASilentAgent(t *testing.T) {
	m, agents, _ := newTestManager(t)
	m.readyTimeout = 20 * time.Millisecond
	agents.onLaunch = func([]string, *os.File) (agentProcess, error) {
		return agentProcess{pid: 4243, exited: make(chan error)}, nil
	}

	_, _, err := m.connect(t.Context(), connectRequest{server: testServer, deviceID: "dev-1", instance: "1"})
	if err == nil || !strings.Contains(err.Error(), "was not listening") || !strings.Contains(err.Error(), ".log") {
		t.Errorf("err = %v, want a timeout naming the log file", err)
	}
	if !slices.Equal(agents.killed, []int{4243}) {
		t.Errorf("killed %v, want the silent agent 4243", agents.killed)
	}
}

func TestConnectStopsANewAgentWhenAttachFails(t *testing.T) {
	m, agents, _ := newTestManager(t)
	agents.attached = func(st *connectionState) {
		st.ADBState, st.Online, st.AttachError = "", false, "adb is not on PATH"
	}

	_, _, err := m.connect(t.Context(), connectRequest{server: testServer, deviceID: "dev-1", instance: "1", attach: true})
	if err == nil || !strings.Contains(err.Error(), "adb is not on PATH") {
		t.Fatalf("connect() error = %v, want the agent's attach error", err)
	}
	if len(agents.terminated) != 1 {
		t.Errorf("terminated %v, want the new agent stopped", agents.terminated)
	}
	if st, ok, _ := agents.store.lookup("dev-1", "1"); ok {
		t.Errorf("state %+v survived the failed attach", st)
	}
}

func TestConnectExitsWithTheCodeOfTheAgentsFailedDial(t *testing.T) {
	m, agents, _ := newTestManager(t)
	agents.attached = func(st *connectionState) {
		st.ADBState, st.Online = "offline", false
		st.AttachError, st.AttachExit = "opening a device stream for dev-1: forbidden", ExitAuth
	}

	_, _, err := m.connect(t.Context(), connectRequest{server: testServer, deviceID: "dev-1", instance: "1", attach: true, waitOnline: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("connect() error = %v, want the agent's dial error", err)
	}
	if code := exitCodeFor(err, false); code != ExitAuth {
		t.Errorf("exit %d, want %d from the failed dial", code, ExitAuth)
	}
	if len(agents.terminated) != 1 {
		t.Errorf("terminated %v, want the new agent stopped", agents.terminated)
	}
}

// The state file exists as soon as the agent listens, so the parent waits for
// the agent to say how its attach ended.
func TestConnectWaitsForTheAgentToSeeTheDeviceOnline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, agents, adb := newTestManager(t)
		const attachTakes = 3 * time.Second
		agents.onLaunch = func(args []string, _ *os.File) (agentProcess, error) {
			deviceID, instance := agentTarget(t, args)
			st := agents.run(deviceID, instance, "127.0.0.1:45001")
			st.Attach = true
			if err := agents.store.write(st); err != nil {
				t.Fatal(err)
			}
			go func() {
				time.Sleep(attachTakes)
				agents.finishAttach(st)
			}()
			return agentProcess{pid: st.PID, exited: make(chan error)}, nil
		}

		start := time.Now()
		st, _, err := m.connect(t.Context(), connectRequest{
			server: testServer, deviceID: "dev-1", instance: "1", attach: true, waitOnline: time.Minute,
		})
		if err != nil {
			t.Fatalf("connect() error = %v", err)
		}
		if !st.Online || time.Since(start) < attachTakes {
			t.Errorf("connect() returned %+v after %s, want the online state after %s", st, time.Since(start), attachTakes)
		}
		if got := adb.got(); len(got) != 0 {
			t.Errorf("adb calls = %v, want none from the parent", got)
		}
	})
}

// An agent that listens but never finishes attaching is reported like a
// timed-out online wait, and left serving.
func TestConnectReportsAnAgentStuckAttaching(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, agents, _ := newTestManager(t)
		agents.onLaunch = func(args []string, _ *os.File) (agentProcess, error) {
			deviceID, instance := agentTarget(t, args)
			st := agents.run(deviceID, instance, "127.0.0.1:45001")
			return agentProcess{pid: st.PID, exited: make(chan error)}, nil
		}

		_, _, err := m.connect(t.Context(), connectRequest{
			server: testServer, deviceID: "dev-1", instance: "1", attach: true, waitOnline: 5 * time.Second,
		})
		if _, ok := errors.AsType[*adbOfflineError](err); !ok || !strings.Contains(err.Error(), "last adb state: no answer") {
			t.Fatalf("connect() error = %v, want an *adbOfflineError with no answer", err)
		}
		if len(agents.killed) != 0 {
			t.Errorf("killed %v, want the listening agent left serving", agents.killed)
		}
	})
}

// The parent cannot see the agent's sessions, so a timed-out wait points to
// the agent's log. The agent keeps serving, because the device may still
// come online.
func TestConnectTimeoutNamesTheAgentLog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, agents, _ := newTestManager(t)
		agents.attached = func(st *connectionState) { st.ADBState, st.Online = "offline", false }

		_, _, err := m.connect(t.Context(), connectRequest{
			server: testServer, deviceID: "dev-1", instance: "1", attach: true, waitOnline: 5 * time.Second,
		})
		if _, ok := errors.AsType[*adbOfflineError](err); !ok {
			t.Fatalf("connect() error = %v, want an *adbOfflineError", err)
		}
		for _, want := range []string{agents.store.paths("dev-1", "1").log, "last adb state: offline", "cfctl device disconnect dev-1"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
		if len(agents.terminated) != 0 || len(agents.killed) != 0 {
			t.Errorf("terminated %v killed %v, want the agent left serving", agents.terminated, agents.killed)
		}
		if _, ok, _ := agents.store.lookup("dev-1", "1"); !ok {
			t.Error("the agent's state is gone after the wait timed out")
		}
	})
}

func TestDisconnectStopsOnlyMatchingAgents(t *testing.T) {
	m, agents, adb := newTestManager(t)
	a1 := agents.run("dev-a", "1", "127.0.0.1:40001")
	a2 := agents.run("dev-a", "2", "127.0.0.1:40002")
	b1 := agents.run("dev-b", "1", "127.0.0.1:40003")

	// Each step runs against what the earlier steps left running.
	for _, step := range []struct {
		filter connectionFilter
		want   []int
	}{
		{connectionFilter{device: "dev-a", instance: "2"}, []int{a2.PID}},
		{connectionFilter{device: "dev-a"}, []int{a1.PID}},
		{connectionFilter{device: "dev-c"}, []int{}},
		{connectionFilter{all: true}, []int{b1.PID}},
	} {
		stopped, err := m.disconnect(t.Context(), step.filter)
		pids := []int{}
		for _, st := range stopped {
			pids = append(pids, st.PID)
		}
		if err != nil || stopped == nil || !slices.Equal(pids, step.want) {
			t.Fatalf("disconnect(%+v) = %v, %v; want pids %v", step.filter, pids, err, step.want)
		}
	}
	if _, err := os.Stat(agents.store.paths("dev-a", "2").state); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dev-a/2 state survived the disconnect (stat err %v)", err)
	}

	want := []string{"disconnect 127.0.0.1:40002", "disconnect 127.0.0.1:40001", "disconnect 127.0.0.1:40003"}
	if got := adb.got(); !slices.Equal(got, want) {
		t.Errorf("adb calls = %v, want %v", got, want)
	}
	if len(agents.killed) != 0 {
		t.Errorf("killed %v, want every agent to stop on the signal", agents.killed)
	}
}

func TestDisconnectKillsAnAgentThatIgnoresTheSignal(t *testing.T) {
	m, agents, _ := newTestManager(t)
	agents.ignoreTerminate = true
	st := agents.run("dev-a", "1", "127.0.0.1:40001")

	stopped, err := m.disconnect(t.Context(), connectionFilter{device: "dev-a"})
	if err != nil || len(stopped) != 1 {
		t.Fatalf("disconnect() = %+v, %v; want the agent stopped", stopped, err)
	}
	if !slices.Equal(agents.terminated, []int{st.PID}) || !slices.Equal(agents.killed, []int{st.PID}) {
		t.Errorf("terminated %v killed %v, want signal then kill of %d", agents.terminated, agents.killed, st.PID)
	}
}

func TestResolveConnectedDevice(t *testing.T) {
	live := []connectionState{
		{DeviceID: "id-a", DeviceName: "phone", Instance: "1"},
		{DeviceID: "id-a", DeviceName: "phone", Instance: "2"},
		{DeviceID: "id-b", DeviceName: "twin", Instance: "1"},
		{DeviceID: "id-c", DeviceName: "twin", Instance: "1"},
		// A record whose name is another record's ID.
		{DeviceID: "id-d", DeviceName: "id-b", Instance: "1"},
	}
	for _, tc := range []struct {
		device, want string
		ambiguous    bool
	}{
		{device: "id-a", want: "id-a"},
		{device: "phone", want: "id-a"},
		{device: "id-b", want: "id-b"},
		{device: "nobody", want: "nobody"},
		{device: "twin", ambiguous: true},
	} {
		got, err := resolveConnectedDevice(live, tc.device)
		ambiguous, isAmbiguous := errors.AsType[*ambiguousConnectionError](err)
		if isAmbiguous != tc.ambiguous || (!tc.ambiguous && (err != nil || got != tc.want)) {
			t.Errorf("resolveConnectedDevice(%q) = %q, %v; want %q, ambiguous %v", tc.device, got, err, tc.want, tc.ambiguous)
			continue
		}
		if tc.ambiguous && !slices.Equal(ambiguous.deviceIDs(), []string{"id-b", "id-c"}) {
			t.Errorf("ambiguous devices = %v, want id-b and id-c", ambiguous.deviceIDs())
		}
	}
}

// The name comes from the local records, so neither command calls the server.
func TestE2E_DeviceDisconnectByName(t *testing.T) {
	m, agents, _ := newTestManager(t)
	phone1 := agents.runNamed("id-a", "phone", "1", "127.0.0.1:41001")
	phone2 := agents.runNamed("id-a", "phone", "2", "127.0.0.1:41002")
	agents.runNamed("id-b", "twin", "1", "127.0.0.1:41003")
	twin2 := agents.runNamed("id-c", "twin", "1", "127.0.0.1:41004")
	useTestManager(t, m)
	backend := http.NotFoundHandler()

	var stopped []connectionState
	out, err := runCLI(t, backend, "device", "disconnect", "phone", "--json")
	if err != nil {
		t.Fatalf("device disconnect phone: %v", err)
	}
	if err := json.Unmarshal([]byte(out), &stopped); err != nil || len(stopped) != 2 ||
		stopped[0].PID != phone1.PID || stopped[1].PID != phone2.PID {
		t.Errorf("disconnect phone = %s (%v), want both instances of id-a", out, err)
	}

	stderr := captureStderr(t, func() {
		_, err = runCLI(t, backend, "device", "disconnect", "twin")
	})
	if code := exitCodeFor(err, false); code != ExitConflict {
		t.Errorf("disconnect twin: err %v exit %d, want %d", err, code, ExitConflict)
	}
	for _, want := range []string{"DEVICE", "id-b", "id-c", "127.0.0.1:41004"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr %q lacks %q", stderr, want)
		}
	}
	if live, _ := agents.store.list(); len(live) != 2 {
		t.Errorf("live agents after the ambiguous name = %+v, want both twins still running", live)
	}

	// An ID still works when a name is shared.
	out, err = runCLI(t, backend, "device", "disconnect", "id-c", "--json")
	if err := json.Unmarshal([]byte(out), &stopped); err != nil || len(stopped) != 1 || stopped[0].PID != twin2.PID {
		t.Errorf("disconnect id-c = %s (%v), want the one agent of id-c", out, err)
	}
	if err != nil {
		t.Errorf("disconnect id-c: %v", err)
	}
}

func TestDeviceConnectFlagRefusals(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--all-instances"}, "needs --background"},
		{[]string{"--background", "--all-instances", "--instance", "1"}, "mutually exclusive"},
		{[]string{"--background", "--", "shell", "true"}, "cannot run a passthrough command"},
	}
	for _, tc := range cases {
		_, err := runCLI(t, http.NotFoundHandler(), append([]string{"device", "connect", "dev-1"}, tc.args...)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want it to mention %q", tc.args, err, tc.want)
		}
	}
}

func TestDeviceDisconnectFlagRefusals(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "name a device by ID or name, or pass --all"},
		{[]string{"dev-1", "--all"}, "drop --all"},
	} {
		_, err := runCLI(t, http.NotFoundHandler(), append([]string{"device", "disconnect"}, tc.args...)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want it to mention %q", tc.args, err, tc.want)
		}
	}
}

// useTestManager points the commands at m for the rest of the test.
func useTestManager(t *testing.T, m *connectionManager) {
	t.Helper()
	prev := openConnectionManager
	openConnectionManager = func() (*connectionManager, error) { return m, nil }
	t.Cleanup(func() { openConnectionManager = prev })
}

func TestE2E_DeviceConnectBackgroundAllInstances(t *testing.T) {
	m, agents, adb := newTestManager(t)
	ports := map[string]string{"1": "127.0.0.1:41001", "2": "127.0.0.1:41002"}
	agents.onLaunch = func(args []string, _ *os.File) (agentProcess, error) {
		deviceID, instance := agentTarget(t, args)
		st := agents.run(deviceID, instance, ports[instance])
		if slices.Contains(args, "--attach") {
			agents.finishAttach(st)
		}
		return agentProcess{pid: st.PID, exited: make(chan error)}, nil
	}
	useTestManager(t, m)

	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/devices/"+testDeviceID {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"id":%q,"name":"n","host_id":"h1","status":"running","instances":[{"name":"1"},{"name":"2"}]}`, testDeviceID)
	})
	out, err := runCLI(t, backend, "device", "connect", testDeviceID, "--background", "--all-instances", "--json")
	if err != nil {
		t.Fatalf("device connect --background --all-instances: %v", err)
	}
	var states []connectionState
	if err := json.Unmarshal([]byte(out), &states); err != nil {
		t.Fatalf("stdout is not a JSON list: %v\n%s", err, out)
	}
	if len(states) != 2 || states[0].Instance != "1" || states[1].Instance != "2" || !states[0].Online || !states[1].Online {
		t.Errorf("states = %+v, want instances 1 and 2 online", states)
	}
	for _, args := range agents.launched {
		if !slices.Contains(args, "--attach") {
			t.Errorf("agent args %v lack --attach", args)
		}
	}
	if got := adb.got(); len(got) != 0 {
		t.Errorf("adb calls = %v, want none from the parent", got)
	}
}

func TestE2E_DeviceConnectionsAndDisconnect(t *testing.T) {
	m, agents, _ := newTestManager(t)
	agents.run("dev-1", "1", "127.0.0.1:41001")
	agents.run("dev-1", "2", "127.0.0.1:41002")
	useTestManager(t, m)
	// Neither command calls the server.
	backend := http.NotFoundHandler()

	out, err := runCLI(t, backend, "device", "connections")
	if err != nil {
		t.Fatalf("device connections: %v", err)
	}
	if !strings.HasPrefix(out, "DEVICE") || !strings.Contains(out, "127.0.0.1:41001") || !strings.Contains(out, "127.0.0.1:41002") {
		t.Errorf("connections table = %q, want both agents", out)
	}
	for _, want := range []string{"STATE", "REATTACHES", "not attached"} {
		if !strings.Contains(out, want) {
			t.Errorf("connections table = %q, want %q", out, want)
		}
	}

	var states []connectionState
	out, err = runCLI(t, backend, "device", "disconnect", "dev-1", "--instance", "1", "--json")
	if err != nil {
		t.Fatalf("device disconnect: %v", err)
	}
	if err := json.Unmarshal([]byte(out), &states); err != nil || len(states) != 1 || states[0].Instance != "1" {
		t.Errorf("disconnect --json = %s (%v), want instance 1 only", out, err)
	}

	out, err = runCLI(t, backend, "device", "disconnect", "dev-9")
	if err != nil || !strings.Contains(out, "nothing to stop") {
		t.Errorf("disconnect with no match = %q, %v; want exit 0 and a note", out, err)
	}
}

func TestADBAgentIsHidden(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"device", "--help"}} {
		if out := renderHelp(t, args...); strings.Contains(out, "adb-agent") {
			t.Errorf("%v help shows the hidden agent:\n%s", args, out)
		}
	}
	for shell, script := range map[string]string{"bash": bashCompletionScript, "zsh": zshCompletionScript, "fish": fishCompletionScript} {
		if strings.Contains(script, "adb-agent") {
			t.Errorf("%s completion offers the hidden agent", shell)
		}
	}
	// The agent still parses, because 'connect --background' runs it.
	if _, _, err := parseCLI(t, []string{"device", "adb-agent", "d1", "--instance", "1"}, &config.Config{Server: testServer}); err != nil {
		t.Errorf("parsing the agent command line: %v", err)
	}
}

func TestDeviceCreateConnectImpliesWait(t *testing.T) {
	c := DeviceCreateCmd{Connect: true}
	c.applyDefaults()
	if !c.Wait {
		t.Error("--connect did not turn on --wait")
	}
	c = DeviceCreateCmd{Connect: true, DryRun: true}
	c.applyDefaults()
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "--dry-run") {
		t.Errorf("validate() = %v, want --connect with --dry-run refused", err)
	}
}

func TestE2E_DeviceCreateConnectAttachesEveryInstance(t *testing.T) {
	m, agents, adb := newTestManager(t)
	useTestManager(t, m)

	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/devices":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"d1","name":"n","host_id":"h1","status":"pending"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/devices/d1":
			fmt.Fprint(w, `{"id":"d1","name":"n","host_id":"h1","status":"running","instances":[{"name":"1"},{"name":"2"}]}`)
		default:
			http.NotFound(w, r)
		}
	})
	out, err := runCLI(t, backend, "device", "create", "--connect", "--json")
	if err != nil {
		t.Fatalf("device create --connect: %v", err)
	}
	var got struct {
		ID          string            `json:"id"`
		Status      string            `json:"status"`
		Connections []connectionState `json:"connections"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	if got.Status != "running" || len(got.Connections) != 2 {
		t.Errorf("create --connect = %+v, want the running device with two connections", got)
	}
	if len(agents.launched) != 2 {
		t.Errorf("launched %d agents, want one per instance", len(agents.launched))
	}
	for _, args := range agents.launched {
		if !slices.Contains(args, "--attach") {
			t.Errorf("agent args %v lack --attach", args)
		}
	}
	if calls := adb.got(); len(calls) != 0 {
		t.Errorf("adb calls = %v, want none from the parent, since each agent attaches", calls)
	}
}

func TestReapKeepsTheLogOfAStartingAgent(t *testing.T) {
	_, agents, _ := newTestManager(t)
	p := agents.store.paths("dev-1", "1")
	// The parent has created the log, and the agent has not taken the lock.
	if err := os.WriteFile(p.log, []byte("starting\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if agents.store.reap(p) {
		t.Error("reap() reported a live agent although no lock is held")
	}
	if _, err := os.Stat(p.log); err != nil {
		t.Errorf("reap() removed the log of an agent that has no state yet: %v", err)
	}
	if _, err := os.Stat(p.lock); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("reap() left its own lock file behind (stat err %v)", err)
	}

	agents.run("dev-1", "1", "127.0.0.1:40000")
	if !agents.store.reap(p) {
		t.Error("reap() missed the agent holding the lock")
	}
	for _, path := range []string{p.state, p.lock, p.log} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("reap() touched %s of a live agent: %v", path, err)
		}
	}
}

func TestListSweepsOnlyStaleOrphanLogs(t *testing.T) {
	_, agents, _ := newTestManager(t)
	agents.run("dev-1", "1", "127.0.0.1:40000")
	liveLog := agents.store.paths("dev-1", "1").log
	staleLog := agents.store.paths("dev-2", "1").log
	freshLog := agents.store.paths("dev-3", "1").log
	old := time.Now().Add(-2 * staleLogAge)
	for _, path := range []string{liveLog, staleLog, freshLog} {
		if err := os.WriteFile(path, []byte("log\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{liveLog, staleLog} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := agents.store.list(); err != nil {
		t.Fatalf("list() error = %v", err)
	}
	if _, err := os.Stat(staleLog); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("list() kept a stale log no state file names (stat err %v)", err)
	}
	for _, path := range []string{liveLog, freshLog} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("list() removed %s: %v", path, err)
		}
	}
}

func TestE2E_DeviceCreateConnectRefusesADeviceWithoutInstances(t *testing.T) {
	m, agents, _ := newTestManager(t)
	useTestManager(t, m)

	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/devices":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"d1","name":"n","host_id":"h1","status":"pending"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/devices/d1":
			fmt.Fprint(w, `{"id":"d1","name":"n","host_id":"h1","status":"running","instances":[]}`)
		default:
			http.NotFound(w, r)
		}
	})
	_, err := runCLI(t, backend, "device", "create", "--connect")
	if err == nil || !strings.Contains(err.Error(), "no instances") {
		t.Errorf("err = %v, want a refusal naming the missing instances", err)
	}
	if len(agents.launched) != 0 {
		t.Errorf("launched %v, want no agent", agents.launched)
	}
}

// agentClient talks to a backend that serves device dev-1 when deviceExists
// and answers 404 to everything else, the ADB route included.
func agentClient(t *testing.T, deviceExists bool) *cco.Client {
	t.Helper()
	return agentClientFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if deviceExists && r.Method == http.MethodGet && r.URL.Path == "/v2/devices/dev-1" {
			fmt.Fprint(w, `{"id":"dev-1","name":"n","host_id":"h1","status":"running","instances":[{"name":"1"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
}

// retiredDeviceClient talks to a backend where device dev-1 reads back with
// the given status and the ADB route answers 502.
func retiredDeviceClient(t *testing.T, status string) *cco.Client {
	t.Helper()
	return agentClientFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/devices/dev-1":
			fmt.Fprintf(w, `{"id":"dev-1","name":"n","host_id":"h1","status":%q,"instances":[{"name":"1"}]}`, status)
		case strings.HasSuffix(r.URL.Path, "/adb"):
			http.Error(w, "Failed to connect to device", http.StatusBadGateway)
		default:
			http.NotFound(w, r)
		}
	}))
}

// agentClientFor builds the agent's client against handler.
func agentClientFor(t *testing.T, handler http.Handler) *cco.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	keyPath, _ := authtest.WriteKeyFile(t)
	// The agent runs with --timeout 0, which the WebSocket dial needs.
	client, err := cco.NewClient(t.Context(), srv.URL,
		&config.Config{Server: srv.URL, CredentialsPath: keyPath}, 0)
	if err != nil {
		t.Fatalf("cco.NewClient: %v", err)
	}
	return client
}

// startAgent runs the agent for dev-1 instance 1 in the test process.
func startAgent(ctx context.Context, client *cco.Client) chan error {
	done := make(chan error, 1)
	go func() {
		done <- (&DeviceADBAgentCmd{ID: "dev-1", Instance: "1"}).Run(ctx, &CLI{Server: testServer}, client)
	}()
	return done
}

// awaitAgentState polls until the agent's state file names a live agent.
func awaitAgentState(t *testing.T, store connectionStore, done chan error) connectionState {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		st, ok, err := store.lookup("dev-1", "1")
		if err != nil {
			t.Fatalf("lookup() error = %v", err)
		}
		if ok {
			return st
		}
		select {
		case err := <-done:
			t.Fatalf("the agent exited before it was listening: %v", err)
		case <-deadline:
			t.Fatal("the agent wrote no state within 10s")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// awaitAgentExit waits for the agent to return and checks it left no files.
func awaitAgentExit(t *testing.T, store connectionStore, done chan error) error {
	t.Helper()
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the agent kept running")
	}
	p := store.paths("dev-1", "1")
	for _, path := range []string{p.state, p.lock, p.log} {
		if _, serr := os.Stat(path); !errors.Is(serr, os.ErrNotExist) {
			t.Errorf("the agent left %s behind (stat err %v)", path, serr)
		}
	}
	return err
}

func TestADBAgentStopsWhenTheDeviceIsGone(t *testing.T) {
	m, _, adb := newTestManager(t)
	useTestManager(t, m)
	p := m.store.paths("dev-1", "1")
	// The parent creates the log before it starts the agent.
	if err := os.WriteFile(p.log, []byte("starting\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	done := startAgent(t.Context(), agentClient(t, true))
	st := awaitAgentState(t, m.store, done)
	if st.PID != os.Getpid() || st.Server != testServer || st.LogFile != p.log {
		t.Errorf("state = %+v, want this process, the server and the log path", st)
	}
	// The state file is the readiness signal, so the listener already accepts.
	var dialer net.Dialer
	local, err := dialer.DialContext(t.Context(), "tcp", st.Address)
	if err != nil {
		t.Fatalf("dialing the agent at %s: %v", st.Address, err)
	}
	defer local.Close()

	// The connection opens a device stream, and the ADB route answers 404.
	if err := awaitAgentExit(t, m.store, done); err != nil {
		t.Errorf("Run() error = %v, want nil once the device is gone", err)
	}
	if got := adb.got(); !slices.Equal(got, []string{"disconnect " + st.Address}) {
		t.Errorf("adb calls = %v, want one disconnect of %s", got, st.Address)
	}
}

// A server that does not refuse retired devices on the ADB route answers 502,
// so the agent learns the device is gone from its status.
func TestADBAgentStopsWhenTheDeviceIsRetired(t *testing.T) {
	for _, status := range []string{cco.StatusDeleted, cco.StatusDeleting} {
		t.Run(status, func(t *testing.T) {
			m, _, adb := newTestManager(t)
			useTestManager(t, m)

			done := startAgent(t.Context(), retiredDeviceClient(t, status))
			st := awaitAgentState(t, m.store, done)
			var dialer net.Dialer
			local, err := dialer.DialContext(t.Context(), "tcp", st.Address)
			if err != nil {
				t.Fatalf("dialing the agent at %s: %v", st.Address, err)
			}
			defer local.Close()

			if err := awaitAgentExit(t, m.store, done); err != nil {
				t.Errorf("Run() error = %v, want nil once the device is %s", err, status)
			}
			if got := adb.got(); !slices.Equal(got, []string{"disconnect " + st.Address}) {
				t.Errorf("adb calls = %v, want one disconnect of %s", got, st.Address)
			}
		})
	}
}

func TestADBAgentStopsWhenItsContextEnds(t *testing.T) {
	m, _, adb := newTestManager(t)
	useTestManager(t, m)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := startAgent(ctx, agentClient(t, true))
	st := awaitAgentState(t, m.store, done)
	// main cancels the command context on SIGTERM.
	cancel()
	if err := awaitAgentExit(t, m.store, done); err != nil {
		t.Errorf("Run() error = %v, want nil on SIGTERM", err)
	}
	if got := adb.got(); !slices.Equal(got, []string{"disconnect " + st.Address}) {
		t.Errorf("adb calls = %v, want one disconnect of %s", got, st.Address)
	}
}

func TestADBAgentWritesNoStateForAMissingDevice(t *testing.T) {
	m, _, adb := newTestManager(t)
	useTestManager(t, m)

	done := startAgent(t.Context(), agentClient(t, false))
	err := awaitAgentExit(t, m.store, done)
	if err == nil || !strings.Contains(err.Error(), "failed to get device") {
		t.Errorf("Run() error = %v, want the failed device lookup", err)
	}
	if got := adb.got(); len(got) != 0 {
		t.Errorf("adb calls = %v, want none before the agent listened", got)
	}
}
