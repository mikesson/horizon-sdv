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
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// printJSON is the single wrap point for JSON command results.
func printJSON(v any) error {
	if err := output.PrintJSON(v); err != nil {
		return fmt.Errorf("encoding JSON: %w", err)
	}
	return nil
}

// printActionResult emits the machine-readable result object for a
// state-changing command run with --json.
func printActionResult(resource, id, action string) error {
	if err := output.PrintActionResult(resource, id, action); err != nil {
		return fmt.Errorf("printing result: %w", err)
	}
	return nil
}

// confirm prints a [y/N] prompt to stderr and reports whether the user
// answered yes. EOF before any input is an error, so a script without a
// terminal cannot read a silent cancel as success.
func confirm(format string, args ...any) (bool, error) {
	fmt.Fprintf(os.Stderr, format+" [y/N] ", args...)
	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && answer == "" {
		fmt.Fprintln(os.Stderr)
		return false, errors.New("cannot prompt for confirmation (stdin is not interactive); pass --force")
	}
	answer = strings.TrimSpace(answer)
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes"), nil
}

// table renders rows to stdout with aligned columns.
func table(header string, rows func(w *tabwriter.Writer)) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, header)
	rows(w)
	if err := w.Flush(); err != nil {
		return fmt.Errorf("rendering table: %w", err)
	}
	return nil
}

func printHosts(hosts []cco.Host) error {
	if len(hosts) == 0 {
		fmt.Println("No hosts found.")
		return nil
	}
	return table("ID\tNAME\tZONE\tSTATUS\tIP ADDRESS", func(w *tabwriter.Writer) {
		for _, h := range hosts {
			ip := h.IPAddress
			if ip == "" {
				ip = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", h.ID, h.Name, h.Zone, h.Status, ip)
		}
	})
}

func printHostCVDs(cvds []cco.HostCVD) error {
	if len(cvds) == 0 {
		fmt.Println("No CVDs found.")
		return nil
	}
	return table("GROUP\tNAME\tSTATUS\tADB\tDEVICE", func(w *tabwriter.Writer) {
		for _, c := range cvds {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				c.Group, c.Name, c.Status, cmp.Or(c.ADBSerial, "-"), cmp.Or(c.DeviceID, "-"))
		}
	})
}

func printConnections(conns []connectionState) error {
	if len(conns) == 0 {
		fmt.Println("No background ADB connections.")
		return nil
	}
	return table("DEVICE\tNAME\tINSTANCE\tADDRESS\tSTATE\tREATTACHES\tPID\tSINCE", func(w *tabwriter.Writer) {
		for _, c := range conns {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%d\t%s\n",
				c.DeviceID, cmp.Or(c.DeviceName, "-"), c.Instance, c.Address, c.stateLabel(), c.Reattaches,
				c.PID, c.StartedAt.Local().Format(time.DateTime))
		}
	})
}

// printConnectionMatches writes the connection records an ambiguous device
// name matched to w.
func printConnectionMatches(w io.Writer, conns []connectionState) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "DEVICE\tNAME\tINSTANCE\tADDRESS\tSINCE")
	for _, c := range conns {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			c.DeviceID, c.DeviceName, c.Instance, c.Address, c.StartedAt.Local().Format(time.DateTime))
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("rendering table: %w", err)
	}
	return nil
}

func printHost(host *cco.Host) {
	fmt.Printf("ID:           %s\n", host.ID)
	fmt.Printf("Name:         %s\n", host.Name)
	if host.GCEInstanceName != "" {
		fmt.Printf("GCE Instance: %s\n", host.GCEInstanceName)
	}
	fmt.Printf("Zone:         %s\n", host.Zone)
	fmt.Printf("Status:       %s\n", host.Status)
	if host.ErrorMessage != "" {
		fmt.Printf("Error:        %s\n", host.ErrorMessage)
		if host.ErrorSuggestion != "" {
			fmt.Printf("Suggestion:   %s\n", host.ErrorSuggestion)
		}
		if len(host.ErrorAlternativeZones) > 0 {
			fmt.Printf("Try Zones:    %s\n", strings.Join(host.ErrorAlternativeZones, ", "))
		}
		if host.ErrorRetryable {
			fmt.Printf("Retryable:    yes\n")
		} else {
			fmt.Printf("Retryable:    no\n")
		}
	}
	fmt.Printf("Machine Type: %s\n", host.MachineType)
	if host.IPAddress != "" {
		fmt.Printf("IP Address:   %s\n", host.IPAddress)
	}
	if host.GPUType != "" {
		fmt.Printf("GPU:          %s (x%d)\n", host.GPUType, host.GPUCount)
	}
	if host.MinCPUPlatform != "" {
		fmt.Printf("CPU Platform: %s (minimum)\n", host.MinCPUPlatform)
	}
	if host.BootDiskSizeGB != 0 {
		fmt.Printf("Boot Disk:    %d GB\n", host.BootDiskSizeGB)
	}
	fmt.Printf("Max Devices:  %d\n", host.MaxDevices)
	if host.Exclusive {
		fmt.Printf("Exclusive:    yes (fenced from automatic placement)\n")
	}
	if host.BatchID != "" {
		fmt.Printf("Batch:        %s\n", host.BatchID)
	}
	fmt.Printf("Created:      %s\n", host.CreatedAt.Format(time.RFC3339))
	if !host.VMTerminatesAt.IsZero() {
		fmt.Printf("VM Stops At:  %s (maximum run duration; a restart resets it)\n", host.VMTerminatesAt.Format(time.RFC3339))
	}
}

// printBatch prints a batch summary in detail form.
func printBatch(b *cco.BatchSummary) {
	if b.BatchID != "" {
		fmt.Printf("Batch ID:   %s\n", b.BatchID)
	}
	if b.Status != "" {
		fmt.Printf("Status:     %s\n", b.Status)
	}
	if b.Requested > 0 {
		fmt.Printf("Requested:  %d\n", b.Requested)
	}
	if b.Admitted > 0 {
		fmt.Printf("Admitted:   %d\n", b.Admitted)
	}
	if b.NewHosts > 0 {
		fmt.Printf("New hosts:  %d\n", b.NewHosts)
	}
	if b.Total > 0 {
		fmt.Printf("Members:    %d (%d running, %d errored)\n", b.Total, b.Running, b.Errored)
	}
	if b.FillPolicy != "" {
		policy := b.FillPolicy
		if b.ReadyTimeoutSeconds > 0 {
			policy += fmt.Sprintf(" (ready timeout %ds)", b.ReadyTimeoutSeconds)
		}
		fmt.Printf("Policy:     %s, placement %s\n", policy, b.Placement)
	}
	if !b.FailedAt.IsZero() {
		fmt.Printf("Failed:     %s (%s)\n", b.FailedAt.Format(time.RFC3339), b.FailureReason)
	}
	for _, w := range b.Warnings {
		fmt.Printf("Warning:    %s\n", w)
	}
}

// printBatches prints the batch list as a table.
func printBatches(batches []cco.BatchSummary) error {
	if len(batches) == 0 {
		fmt.Println("No batches found.")
		return nil
	}
	return table("BATCH ID\tSTATUS\tTOTAL\tRUNNING\tERRORED", func(w *tabwriter.Writer) {
		for _, b := range batches {
			fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\n", b.BatchID, b.Status, b.Total, b.Running, b.Errored)
		}
	})
}

// printBatchMembers prints a batch's devices with their connection info.
func printBatchMembers(members []cco.BatchMember) error {
	if len(members) == 0 {
		fmt.Println("No devices in batch.")
		return nil
	}
	return table("DEVICE ID\tNAME\tSTATUS\tADB SERIAL\tHOST", func(w *tabwriter.Writer) {
		for _, m := range members {
			serial := m.ADBSerial
			if serial == "" {
				serial = "-"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", m.DeviceID, m.Name, m.Status, serial, m.HostID)
		}
	})
}

func printDevices(devices []cco.Device) error {
	if len(devices) == 0 {
		fmt.Println("No devices found.")
		return nil
	}
	return table("ID\tNAME\tHOST\tSTATUS\tBUILD\tPINNED", func(w *tabwriter.Writer) {
		for _, d := range devices {
			build := d.BuildTarget
			if d.BuildID != "" {
				build = fmt.Sprintf("%s@%s", d.BuildTarget, d.BuildID)
			}
			if build == "" {
				build = "-"
			}
			pinned := "-"
			if !d.Lifecycle.PinnedAt.IsZero() {
				pinned = d.Lifecycle.PinnedReason
				if !d.Lifecycle.PinnedUntil.IsZero() {
					pinned += " until " + d.Lifecycle.PinnedUntil.Format("Jan 2 15:04")
				}
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", d.ID, d.Name, d.HostID, d.Status, build, pinned)
		}
	})
}

func printDevice(device *cco.Device) {
	fmt.Printf("ID:           %s\n", device.ID)
	fmt.Printf("Name:         %s\n", device.Name)
	fmt.Printf("Host ID:      %s\n", device.HostID)
	if device.BatchID != "" {
		fmt.Printf("Batch ID:     %s\n", device.BatchID)
	}
	fmt.Printf("Status:       %s\n", device.Status)
	if device.BuildTarget != "" {
		fmt.Printf("Build Target: %s\n", device.BuildTarget)
	}
	if device.BuildID != "" {
		fmt.Printf("Build ID:     %s\n", device.BuildID)
	}
	if device.GPUMode != "" {
		fmt.Printf("GPU Mode:     %s\n", device.GPUMode)
	}
	// The flat ADB fields belong to the first instance, so a group renders its
	// instances instead.
	if len(device.Instances) > 1 {
		printDeviceInstances(device.Instances)
	} else {
		if device.ADBPort > 0 {
			fmt.Printf("ADB Port:     %d\n", device.ADBPort)
		}
		if serial := adbSerial(device); serial != "" {
			fmt.Printf("ADB Serial:   %s\n", serial)
		}
	}
	if device.ErrorMessage != "" {
		fmt.Printf("Error:        %s\n", device.ErrorMessage)
	}
	fmt.Printf("Created:      %s\n", device.CreatedAt.Format(time.RFC3339))
	if !device.Lifecycle.ExpiresAt.IsZero() {
		fmt.Printf("Expires:      %s\n", device.Lifecycle.ExpiresAt.Format(time.RFC3339))
	}
}

// operationStatus maps the operation's done and error fields to running,
// failed or done.
func operationStatus(op *cco.Operation) string {
	switch {
	case !op.Done:
		return cco.StatusOpRunning
	case op.Error != nil:
		return cco.StatusOpFailed
	default:
		return cco.StatusOpDone
	}
}

func printOperations(ops []cco.Operation) error {
	if len(ops) == 0 {
		fmt.Println("No operations found.")
		return nil
	}
	return table("ID\tVERB\tRESOURCE\tSTATUS\tCREATED", func(w *tabwriter.Writer) {
		for i := range ops {
			op := &ops[i]
			fmt.Fprintf(w, "%s\t%s\t%s/%s\t%s\t%s\n", op.ID(), op.Metadata.Verb,
				op.Metadata.ResourceType, op.Metadata.ResourceID,
				operationStatus(op), op.Metadata.CreateTime.Format(time.RFC3339))
		}
	})
}

func printSnapshots(snaps []cco.Snapshot) error {
	if len(snaps) == 0 {
		fmt.Println("No snapshots found.")
		return nil
	}
	return table("ID\tSTATE\tCREATED\tCREATED BY", func(w *tabwriter.Writer) {
		for i := range snaps {
			s := &snaps[i]
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.ID, s.State,
				s.CreatedAt.Format(time.RFC3339), s.CreatedBy)
		}
	})
}

func printOperation(op *cco.Operation) {
	fmt.Printf("ID:         %s\n", op.ID())
	fmt.Printf("Verb:       %s\n", op.Metadata.Verb)
	fmt.Printf("Resource:   %s/%s\n", op.Metadata.ResourceType, op.Metadata.ResourceID)
	fmt.Printf("Status:     %s\n", operationStatus(op))
	fmt.Printf("Created:    %s\n", op.Metadata.CreateTime.Format(time.RFC3339))
	if op.Metadata.Attempt > 0 {
		fmt.Printf("Attempt:    %d\n", op.Metadata.Attempt)
	}
	if !op.Done && op.Metadata.LastError != "" {
		fmt.Printf("Last Error: %s\n", op.Metadata.LastError)
	}
	if op.Metadata.NextAttemptAt != nil {
		fmt.Printf("Next Try:   %s\n", op.Metadata.NextAttemptAt.Format(time.RFC3339))
	}
	if op.Error != nil {
		detail := op.Error.Detail
		if detail == "" {
			detail = op.Error.Title
		}
		fmt.Printf("Failure:    %s\n", detail)
	}
}

// printDeviceInstances renders the Android VMs of a device's CVD group. No
// serial is shown per instance because the host firewall opens only the first
// instance's ADB port. The others are reachable through the WebSocket bridge.
func printDeviceInstances(instances []cco.Instance) {
	fmt.Printf("Instances:    %d\n", len(instances))
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, inst := range instances {
		state := inst.State
		if state == "" {
			state = "-" // absent from a fleet listing that did answer
		}
		port := "-"
		if inst.ADBPort > 0 {
			port = fmt.Sprintf("adb %d", inst.ADBPort)
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", inst.Name, state, port, strings.Join(inst.Displays, ", "))
	}
	// A flush failure means the pipe is gone, so there is nowhere to report it.
	_ = w.Flush()
}

// adbSerial builds host:port from the device's host IP and ADB port,
// preferring the external IP. It returns "" when either is unknown.
func adbSerial(device *cco.Device) string {
	ip := device.HostExternalIP
	if ip == "" {
		ip = device.HostInternalIP
	}
	if ip == "" || device.ADBPort == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", ip, device.ADBPort)
}

// printResolution renders the platform's plan for a declarative create. It
// prints nothing for the zero value, which an imperative create returns.
func printResolution(res cco.Resolution) {
	if res.BuildAction == "" && res.HostAction == "" {
		return
	}
	switch res.BuildAction {
	case "register":
		output.Info("Build: registering %s", res.BuildName)
	default:
		output.Info("Build: %s (%s)", res.BuildName, res.BuildID)
	}
	if res.BuildTarget != "" {
		output.Info("Target: %s", res.BuildTarget)
	}
	switch res.HostAction {
	case "provision":
		shape := res.MachineType
		if res.CapacityModel != "" {
			shape += ", " + res.CapacityModel
		}
		if res.HostID != "" {
			output.Info("Host: provisioning %s (%s in %s)", res.HostID, shape, res.Zone)
		} else {
			output.Info("Host: will provision %s in %s", shape, res.Zone)
		}
		for _, r := range res.PlacementReasons {
			output.Info("Placement: %s", r)
		}
	default:
		output.Info("Host: placing on %s", res.HostID)
	}
	for _, w := range res.Warnings {
		output.Warn("%s", w)
	}
}

// yesNo renders a boolean table cell.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func printBuilds(builds []cco.Build) error {
	if len(builds) == 0 {
		fmt.Println("No builds registered.")
		return nil
	}
	return table("ID\tNAME\tSOURCE\tREF\tDEFAULT", func(w *tabwriter.Writer) {
		for i := range builds {
			b := &builds[i]
			ref := b.BuildID
			if ref == "" {
				ref = b.Branch
			}
			if b.Source == "url" {
				ref = fmt.Sprintf("%d artifact(s)", len(b.ArtifactSpecs))
			}
			def := ""
			if b.IsDefault {
				def = yesNo(true)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", b.ID, b.Name, b.Source, ref, def)
		}
	})
}

func printFailedTasks(tasks []cco.FailedTask) error {
	if len(tasks) == 0 {
		fmt.Println("No dead-lettered tasks.")
		return nil
	}
	return table("ID\tTYPE\tRESOURCE\tATTEMPTS\tLAST ERROR", func(w *tabwriter.Writer) {
		for _, t := range tasks {
			fmt.Fprintf(w, "%s\t%s\t%s\t%d/%d\t%s\n", t.ID, t.Type, t.ResourceID, t.Attempts, t.MaxAttempts, t.LastError)
		}
	})
}

// machineTypeGPU renders the GPU cell for a machine type, including whether
// the GPU is integrated or attachable.
func machineTypeGPU(m *cco.MachineType) string {
	switch m.GPUMode {
	case "integrated":
		return fmt.Sprintf("%s x%d (integrated)", m.IntegratedGPUType, m.IntegratedGPUCount)
	case "attachable":
		if m.DefaultGPUType != "" {
			return fmt.Sprintf("%s (attachable, up to %d)", m.DefaultGPUType, m.MaxGpus)
		}
		return "attachable"
	default:
		if m.GPUSupport {
			return yesNo(true)
		}
		return "-"
	}
}

func printMachineTypes(mts []cco.MachineType) error {
	if len(mts) == 0 {
		fmt.Println("No machine types found.")
		return nil
	}
	return table("ID\tVCPUS\tMEMORY\tARCH\tGPU\tENABLED\tDEFAULT", func(w *tabwriter.Writer) {
		for i := range mts {
			m := &mts[i]
			enabled := yesNo(m.IsEnabled)
			def := ""
			if m.IsDefault {
				def = "*"
			}
			fmt.Fprintf(w, "%s\t%d\t%d GB\t%s\t%s\t%s\t%s\n",
				m.ID, m.Vcpus, m.MemoryGB, m.Architecture, machineTypeGPU(m), enabled, def)
		}
	})
}

func printMachineType(m *cco.MachineType) {
	fmt.Printf("ID:           %s\n", m.ID)
	fmt.Printf("Name:         %s\n", m.Name)
	if m.Description != "" {
		fmt.Printf("Description:  %s\n", m.Description)
	}
	fmt.Printf("Family:       %s\n", m.Family)
	fmt.Printf("Category:     %s\n", m.Category)
	fmt.Printf("Architecture: %s\n", m.Architecture)
	if m.CPUType != "" {
		fmt.Printf("CPU:          %s\n", m.CPUType)
	}
	fmt.Printf("vCPUs:        %d\n", m.Vcpus)
	fmt.Printf("Memory:       %d GB\n", m.MemoryGB)
	fmt.Printf("GPU:          %s\n", machineTypeGPU(m))
	zones := "all"
	if len(m.Zones) > 0 {
		zones = strings.Join(m.Zones, ", ")
	}
	fmt.Printf("Zones:        %s\n", zones)
	if !m.IsEnabled {
		fmt.Printf("Enabled:      no (not offered for new hosts)\n")
	}
	if m.IsDefault {
		fmt.Printf("Default:      yes\n")
	}
}

func printMachineFamilies(families []cco.FamilySummary) error {
	if len(families) == 0 {
		fmt.Println("No machine families found.")
		return nil
	}
	return table("FAMILY\tNAME\tCATEGORY\tARCH\tGPU\tTYPES", func(w *tabwriter.Writer) {
		for _, f := range families {
			gpu := "-"
			if f.GPUSupport {
				gpu = yesNo(true)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\n",
				f.Family, f.Name, f.Category, f.Architecture, gpu, f.TypeCount)
		}
	})
}

// printZones renders the zone catalog. The machine-type and GPU columns appear
// only with their filter, because without it the omitempty wire fields would
// read as "unavailable".
func printZones(zones []cco.ZoneAvailability, withMachineType, withGPU bool) error {
	if len(zones) == 0 {
		fmt.Println("No zones found.")
		return nil
	}
	header := "ZONE\tREGION\tSTATUS\tENABLED"
	if withMachineType {
		header += "\tMACHINE TYPE"
	}
	if withGPU {
		header += "\tGPU\tMAX GPUS"
	}
	return table(header, func(w *tabwriter.Writer) {
		for _, z := range zones {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s", z.Zone, z.Region, z.Status, yesNo(z.IsEnabled))
			if withMachineType {
				fmt.Fprintf(w, "\t%s", availability(z.MachineTypeAvailable))
			}
			if withGPU {
				fmt.Fprintf(w, "\t%s\t%d", availability(z.GPUAvailable), z.GPUMaxCount)
			}
			fmt.Fprintln(w)
		}
	})
}

// availability renders a per-zone resource-availability cell.
func availability(ok bool) string {
	if ok {
		return "available"
	}
	return "unavailable"
}

func printQuota(q *cco.QuotaResponse) {
	fmt.Printf("Max Hosts:    %d\n", q.MaxHosts)
	fmt.Printf("Used:         %d\n", q.UsedHosts)
	fmt.Printf("Available:    %d\n", q.Available)
	fmt.Printf("Usage:        %.0f%%\n", q.Percentage)
	if !q.HighWaterAt.IsZero() {
		fmt.Printf("Peak (7d):    %d hosts at %s\n", q.HighWaterUsedHosts, q.HighWaterAt.Format(time.RFC3339))
	}
}

func printBuild(b *cco.Build) {
	fmt.Printf("ID:           %s\n", b.ID)
	fmt.Printf("Name:         %s\n", b.Name)
	fmt.Printf("Source:       %s\n", b.Source)
	fmt.Printf("Target:       %s\n", b.Target)
	if b.BuildID != "" {
		fmt.Printf("Build ID:     %s\n", b.BuildID)
	}
	if b.Branch != "" {
		fmt.Printf("Branch:       %s\n", b.Branch)
	}
	if b.Flavor != "" {
		fmt.Printf("Flavor:       %s\n", b.Flavor)
	}
	if b.Fingerprint != "" {
		fmt.Printf("Fingerprint:  %s\n", b.Fingerprint)
	}
	if b.CreatedBy != "" {
		fmt.Printf("Created By:   %s\n", b.CreatedBy)
	}
	if b.Description != "" {
		fmt.Printf("Description:  %s\n", b.Description)
	}
	if b.IsDefault {
		fmt.Printf("Default:      %s\n", yesNo(true))
	}
	for _, a := range b.ArtifactSpecs {
		fmt.Printf("Artifact:     [%s] %s\n", a.Type, a.URL)
	}
}

func printDisplays(resp *cco.ListDisplaysResponse) error {
	if len(resp.Displays) == 0 {
		fmt.Println("No displays.")
	} else {
		if err := table("NUMBER\tWIDTH\tHEIGHT\tDPI\tREFRESH", func(w *tabwriter.Writer) {
			for _, d := range resp.Displays {
				fmt.Fprintf(w, "%d\t%d\t%d\t%d\t%d Hz\n", d.DisplayNumber, d.Width, d.Height, d.DPI, d.RefreshRateHz)
			}
		}); err != nil {
			return err
		}
	}
	if len(resp.Presets) == 0 {
		return nil
	}
	fmt.Println()
	fmt.Println("Presets (for 'device display add --preset'):")
	return table("NAME\tWIDTH\tHEIGHT\tDPI\tREFRESH", func(w *tabwriter.Writer) {
		for _, p := range resp.Presets {
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d Hz\n", p.Name, p.Width, p.Height, p.DPI, p.RefreshRateHz)
		}
	})
}

func printLifecycle(lc *cco.DeviceLifecycle) {
	fmt.Printf("Mode:         %s\n", lc.Mode)
	if lc.TTLSeconds > 0 {
		fmt.Printf("TTL:          %s\n", (time.Duration(lc.TTLSeconds) * time.Second).String())
	}
	if !lc.ExpiresAt.IsZero() {
		fmt.Printf("Expires:      %s\n", lc.ExpiresAt.Format(time.RFC3339))
	}
	if lc.ExtendCount > 0 {
		fmt.Printf("Extensions:   %d\n", lc.ExtendCount)
	}
	if lc.PinnedAt.IsZero() {
		fmt.Printf("Pinned:       no\n")
		return
	}
	fmt.Printf("Pinned:       %s by %s at %s\n", lc.PinnedReason, lc.PinnedBy, lc.PinnedAt.Format(time.RFC3339))
	if lc.PinnedReasonDetail != "" {
		fmt.Printf("Pin Detail:   %s\n", lc.PinnedReasonDetail)
	}
	if !lc.PinnedUntil.IsZero() {
		fmt.Printf("Pin Expires:  %s\n", lc.PinnedUntil.Format(time.RFC3339))
	} else {
		fmt.Printf("Pin Expires:  never (explicit unpin required)\n")
	}
}

// printAdminStats renders the platform-statistics snapshot.
func printAdminStats(s *cco.AdminStats) {
	fmt.Printf("Hosts:    %d active / %d total (quota %d)\n", s.Hosts.Active, s.Hosts.Total, s.Hosts.MaxQuota)
	fmt.Printf("Devices:  %d active / %d total\n", s.Devices.Active, s.Devices.Total)
	if line := statusCounts(s.HostsByStatus); line != "" {
		fmt.Printf("Hosts by status:   %s\n", line)
	}
	if line := statusCounts(s.DevicesByStatus); line != "" {
		fmt.Printf("Devices by status: %s\n", line)
	}
	fmt.Printf("As of:    %s\n", s.Timestamp.Format(time.RFC3339))
}

// statusCounts renders a status to count map in stable order.
func statusCounts(m map[string]int64) string {
	parts := make([]string, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}

// reclaimableTime renders an optional timestamp cell.
func reclaimableTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format(time.RFC3339)
}

// printReclaimable renders the reclaimable-capacity worklist, one section
// per bucket, skipping empty buckets.
func printReclaimable(r *cco.Reclaimable) error {
	if len(r.PinnedDevices)+len(r.StoppedDevices)+len(r.StoppedHosts)+len(r.ExpiringHosts) == 0 {
		fmt.Println("Nothing to reclaim.")
		return nil
	}
	deviceSection := func(title string, devices []cco.ReclaimableDevice) error {
		if len(devices) == 0 {
			return nil
		}
		fmt.Printf("%s (%d):\n", title, len(devices))
		return table("ID\tNAME\tHOST\tSTATUS\tPINNED BY\tREASON\tUNTIL", func(w *tabwriter.Writer) {
			for _, d := range devices {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					d.ID, d.Name, d.HostID, d.Status, d.PinnedBy, d.PinnedReason, reclaimableTime(d.PinnedUntil))
			}
		})
	}
	hostSection := func(title string, hosts []cco.ReclaimableHost) error {
		if len(hosts) == 0 {
			return nil
		}
		fmt.Printf("%s (%d):\n", title, len(hosts))
		return table("ID\tNAME\tZONE\tSTATUS\tDEVICES\tEXPIRES", func(w *tabwriter.Writer) {
			for _, h := range hosts {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n",
					h.ID, h.Name, h.Zone, h.Status, h.CurrentDeviceCount, reclaimableTime(h.ExpiresAt))
			}
		})
	}
	if err := deviceSection("Pinned devices", r.PinnedDevices); err != nil {
		return err
	}
	if err := deviceSection("Stopped devices", r.StoppedDevices); err != nil {
		return err
	}
	if err := hostSection("Stopped hosts (still billed)", r.StoppedHosts); err != nil {
		return err
	}
	return hostSection("Hosts expiring within the hour", r.ExpiringHosts)
}

// printAuditEntries renders the audit-log page, newest first.
func printAuditEntries(resp *cco.AuditListResponse) error {
	if len(resp.Items) == 0 {
		fmt.Println("No audit entries match.")
		return nil
	}
	err := table("TIME\tACTOR\tACTION\tRESOURCE", func(w *tabwriter.Writer) {
		for _, e := range resp.Items {
			resource := e.ResourceType
			if e.ResourceID != "" {
				resource += "/" + e.ResourceID
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
				e.Timestamp.Format(time.RFC3339), e.ActorEmail, e.Action, resource)
		}
	})
	if err != nil {
		return err
	}
	if resp.Total > int64(len(resp.Items)) {
		fmt.Printf("Showing %d of %d (offset %d); page with --offset/--limit\n",
			len(resp.Items), resp.Total, resp.Offset)
	}
	return nil
}

// printControlPlane renders the snapshot as compact panels, skipping empty
// ones. --json carries the full data.
func printControlPlane(cp *cco.ControlPlane) error {
	fmt.Printf("As of:    %s (up to %d workers/instance)\n", cp.Now.Format(time.RFC3339), cp.WorkersPerInstance)
	fmt.Printf("Queue:    %d pending (oldest %ds), %d leased, %d failed\n",
		cp.Queue.Pending, cp.Queue.OldestPendingAgeSeconds, cp.Queue.Leased, cp.Queue.Failed)
	if len(cp.TypeCounts) > 0 {
		parts := make([]string, len(cp.TypeCounts))
		for i, tc := range cp.TypeCounts {
			parts[i] = fmt.Sprintf("%s %s=%d", tc.Type, tc.State, tc.Count)
		}
		fmt.Printf("By type:  %s\n", strings.Join(parts, ", "))
	}
	if err := printLiveTasks(cp.LiveTasks); err != nil {
		return err
	}
	return printLeaderLeases(cp.Leaders)
}

// printCapacity renders the capacity snapshot as a fleet summary, a row per
// zone with its server-computed verdict, and the quota pools.
func printCapacity(s *cco.Capacity) error {
	fmt.Printf("As of:    %s\n", s.Now.Format(time.RFC3339))
	fmt.Printf("Fleet:    %d machines, %d devices across %d regions; %d of %d enabled zones clear\n",
		s.Fleet.Machines, s.Fleet.Devices, s.Fleet.Regions, s.Fleet.ZonesClear, s.Fleet.ZonesEnabled)
	if err := printCapacityRegions(s.Regions); err != nil {
		return err
	}
	if err := printCapacityZones(s.Zones); err != nil {
		return err
	}
	if err := printCapacityPools(s.Pools, s.ReserveMarginPercent); err != nil {
		return err
	}
	printSpotPanel(&s.Spot)
	return nil
}

func printCapacityRegions(regions []cco.CapacityRegion) error {
	if len(regions) == 0 {
		return nil
	}
	fmt.Println("\nRegions:")
	return table("REGION\tMACHINES\tDEVICES\tZONES ON\tQUOTA", func(w *tabwriter.Writer) {
		for _, r := range regions {
			quota := string(r.QuotaState)
			if r.QuotaPool != "" {
				quota = fmt.Sprintf("%s (%s %d/%d)", quota, r.QuotaPool, r.QuotaUsage, r.QuotaLimit)
			}
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%s\n",
				r.Region, r.Machines, r.Devices, r.ZonesEnabled, quota)
		}
	})
}

func printCapacityZones(zones []cco.CapacityZone) error {
	if len(zones) == 0 {
		return nil
	}
	fmt.Println("\nZones:")
	return table("ZONE\tENABLED\tVERDICT\tMACHINES\tDEVICES\tSPOT\tWHY", func(w *tabwriter.Writer) {
		for _, z := range zones {
			spot := "no signal"
			if z.Spot.MachineType != "" {
				spot = string(z.Spot.Band)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%s\t%s\n",
				z.Zone, yesNo(z.Enabled), z.Verdict, z.Machines, z.Devices, spot, z.VerdictDetail)
		}
	})
}

// printCapacityPools prints one row per quota pool. A pool with no series
// prints as unknown, because Google publishes no per-family metric until the
// family is first used.
func printCapacityPools(pools []cco.CapacityPool, marginPercent int64) error {
	if len(pools) == 0 {
		return nil
	}
	fmt.Printf("\nQuota headroom (ranking reserves %d%% of each limit):\n", marginPercent)
	return table("REGION\tPOOL\tSTATE\tUSAGE\tLIMIT\tRESERVE\tFAMILIES", func(w *tabwriter.Writer) {
		for _, p := range pools {
			usage, limit, reserve := "-", "-", "-"
			if p.State != "unknown" {
				usage = strconv.FormatInt(p.Usage, 10)
				limit = strconv.FormatInt(p.Limit, 10)
				reserve = strconv.FormatInt(p.Reserve, 10)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				p.Region, p.Pool, p.State, usage, limit, reserve, strings.Join(p.Families, ","))
		}
	})
}

func printLiveTasks(tasks []cco.LiveTask) error {
	if len(tasks) == 0 {
		return nil
	}
	fmt.Printf("\nLive tasks (%d):\n", len(tasks))
	return table("ID\tTYPE\tSTATE\tRESOURCE\tATTEMPTS\tHOLDER", func(w *tabwriter.Writer) {
		for _, t := range tasks {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d/%d\t%s\n",
				t.ID, t.Type, t.State, t.ResourceID, t.Attempts, t.MaxAttempts, t.LeaseHolder)
		}
	})
}

func printLeaderLeases(leaders []cco.LeaderLease) error {
	if len(leaders) == 0 {
		return nil
	}
	fmt.Println("\nLeaders:")
	return table("NAME\tHOLDER\tRENEWED\tEXPIRES", func(w *tabwriter.Writer) {
		for _, l := range leaders {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
				l.Name, l.Holder, l.RenewedAt.Format(time.RFC3339), reclaimableTime(l.LeaseExpiry))
		}
	})
}

func printSpotPanel(s *cco.SpotPanel) {
	fmt.Println("\nSpot:")
	if len(s.Fleet) > 0 {
		parts := make([]string, len(s.Fleet))
		for i, f := range s.Fleet {
			parts[i] = fmt.Sprintf("%s %s=%d", f.Model, f.Status, f.Count)
		}
		fmt.Printf("Fleet:            %s\n", strings.Join(parts, ", "))
	}
	fmt.Printf("Preemptions (7d): %d preempted, %d recovered", s.Preempted7D, s.Recovered7D)
	if s.TypicalRecoverySeconds > 0 {
		fmt.Printf(" (typical %ds)", s.TypicalRecoverySeconds)
	}
	fmt.Println()
	advice := "disabled"
	if s.Advice.Enabled {
		advice = fmt.Sprintf("enabled, %d cells", s.Advice.Cells)
		if !s.Advice.NewestSyncedAt.IsZero() {
			advice += ", newest " + s.Advice.NewestSyncedAt.Format(time.RFC3339)
		}
		// The server decides staleness.
		if s.Advice.Stale {
			advice += " (stale, ranking on quota and health only)"
		}
	}
	fmt.Printf("Advice:           %s\n", advice)
}
