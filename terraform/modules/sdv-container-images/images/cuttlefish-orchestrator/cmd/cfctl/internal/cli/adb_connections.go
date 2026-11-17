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
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/filelock"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// connectionState is the state file of one background ADB agent.
type connectionState struct {
	PID        int       `json:"pid"`
	DeviceID   string    `json:"device_id"`
	DeviceName string    `json:"device_name"`
	Instance   string    `json:"instance"`
	Address    string    `json:"address"`
	Server     string    `json:"server"`
	StartedAt  time.Time `json:"started_at"`
	LogFile    string    `json:"log_file"`
	// Attach reports that the agent keeps the local adb attached to Address.
	Attach bool `json:"attach"`
	// AttachDone reports that the agent's first 'adb connect' and the wait
	// for the device to come online have ended.
	AttachDone bool `json:"attach_done"`
	// AttachError is the failure of the first 'adb connect', or the failed
	// stream dial that ended the online wait.
	AttachError string `json:"attach_error,omitempty"`
	// AttachExit is the exit code of a failed stream dial in AttachError,
	// and 0 for any other failure.
	AttachExit int `json:"attach_exit,omitempty"`
	// ADBState is adb's last answer for Address, such as "device",
	// "offline" or "not found".
	ADBState string `json:"adb_state"`
	// Online reports that ADBState is "device".
	Online bool `json:"online"`
	// Reattaches counts the times the agent ran 'adb connect' again after
	// adb dropped the address and adb then knew the address again.
	Reattaches int `json:"reattaches"`
}

// stateLabel is the STATE column of 'device connections'.
func (s connectionState) stateLabel() string {
	switch {
	case !s.Attach:
		return "not attached"
	case !s.AttachDone:
		return "attaching"
	case s.AttachError != "":
		return "attach failed"
	default:
		return cmp.Or(s.ADBState, "unknown")
	}
}

// port is the local port of the agent's listener, or 0 when the address does
// not parse.
func (s connectionState) port() int {
	_, p, err := net.SplitHostPort(s.Address)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(p)
	return n
}

// errAgentExited stands in for the wait result of an agent that exited 0.
var errAgentExited = errors.New("the agent exited")

// connectionStore keeps the agents' state, lock and log files in one
// directory. An agent holds its lock file for its whole life, and the kernel
// releases the lock when the process dies, so a held lock is the liveness
// test. A recycled PID cannot pass it.
type connectionStore struct {
	dir string
}

// connectionPaths are the files of one device instance's agent.
type connectionPaths struct {
	state, lock, log string
}

// defaultConnectionStore opens the connections directory under the config
// directory.
func defaultConnectionStore() (connectionStore, error) {
	dir, err := config.Dir()
	if err != nil {
		return connectionStore{}, fmt.Errorf("locating the connections directory: %w", err)
	}
	return openConnectionStore(filepath.Join(dir, "connections"))
}

// openConnectionStore creates dir, readable by the owner only, if needed.
func openConnectionStore(dir string) (connectionStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return connectionStore{}, fmt.Errorf("creating %s: %w", dir, err)
	}
	return connectionStore{dir: dir}, nil
}

func (s connectionStore) paths(deviceID, instance string) connectionPaths {
	base := filepath.Join(s.dir, fileSafe(deviceID)+"--"+fileSafe(instance))
	return connectionPaths{state: base + ".json", lock: base + ".lock", log: base + ".log"}
}

// fileSafe maps every byte outside [A-Za-z0-9._-] to an underscore.
func fileSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		default:
			return '_'
		}
	}, s)
}

// write replaces the state file atomically, so a reader never sees half of
// it. CreateTemp makes the file with mode 0600.
func (s connectionStore) write(st connectionState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the connection state: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, ".state-*")
	if err != nil {
		return fmt.Errorf("writing the connection state: %w", err)
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("writing the connection state: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.paths(st.DeviceID, st.Instance).state); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("writing the connection state: %w", err)
	}
	return nil
}

func readConnectionState(path string) (connectionState, error) {
	var st connectionState
	data, err := os.ReadFile(path)
	if err != nil {
		return st, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("decoding %s: %w", path, err)
	}
	return st, nil
}

// lookup returns the live agent for one device instance, and false when none
// runs. A state file whose agent is gone is removed.
func (s connectionStore) lookup(deviceID, instance string) (connectionState, bool, error) {
	p := s.paths(deviceID, instance)
	if _, err := os.Stat(p.state); errors.Is(err, fs.ErrNotExist) {
		return connectionState{}, false, nil
	}
	if !s.reap(p) {
		return connectionState{}, false, nil
	}
	st, err := readConnectionState(p.state)
	if errors.Is(err, fs.ErrNotExist) {
		return connectionState{}, false, nil
	}
	if err != nil {
		return connectionState{}, false, err
	}
	if st.DeviceID != deviceID || st.Instance != instance {
		return connectionState{}, false, fmt.Errorf("%s holds device %s instance %s; expected device %s instance %s", p.state, st.DeviceID, st.Instance, deviceID, instance)
	}
	return st, true, nil
}

// list returns the live agents sorted by device and instance, removing the
// files of every agent that is gone. It never returns nil.
func (s connectionStore) list() ([]connectionState, error) {
	matches, err := filepath.Glob(filepath.Join(s.dir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", s.dir, err)
	}
	live := []connectionState{}
	var errs []error
	for _, path := range matches {
		base := strings.TrimSuffix(path, ".json")
		if !s.reap(connectionPaths{state: path, lock: base + ".lock", log: base + ".log"}) {
			continue
		}
		st, err := readConnectionState(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		live = append(live, st)
	}
	s.sweepLogs()
	slices.SortFunc(live, func(a, b connectionState) int {
		return cmp.Or(cmp.Compare(a.DeviceID, b.DeviceID), cmp.Compare(a.Instance, b.Instance))
	})
	return live, errors.Join(errs...)
}

const (
	reapAttempts = 3
	reapRetry    = 20 * time.Millisecond
)

// reap reports whether a live agent holds p's lock. When none does, it takes
// the lock itself before removing the dead agent's files, so a new agent
// cannot lock and write its state in between. Another reap or liveness check
// holds the lock only for an instant, so a held lock is retried briefly.
func (s connectionStore) reap(p connectionPaths) bool {
	for attempt := range reapAttempts {
		if attempt > 0 {
			time.Sleep(reapRetry)
		}
		f, err := filelock.TryLock(p.lock)
		if errors.Is(err, filelock.ErrHeld) {
			continue
		}
		if err != nil {
			output.Debug("locking %s: %v", p.lock, err)
			return false
		}
		// Without a state file, the log may belong to an agent that is still
		// starting.
		if _, err := os.Stat(p.state); err == nil {
			removeFiles(p.state, p.log)
		}
		filelock.Release(f, p.lock)
		return false
	}
	return true
}

// staleLogAge is how long a log with no state file is kept. Such a log is
// from an agent that failed to start, or from one that exited on Windows,
// which cannot delete its own open log. A starting agent's log is younger.
const staleLogAge = time.Hour

// sweepLogs removes the logs that no state file names and that are older
// than staleLogAge.
func (s connectionStore) sweepLogs() {
	logs, err := filepath.Glob(filepath.Join(s.dir, "*.log"))
	if err != nil {
		output.Debug("listing logs in %s: %v", s.dir, err)
		return
	}
	for _, path := range logs {
		if _, err := os.Stat(strings.TrimSuffix(path, ".log") + ".json"); !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) > staleLogAge {
			removeFiles(path)
		}
	}
}

// removeFiles removes each path. Missing files are fine.
func removeFiles(paths ...string) {
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			output.Debug("removing %s: %v", path, err)
		}
	}
}

// agentProcess is a started agent.
type agentProcess struct {
	pid int
	// exited delivers the result of waiting on the process.
	exited <-chan error
}

// agentProcesses starts and signals agent processes. Tests replace it so they
// never fork.
type agentProcesses interface {
	launch(ctx context.Context, args []string, log *os.File) (agentProcess, error)
	terminate(pid int) error
	kill(pid int) error
}

// adbRunner runs the local adb with its output sent to stdout and stderr.
type adbRunner func(ctx context.Context, stdout, stderr io.Writer, args ...string) error

// connectionManager starts, reuses and stops background ADB agents.
type connectionManager struct {
	store connectionStore
	procs agentProcesses
	adb   adbRunner

	readyTimeout time.Duration
	stopTimeout  time.Duration
	poll         time.Duration
}

// openConnectionManager builds the production manager. Tests swap it.
var openConnectionManager = func() (*connectionManager, error) {
	store, err := defaultConnectionStore()
	if err != nil {
		return nil, err
	}
	return &connectionManager{
		store:        store,
		procs:        osAgentProcesses{},
		adb:          runADB,
		readyTimeout: 30 * time.Second,
		stopTimeout:  5 * time.Second,
		poll:         100 * time.Millisecond,
	}, nil
}

// connectRequest names one instance to serve in the background.
type connectRequest struct {
	server string
	// profile is the config profile the agent loads its auth from, or ""
	// when none was selected.
	profile  string
	deviceID string
	instance string
	port     int
	attach   bool
	verbose  bool
	// waitOnline bounds the wait for adb to report the device online after
	// the attach. 0 skips the wait.
	waitOnline time.Duration
}

// connect returns the agent serving the instance, starting one when none
// runs. reused reports an agent that was already running. With attach a new
// agent runs 'adb connect' itself and waits for adb to report the device
// online, and connect returns once the agent's state file says how that
// ended. A reused agent is attached from here, because its state can be a
// supervision tick old. When the online wait times out, the agent keeps
// serving, since the device may still come up, and the error names the
// agent's log.
func (m *connectionManager) connect(ctx context.Context, req connectRequest) (connectionState, bool, error) {
	st, reused, err := m.store.lookup(req.deviceID, req.instance)
	if err != nil {
		return connectionState{}, false, err
	}
	if reused {
		if err := m.reuse(ctx, st, req); err != nil {
			return connectionState{}, false, err
		}
		return st, true, nil
	}

	if st, err = m.start(ctx, req); err != nil {
		return connectionState{}, false, err
	}
	if !req.attach {
		return st, false, nil
	}
	if st.AttachError != "" {
		err := errors.New(st.AttachError)
		if st.AttachExit != 0 {
			err = &exitError{err: fmt.Errorf("device %s instance %s: %w", req.deviceID, req.instance, err), code: st.AttachExit}
		}
		return connectionState{}, false, errors.Join(err, m.stop(ctx, st))
	}
	// An agent that has not finished attaching by the parent's deadline is
	// reported like one whose online wait ran out.
	if !st.Online && (req.waitOnline > 0 || !st.AttachDone) {
		// The agent owns the sessions, so the last failed session of a wait
		// that ran out shows only in its log.
		return connectionState{}, false, agentKeepsServing(&adbOfflineError{
			address: st.Address, limit: req.waitOnline, state: cmp.Or(st.ADBState, "no answer"),
		}, st)
	}
	return st, false, nil
}

// reuse checks that a running agent fits the request and, with attach, points
// the local adb at it and waits for the device online.
func (m *connectionManager) reuse(ctx context.Context, st connectionState, req connectRequest) error {
	if st.Server != req.server {
		return fmt.Errorf("instance %s of device %s is already connected through %s; run 'cfctl device disconnect %s' first",
			req.instance, req.deviceID, st.Server, req.deviceID)
	}
	if req.port != 0 && st.port() != req.port {
		return fmt.Errorf("instance %s of device %s is already connected on %s; run 'cfctl device disconnect %s' to move it to port %d",
			req.instance, req.deviceID, st.Address, req.deviceID, req.port)
	}
	if !req.attach {
		return nil
	}
	// adb connect is idempotent, and a restarted adb server has forgotten the
	// address of a reused agent.
	if err := m.adb(ctx, os.Stderr, os.Stderr, "connect", st.Address); err != nil {
		return err
	}
	if err := waitADBOnline(ctx, m.adb, st.Address, req.waitOnline, nil); err != nil {
		return agentKeepsServing(err, st)
	}
	return nil
}

// agentKeepsServing adds the agent's log and the stop command to a failed
// online wait.
func agentKeepsServing(err error, st connectionState) error {
	return fmt.Errorf("%w; the agent (pid %d) keeps serving, see its log %s or stop it with 'cfctl device disconnect %s'",
		err, st.PID, st.LogFile, st.DeviceID)
}

// agentArgs is the command line of the hidden agent. Global flags are explicit
// so an inherited CFCTL_TIMEOUT, CFCTL_SERVER or CFCTL_PROFILE, or a later
// 'config use', cannot change the agent.
func agentArgs(req connectRequest) []string {
	args := []string{"--server", req.server, "--timeout", "0", "--no-json"}
	if req.profile != "" {
		args = append(args, "--profile", req.profile)
	}
	if req.verbose {
		args = append(args, "--verbose")
	}
	args = append(args, "device", "adb-agent", req.deviceID,
		"--instance", req.instance, "--port", strconv.Itoa(req.port))
	if req.attach {
		args = append(args, "--attach", "--wait-online", req.waitOnline.String())
	}
	return args
}

// start launches an agent and waits until its state file says it listens
// and, with attach, until it says the first attach has ended.
func (m *connectionManager) start(ctx context.Context, req connectRequest) (connectionState, error) {
	p := m.store.paths(req.deviceID, req.instance)
	// O_APPEND keeps the agent's writes at the end of the log after the agent
	// truncates it.
	logFile, err := os.OpenFile(p.log, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_APPEND, 0o600)
	if err != nil {
		return connectionState{}, fmt.Errorf("creating the agent log: %w", err)
	}
	// The child holds its own descriptor once launched.
	defer logFile.Close()

	child, err := m.procs.launch(ctx, agentArgs(req), logFile)
	if err != nil {
		return connectionState{}, fmt.Errorf("starting the ADB agent: %w", err)
	}
	return m.awaitReady(ctx, req, child, p.log)
}

// awaitReady polls the agent's state file until the agent is ready, the agent
// exits, the deadline passes or ctx ends. Without attach the agent is ready
// once it listens. With attach it is ready once its first attach has ended,
// and the deadline grows by the online wait. An agent that listens but is not
// ready by the deadline is returned as it stands and keeps serving.
func (m *connectionManager) awaitReady(ctx context.Context, req connectRequest, child agentProcess, logPath string) (connectionState, error) {
	limit := m.readyTimeout
	if req.attach {
		limit += req.waitOnline
	}
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(m.poll)
	defer tick.Stop()

	var listening *connectionState
	for {
		st, ok, err := m.store.lookup(req.deviceID, req.instance)
		if err != nil {
			return connectionState{}, err
		}
		if ok && st.PID == child.pid {
			if !req.attach || st.AttachDone {
				return st, nil
			}
			listening = &st
		}
		select {
		case err := <-child.exited:
			stage := "before it was listening"
			if listening != nil {
				stage = "while it attached adb"
			}
			return connectionState{}, fmt.Errorf("the ADB agent for device %s instance %s exited %s%s; see %s: %w",
				req.deviceID, req.instance, stage, lastLogLine(logPath), logPath, cmp.Or(err, errAgentExited))
		case <-deadline.C:
			if listening != nil {
				return *listening, nil
			}
			_ = m.procs.kill(child.pid)
			return connectionState{}, fmt.Errorf("the ADB agent for device %s instance %s was not listening after %s; see %s",
				req.deviceID, req.instance, limit, logPath)
		case <-ctx.Done():
			if listening == nil {
				_ = m.procs.kill(child.pid)
			}
			return connectionState{}, fmt.Errorf("waiting for the ADB agent: %w", ctx.Err())
		case <-tick.C:
		}
	}
}

// lastLogLine returns ": " plus the last non-empty line of the agent log, or
// "" when there is none.
func lastLogLine(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	last := strings.TrimSpace(string(lines[len(lines)-1]))
	if last == "" {
		return ""
	}
	return ": " + last
}

// stop asks the agent to exit, kills it if it does not, detaches adb from its
// address and removes its files.
func (m *connectionManager) stop(ctx context.Context, st connectionState) error {
	p := m.store.paths(st.DeviceID, st.Instance)
	if err := m.procs.terminate(st.PID); err != nil {
		output.Debug("signaling ADB agent %d: %v", st.PID, err)
	}
	if !m.awaitReleased(ctx, p.lock) {
		if err := m.procs.kill(st.PID); err != nil {
			output.Debug("killing ADB agent %d: %v", st.PID, err)
		}
		if !m.awaitReleased(ctx, p.lock) {
			return fmt.Errorf("the ADB agent for device %s instance %s (pid %d) did not stop", st.DeviceID, st.Instance, st.PID)
		}
	}
	// The agent detaches adb itself on a clean exit. A killed one cannot.
	if err := m.adb(context.WithoutCancel(ctx), io.Discard, io.Discard, "disconnect", st.Address); err != nil {
		output.Debug("adb disconnect %s: %v", st.Address, err)
	}
	m.store.reap(p)
	return nil
}

// awaitReleased polls until the lock is free, for at most the stop timeout.
func (m *connectionManager) awaitReleased(ctx context.Context, lock string) bool {
	deadline := time.Now().Add(m.stopTimeout)
	for filelock.Held(lock) {
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(m.poll)
	}
	return true
}

// connectionFilter selects the agents 'device disconnect' stops.
type connectionFilter struct {
	all bool
	// device is a device ID or the name a connection record stores.
	device   string
	instance string
}

// matches reports whether the filter selects st once device is resolved to
// deviceID.
func (f connectionFilter) matches(st connectionState, deviceID string) bool {
	if f.all {
		return true
	}
	return st.DeviceID == deviceID && (f.instance == "" || st.Instance == f.instance)
}

// ambiguousConnectionError reports a name that the connection records give to
// more than one device.
type ambiguousConnectionError struct {
	name string
	// matches holds every record with the name.
	matches []connectionState
}

func (e *ambiguousConnectionError) Error() string {
	return fmt.Sprintf("background connections to %d devices are named %s; name one by its device ID",
		len(e.deviceIDs()), e.name)
}

// ExitCode implements kong.ExitCoder.
func (e *ambiguousConnectionError) ExitCode() int { return ExitConflict }

func (e *ambiguousConnectionError) deviceIDs() []string {
	ids := []string{}
	for _, st := range e.matches {
		if !slices.Contains(ids, st.DeviceID) {
			ids = append(ids, st.DeviceID)
		}
	}
	return ids
}

// resolveConnectedDevice maps a device ID or name to the device ID of the
// live records, without calling the server. An ID that a record carries wins.
// A name no record carries is returned as it is, so it matches nothing.
func resolveConnectedDevice(live []connectionState, device string) (string, error) {
	var named []connectionState
	for _, st := range live {
		if st.DeviceID == device {
			return device, nil
		}
		if st.DeviceName == device {
			named = append(named, st)
		}
	}
	e := &ambiguousConnectionError{name: device, matches: named}
	switch ids := e.deviceIDs(); len(ids) {
	case 0:
		return device, nil
	case 1:
		return ids[0], nil
	default:
		return "", e
	}
}

// disconnect stops every live agent the filter matches and returns the ones
// it stopped. It never returns nil.
func (m *connectionManager) disconnect(ctx context.Context, f connectionFilter) ([]connectionState, error) {
	live, err := m.store.list()
	errs := []error{err}
	stopped := []connectionState{}
	var deviceID string
	if !f.all {
		if deviceID, err = resolveConnectedDevice(live, f.device); err != nil {
			return stopped, errors.Join(append(errs, err)...)
		}
	}
	for _, st := range live {
		if !f.matches(st, deviceID) {
			continue
		}
		if err := m.stop(ctx, st); err != nil {
			errs = append(errs, err)
			continue
		}
		stopped = append(stopped, st)
	}
	return stopped, errors.Join(errs...)
}

// osAgentProcesses re-executes this binary as a detached agent.
type osAgentProcesses struct{}

func (osAgentProcesses) launch(ctx context.Context, args []string, log *os.File) (agentProcess, error) {
	exe, err := os.Executable()
	if err != nil {
		return agentProcess{}, fmt.Errorf("locating the cfctl binary: %w", err)
	}
	// The agent outlives this command, so its context never cancels.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), exe, args...)
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = detachedProcAttr()
	if err := cmd.Start(); err != nil {
		return agentProcess{}, fmt.Errorf("running %s: %w", exe, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	return agentProcess{pid: cmd.Process.Pid, exited: exited}, nil
}

func (osAgentProcesses) terminate(pid int) error {
	return terminateProcess(pid)
}

func (osAgentProcesses) kill(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("finding process %d: %w", pid, err)
	}
	if err := p.Kill(); err != nil {
		return fmt.Errorf("killing process %d: %w", pid, err)
	}
	return nil
}
