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
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/filelock"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// runBackground starts or reuses one agent per selected instance and returns
// once each is listening and attached.
func (c *DeviceConnectCmd) runBackground(ctx context.Context, cli *CLI, client *cco.Client) error {
	device, err := client.GetDevice(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("failed to get device: %w", err)
	}
	var instances []string
	if c.AllInstances {
		if instances = instanceNames(device); len(instances) == 0 {
			return fmt.Errorf("device %s has no instances yet; it may still be creating", device.ID)
		}
		if c.Port != 0 && len(instances) > 1 {
			return fmt.Errorf("--port names one port, and device %s has %d instances. Drop --port", device.ID, len(instances))
		}
	} else {
		instance, err := c.resolveOn(device)
		if err != nil {
			return err
		}
		instances = []string{instance}
	}

	states, err := startBackgroundConnections(ctx, cli, client, device.ID, instances, backgroundOptions{
		port: c.Port, attach: !c.NoAt, waitOnline: c.WaitOnline,
	})
	if err != nil {
		return err
	}
	if cli.JSON {
		if c.AllInstances {
			return printJSON(states)
		}
		return printJSON(states[0])
	}
	printBackgroundConnections(states, !c.NoAt, output.Info)
	cli.Hint("List agents with 'cfctl device connections'; stop them with 'cfctl device disconnect %s'", device.ID)
	return nil
}

// instanceNames lists a device's instance names in the device's order.
func instanceNames(d *cco.Device) []string {
	names := make([]string, len(d.Instances))
	for i, inst := range d.Instances {
		names[i] = inst.Name
	}
	return names
}

// backgroundOptions are the per-instance settings of a background connection.
type backgroundOptions struct {
	port   int
	attach bool
	// waitOnline bounds the wait for adb to report each instance online.
	waitOnline time.Duration
}

// startBackgroundConnections connects each instance in turn. Agents started
// before a failure keep running and stay listed by 'device connections'.
func startBackgroundConnections(ctx context.Context, cli *CLI, client *cco.Client, deviceID string, instances []string, opts backgroundOptions) ([]connectionState, error) {
	m, err := openConnectionManager()
	if err != nil {
		return nil, err
	}
	states := make([]connectionState, 0, len(instances))
	for _, instance := range instances {
		st, reused, err := m.connect(ctx, connectRequest{
			server: cli.Server, profile: cli.profile, deviceID: deviceID, instance: instance,
			port: opts.port, attach: opts.attach, verbose: cli.Verbose, waitOnline: opts.waitOnline,
		})
		if _, offline := errors.AsType[*adbOfflineError](err); offline {
			return states, offlineExit(ctx, client.GetDevice, deviceID, instance, err)
		}
		if err != nil {
			return states, err
		}
		if reused {
			output.Info("Reusing the ADB agent already serving device %s instance %s (pid %d)", deviceID, instance, st.PID)
		}
		states = append(states, st)
	}
	return states, nil
}

// printBackgroundConnections prints one line per connection through emit.
func printBackgroundConnections(states []connectionState, attached bool, emit func(format string, args ...any)) {
	for _, st := range states {
		if attached {
			emit("Connected as %s (device %s, instance %s)", st.Address, st.DeviceID, st.Instance)
		} else {
			emit("Listening on %s (device %s, instance %s). Run: adb connect %s", st.Address, st.DeviceID, st.Instance, st.Address)
		}
	}
}

// DeviceADBAgentCmd is the background agent behind 'device connect
// --background'. It is hidden because only cfctl starts it.
type DeviceADBAgentCmd struct {
	ID         string        `arg:"" required:"" placeholder:"DEVICE_ID" help:"Device ID"`
	Instance   string        `required:"" placeholder:"NAME" help:"Instance name"`
	Port       int           `default:"0" help:"Local port to listen on (0 picks a free one)"`
	Attach     bool          `help:"Run 'adb connect' and keep adb attached"`
	WaitOnline time.Duration `name:"wait-online" default:"0s" help:"With --attach, wait this long for adb to report the device online"`
}

// Run serves the tunnel until SIGTERM or until the device is gone, then
// detaches adb and removes the agent's state and log. With --attach it also
// keeps the local adb attached for as long as it serves.
func (c *DeviceADBAgentCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	m, err := openConnectionManager()
	if err != nil {
		return err
	}
	p := m.store.paths(c.ID, c.Instance)
	lock, err := filelock.Acquire(p.lock)
	if err != nil {
		return fmt.Errorf("locking %s: %w", p.lock, err)
	}
	defer filelock.Release(lock, p.lock)

	device, err := client.GetDevice(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("failed to get device: %w", err)
	}

	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Port)))
	if err != nil {
		return fmt.Errorf("listening on port %d: %w", c.Port, err)
	}
	defer listener.Close()

	st := connectionState{
		PID: os.Getpid(), DeviceID: c.ID, DeviceName: device.Name, Instance: c.Instance,
		Address: listener.Addr().String(), Server: cli.Server,
		StartedAt: time.Now().UTC(), LogFile: p.log, Attach: c.Attach,
	}
	// The state file is the readiness signal the parent polls for, so it is
	// written only once the listener is bound.
	if err := m.store.write(st); err != nil {
		return err
	}
	// The files go while the lock is still held, so they never belong to a
	// newer agent.
	defer removeFiles(p.state, p.log)
	defer func() {
		if err := m.adb(context.WithoutCancel(ctx), io.Discard, io.Discard, "disconnect", st.Address); err != nil {
			output.Debug("adb disconnect %s: %v", st.Address, err)
		}
	}()
	output.Info("%s: agent %d serving ADB for device %s instance %s on %s via %s",
		st.StartedAt.Format(time.RFC3339), st.PID, c.ID, c.Instance, st.Address, cli.Server)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	recheck := make(chan struct{}, 1)
	sessions := &agentSessions{
		deviceID: c.ID, instance: c.Instance, getDevice: client.GetDevice,
		clock: wallClock{}, stop: cancel,
		log: &agentLog{
			clock: wallClock{}, path: p.log, limit: agentLogLimit,
			emit: func(line string) { output.Info("%s", line) },
		},
		remoteEnded: func() {
			select {
			case recheck <- struct{}{}:
			default:
			}
		},
	}
	// The supervisor stops before the deferred calls remove the state file,
	// so it cannot write the file again.
	var supervisor sync.WaitGroup
	if c.Attach {
		sessions.watch = newOnlineSessions()
		attachment := &agentAttachment{
			adb: m.adb, adbLog: os.Stderr, waitOnline: c.WaitOnline, every: agentSuperviseEvery,
			sessions: sessions, write: m.store.write, recheck: recheck,
		}
		supervisor.Go(func() { attachment.run(ctx, st) })
	}
	err = serveADBTunnel(ctx, listener, client, c.ID, c.Instance, sessions)
	cancel()
	supervisor.Wait()
	return err
}

// DeviceDisconnectCmd implements 'cfctl device disconnect'.
type DeviceDisconnectCmd struct {
	ID       string `arg:"" optional:"" placeholder:"DEVICE" help:"ID or name of the device whose background connections to stop; the name is read from the local connection records"`
	Instance string `placeholder:"NAME" help:"Stop only this instance's connection"`
	All      bool   `help:"Stop every background connection"`
}

// Run stops the matching background agents. It never calls the server, so a
// device name resolves only against the local connection records.
func (c *DeviceDisconnectCmd) Run(ctx context.Context, cli *CLI) error {
	switch {
	case c.All && (c.ID != "" || c.Instance != ""):
		return errors.New("--all stops every connection; drop DEVICE and --instance, or drop --all")
	case !c.All && c.ID == "":
		return errors.New("name a device by ID or name, or pass --all")
	}
	m, err := openConnectionManager()
	if err != nil {
		return err
	}
	stopped, err := m.disconnect(ctx, connectionFilter{all: c.All, device: c.ID, instance: c.Instance})
	if ambiguous, ok := errors.AsType[*ambiguousConnectionError](err); ok {
		if perr := printConnectionMatches(os.Stderr, ambiguous.matches); perr != nil {
			err = errors.Join(err, perr)
		}
	}
	if cli.JSON {
		return errors.Join(printJSON(stopped), err)
	}
	if len(stopped) == 0 && err == nil {
		output.Success("No background ADB connection matches; nothing to stop.")
		return nil
	}
	for _, st := range stopped {
		output.Success("Disconnected %s (device %s, instance %s)", st.Address, st.DeviceID, st.Instance)
	}
	return err
}

// DeviceConnectionsCmd implements 'cfctl device connections'.
type DeviceConnectionsCmd struct{}

// Run lists the live background agents, pruning the state of dead ones.
func (c *DeviceConnectionsCmd) Run(cli *CLI) error {
	m, err := openConnectionManager()
	if err != nil {
		return err
	}
	live, err := m.store.list()
	if cli.JSON {
		return errors.Join(printJSON(live), err)
	}
	return errors.Join(printConnections(live), err)
}
