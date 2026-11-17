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
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// DeviceConnectCmd implements 'cfctl device connect'.
type DeviceConnectCmd struct {
	ID           string        `arg:"" required:"" placeholder:"DEVICE" help:"Device ID or name"`
	Port         int           `default:"0" help:"Local port to listen on (0 picks a free one)"`
	NoAt         bool          `name:"no-attach" help:"Check that a device stream opens, then print the address instead of running 'adb connect'"`
	Background   bool          `help:"Run the tunnel in a background agent and return once adb reports the device online; stop it with 'cfctl device disconnect'"`
	AllInstances bool          `name:"all-instances" help:"With --background, connect every instance of the device"`
	WaitOnline   time.Duration `name:"wait-online" default:"60s" help:"After 'adb connect', wait this long for adb to report the device online (0 skips the wait). Separate from --timeout"`
	InstanceFlag `embed:""`
	Command      []string `arg:"" optional:"" passthrough:"" placeholder:"ADB_ARGS" help:"Run this adb command once the device is online and exit with adb's exit code, which can collide with cfctl's 3, 4 and 5 (write it after --, e.g. -- shell getprop ro.build.id)"`
}

// Run bridges the device's ADB stream to a local port and points adb at it.
// The host's ADB port is reachable only from inside the VPC, so the stream is
// tunneled through the orchestrator. Without --background the command stays
// in the foreground for the life of the tunnel. cfctl's messages and adb's
// own output go to stderr, so stdout carries only the passthrough command's
// output.
func (c *DeviceConnectCmd) Run(ctx context.Context, cli *CLI, client *cco.Client) error {
	c.Command = passthroughCommand(c.Command)
	if err := c.validate(); err != nil {
		return err
	}
	// Background agents stay keyed by the device ID.
	if err := resolveDeviceArg(ctx, client, &c.ID); err != nil {
		return err
	}
	if c.Background {
		return c.runBackground(ctx, cli, client)
	}

	// Resolve first so a bad ID fails before the stream upgrade.
	device, err := client.GetDevice(ctx, c.ID)
	if err != nil {
		return fmt.Errorf("failed to get device: %w", err)
	}
	instance, err := c.resolveOn(device)
	if err != nil {
		return err
	}

	if c.NoAt {
		// Nothing dials until the user's adb connects, so an auth or routing
		// failure would otherwise surface only there.
		if err := probeADBStream(ctx, client, device.ID, instance); err != nil {
			return err
		}
	}

	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Port)))
	if err != nil {
		return fmt.Errorf("listening on port %d: %w", c.Port, err)
	}
	defer listener.Close()

	target := listener.Addr().String()
	output.Info("Tunneling ADB for %s (instance %s) via %s", device.ID, instance, cli.Server)

	// The tunnel serves before adb connects. adb sends its handshake at once
	// and gives up after 10s when nothing answers.
	sessions := newOnlineSessions()
	tctx, stop := context.WithCancel(ctx)
	defer stop()
	served := make(chan error, 1)
	go func() { served <- serveADBTunnel(tctx, listener, client, device.ID, instance, sessions) }()
	stopTunnel := func() error {
		stop()
		return <-served
	}

	if c.NoAt {
		output.Info("Listening on %s. Run: adb connect %s", target, target)
		return <-served
	}

	if err := localADB(ctx, os.Stderr, os.Stderr, "connect", target); err != nil {
		return errors.Join(err, stopTunnel())
	}
	defer disconnectADB(ctx, target)
	if err := waitADBOnline(ctx, localADB, target, c.WaitOnline, sessions); err != nil {
		return errors.Join(offlineExit(ctx, client.GetDevice, device.ID, instance, err), stopTunnel())
	}

	if len(c.Command) > 0 {
		// The tunnel serves only while the passthrough command runs.
		runErr := localADB(ctx, os.Stdout, os.Stderr, append([]string{"-s", target}, c.Command...)...)
		return errors.Join(runErr, stopTunnel())
	}
	output.Info("Connected as %s. Leave this running; press Ctrl+C to disconnect.", target)
	return <-served
}

// probeADBStream opens one device stream and closes it, so a rejected or
// unroutable stream fails the command before the address is printed.
func probeADBStream(ctx context.Context, client *cco.Client, deviceID, instance string) error {
	stream, err := client.DialADB(ctx, deviceID, instance)
	if err != nil {
		return streamDialFailed(deviceID, err)
	}
	if err := stream.Close(); err != nil {
		output.Debug("closing the probe stream: %v", err)
	}
	return nil
}

// validate checks the flag combinations before anything is dialed.
func (c *DeviceConnectCmd) validate() error {
	switch {
	case c.NoAt && len(c.Command) > 0:
		return errors.New("--no-attach hands the address to your own adb, and a passthrough command needs the attach. Drop one of them")
	case c.Background && len(c.Command) > 0:
		return errors.New("--background returns once adb is attached, so it cannot run a passthrough command. Run 'adb -s ADDRESS' with the printed address instead")
	case c.AllInstances && !c.Background:
		return errors.New("--all-instances needs --background, because a foreground tunnel serves one instance")
	case c.AllInstances && c.Instance != "":
		return errors.New("--instance and --all-instances are mutually exclusive")
	}
	return nil
}

// tunnelSessions paces a tunnel's device streams and hears how each one
// ended. serveADBTunnel calls it from several goroutines at once.
type tunnelSessions interface {
	// beforeDial returns once a new device stream may be dialed.
	beforeDial(ctx context.Context)
	// ended receives the result of one session, nil for a clean end. The
	// local connection stays open until it returns.
	ended(ctx context.Context, err error)
}

// foregroundSessions dials at once and logs failed sessions at debug level.
type foregroundSessions struct{}

func (foregroundSessions) beforeDial(context.Context) {}

func (foregroundSessions) ended(_ context.Context, err error) {
	if err != nil {
		output.Debug("adb tunnel session ended: %v", err)
	}
}

// disconnectADB removes the tunnel from the local adb server so no stale
// offline entry remains. It strips cancellation because the command context is
// already canceled when the deferred call runs.
func disconnectADB(ctx context.Context, target string) {
	if err := localADB(context.WithoutCancel(ctx), os.Stderr, os.Stderr, "disconnect", target); err != nil {
		output.Debug("adb disconnect %s: %v", target, err)
	}
}

// serveADBTunnel accepts local connections until ctx ends and opens one device
// stream per connection. The orchestrator's request timeout eventually closes
// each stream, and adb then redials on a new connection with a fresh stream.
func serveADBTunnel(ctx context.Context, listener net.Listener, client *cco.Client, deviceID, instance string, sessions tunnelSessions) error {
	// Unblock Accept when the user interrupts.
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		local, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				output.Info("Disconnecting.")
				return nil
			}
			return fmt.Errorf("accepting a local connection: %w", err)
		}

		wg.Go(func() {
			defer local.Close()
			sessions.beforeDial(ctx)
			if ctx.Err() != nil {
				return
			}
			sessions.ended(ctx, bridgeADB(ctx, client, deviceID, instance, local))
		})
	}
}

// errStreamDial marks a session that ended because its device stream did not
// open.
var errStreamDial = errors.New("opening a device stream")

// streamDialFailed wraps a failed device stream dial in errStreamDial.
func streamDialFailed(deviceID string, err error) error {
	return fmt.Errorf("%w for %s: %w", errStreamDial, deviceID, err)
}

// errStreamSilent marks a session whose device stream opened but ended before
// the device sent a byte. The orchestrator can accept the stream and close it
// at once when the guest's adbd does not answer.
var errStreamSilent = errors.New("the device stream ended before the device sent any data")

// errRemoteEnded marks a session that carried data and ended because the
// device stream closed, as at the orchestrator's request timeout or in an
// outage. A session the local adb closed ends with nil.
var errRemoteEnded = errors.New("the device stream closed")

// copyResult is how one direction of a bridged session ended.
type copyResult struct {
	fromRemote bool
	err        error
}

// countingWriter counts the bytes written through it.
type countingWriter struct {
	w io.Writer
	n atomic.Int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

// bridgeADB copies bytes between one local connection and one device stream
// until either end goes away.
func bridgeADB(ctx context.Context, client *cco.Client, deviceID, instance string, local net.Conn) error {
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	remote, err := client.DialADB(ctx, deviceID, instance)
	if err != nil {
		return streamDialFailed(deviceID, err)
	}
	defer remote.Close()
	toLocal := &countingWriter{w: local}

	// Whichever direction ends first cancels the other. A half-closed tunnel
	// leaves adb waiting on a device that cannot answer.
	errs := make(chan copyResult, 2)
	go func() {
		_, err := io.Copy(remote, local)
		cancel()
		errs <- copyResult{err: err}
	}()
	go func() {
		_, err := io.Copy(toLocal, remote)
		cancel()
		// Report before closing local: the close ends the other copy, and its
		// result must not arrive first and read as a local close.
		// Report before closing local: the close ends the other copy, and its
		// result must not arrive first and read as a local close.
		errs <- copyResult{fromRemote: true, err: err}
		// Unblock the other copy, which is parked in a socket read that no
		// context cancellation reaches.
		_ = local.Close()
	}()

	first := <-errs
	if toLocal.n.Load() == 0 && parent.Err() == nil {
		return fmt.Errorf("%w for %s", errStreamSilent, deviceID)
	}
	if first.err != nil && !errors.Is(first.err, io.EOF) && ctx.Err() == nil {
		return fmt.Errorf("relaying ADB for %s: %w", deviceID, first.err)
	}
	if first.fromRemote && parent.Err() == nil {
		return fmt.Errorf("%w for %s", errRemoteEnded, deviceID)
	}
	return nil
}

// localADB runs the local adb for the foreground tunnel. Tests replace it so
// they never start adb.
var localADB adbRunner = runADB

// runADB runs the local adb binary with its output sent to stdout and stderr,
// and reports a missing binary with an install hint.
func runADB(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "adb", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("adb is not on PATH; install platform-tools or pass --no-attach: %w", err)
		}
		return fmt.Errorf("adb %s failed: %w", args[0], err)
	}
	return nil
}
