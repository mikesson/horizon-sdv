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
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"time"
)

const (
	// agentSuperviseEvery is how often an attaching agent asks adb whether it
	// still knows the agent's address.
	agentSuperviseEvery = 15 * time.Second

	// adbStateNotFound is the state of an address adb has dropped. adb drops
	// a TCP transport after about a minute of failed reconnects, and never
	// tries it again.
	adbStateNotFound = "not found"
)

// agentAttachment keeps the local adb attached to a background agent's
// address. It runs 'adb connect' once, waits for the device to come online,
// and from then on checks adb's state every tick and after each session the
// device stream closed. When adb has dropped the address and the device still
// exists, it runs 'adb connect' again. It is the only writer of the agent's
// state file once the agent has written its first state.
type agentAttachment struct {
	adb adbRunner
	// adbLog receives the output of the first 'adb connect'.
	adbLog     io.Writer
	waitOnline time.Duration
	every      time.Duration
	sessions   *agentSessions
	// write replaces the agent's state file.
	write func(connectionState) error
	// recheck delivers a value after a session the device stream closed.
	recheck <-chan struct{}
}

// run attaches and then supervises until ctx ends. st is the state the agent
// has already written.
func (a *agentAttachment) run(ctx context.Context, st connectionState) {
	st = a.attach(ctx, st)
	if ctx.Err() != nil {
		return
	}
	st.AttachDone = true
	a.save(st)
	if st.AttachError != "" {
		// The parent stops the agent.
		return
	}
	tick := time.NewTicker(a.every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-a.recheck:
		}
		if next := a.check(ctx, st); next != st {
			st = next
			a.save(st)
		}
	}
}

// attach runs the first 'adb connect' and the wait for the device to come
// online.
func (a *agentAttachment) attach(ctx context.Context, st connectionState) connectionState {
	if err := a.adb(ctx, a.adbLog, a.adbLog, "connect", st.Address); err != nil {
		st.AttachError = err.Error()
		return st
	}
	err := waitADBOnline(ctx, a.adb, st.Address, a.waitOnline, a.sessions.watch)
	if offline, ok := errors.AsType[*adbOfflineError](err); ok {
		st.ADBState = adbStateLabel(offline.state)
		return st
	}
	if errors.Is(err, errStreamDial) {
		st.AttachError = err.Error()
		st.AttachExit = exitCodeFor(err, false)
		return st
	}
	if errors.Is(err, exec.ErrNotFound) {
		st.AttachError = err.Error()
		return st
	}
	if err != nil {
		return st
	}
	if a.waitOnline > 0 {
		st.ADBState = adbOnline
	} else {
		answer, _ := adbState(ctx, a.adb, st.Address)
		st.ADBState = adbStateLabel(answer)
	}
	st.Online = st.ADBState == adbOnline
	return st
}

// check asks adb for the address's state and connects again when adb has
// dropped it and the device still exists. It stops the agent when the device
// is gone. It returns the updated state.
func (a *agentAttachment) check(ctx context.Context, st connectionState) connectionState {
	answer, err := adbState(ctx, a.adb, st.Address)
	if ctx.Err() != nil {
		return st
	}
	if errors.Is(err, exec.ErrNotFound) {
		a.sessions.log.printf("Checking adb: %v", err)
		return st
	}
	state := adbStateLabel(answer)
	if state == adbStateNotFound {
		if a.sessions.deviceGone(ctx) {
			a.sessions.gone()
			return st
		}
		state = a.reconnect(ctx, st.Address)
		// adb connect exits 0 when no handshake arrived, and then still does
		// not know the address.
		if state != adbStateNotFound {
			st.Reattaches++
		}
	}
	st.ADBState = state
	st.Online = state == adbOnline
	return st
}

// reconnect runs 'adb connect' for address and returns adb's state for it
// afterwards.
func (a *agentAttachment) reconnect(ctx context.Context, address string) string {
	a.sessions.log.printf("adb dropped %s; running adb connect again", address)
	// The output goes through the agent's log, which counts repeats, since an
	// outage repeats it on every tick.
	var out bytes.Buffer
	err := a.adb(ctx, &out, &out, "connect", address)
	if msg := strings.TrimSpace(out.String()); msg != "" {
		a.sessions.log.printf("adb connect %s: %s", address, msg)
	}
	if err != nil {
		a.sessions.log.printf("adb connect %s: %v", address, err)
		return adbStateNotFound
	}
	answer, _ := adbState(ctx, a.adb, address)
	return adbStateLabel(answer)
}

func (a *agentAttachment) save(st connectionState) {
	if err := a.write(st); err != nil {
		a.sessions.log.printf("Writing the agent state: %v", err)
	}
}

// adbStateLabel shortens adb's answer for an address it does not know, such
// as "error: device '127.0.0.1:41001' not found", to adbStateNotFound.
func adbStateLabel(answer string) string {
	if strings.Contains(answer, adbStateNotFound) {
		return adbStateNotFound
	}
	return answer
}
