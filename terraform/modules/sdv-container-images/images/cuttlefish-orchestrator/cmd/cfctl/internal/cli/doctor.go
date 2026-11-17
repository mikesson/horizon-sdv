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
	"strings"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/auth"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// DoctorCmd implements 'cfctl doctor', which checks whether this machine can
// use the deployment.
type DoctorCmd struct {
	Bucket string `placeholder:"BUCKET" help:"Also probe the server's access to this artifact bucket (the server does not advertise one, so it must be named)"`
	HostID string `name:"host" placeholder:"HOST" help:"With --bucket: also probe this host's access (host ID or name)"`
}

// doctorCheck is one line of the diagnosis. A skipped check is neither a
// pass nor a failure.
type doctorCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped,omitempty"`
	Detail  string `json:"detail"`
}

// doctorAuthCheck names the probe that reports a client construction failure.
const doctorAuthCheck = "auth"

// doctorSignerCheck names the probe that runs without a client, since the
// signer matters most when the client cannot be built.
const doctorSignerCheck = "signer"

// doctorProbe is one named check.
type doctorProbe struct {
	name string
	run  func(context.Context) (string, error)
}

// probes builds the check list. Each error names the request it came from,
// since the error line is all the user sees.
func (c *DoctorCmd) probes(client *cco.Client, cfg *config.Config) []doctorProbe {
	return []doctorProbe{
		{doctorSignerCheck, func(ctx context.Context) (string, error) {
			signer, ok := auth.SignerFor(ctx, cfg, auth.DetectSigner)
			if !ok {
				return "none (" + string(auth.Resolve(cfg).Method) + ")", nil
			}
			return "signing as " + signer.Description, nil
		}},
		{"health", func(ctx context.Context) (string, error) {
			h, err := client.Health(ctx)
			if err != nil {
				return "", fmt.Errorf("GET /health: %w", err)
			}
			return h.Status, nil
		}},
		{"ready", func(ctx context.Context) (string, error) {
			r, err := client.GetReady(ctx)
			if err != nil {
				return "", fmt.Errorf("GET /ready: %w", err)
			}
			return r.Status, nil
		}},
		{"info", func(ctx context.Context) (string, error) {
			i, err := client.Info(ctx)
			if err != nil {
				return "", fmt.Errorf("GET /info: %w", err)
			}
			return "server version " + i.Version, nil
		}},
		{doctorAuthCheck, func(ctx context.Context) (string, error) {
			me, err := client.Me(ctx)
			if err != nil {
				return "", fmt.Errorf("GET /v2/me: %w", err)
			}
			return "authenticated as " + me.Email, nil
		}},
		{"quota", func(ctx context.Context) (string, error) {
			q, err := client.GetQuota(ctx)
			if err != nil {
				return "", fmt.Errorf("GET /v2/quota: %w", err)
			}
			return fmt.Sprintf("%d/%d hosts used, %d available", q.UsedHosts, q.MaxHosts, q.Available), nil
		}},
		{"images", func(ctx context.Context) (string, error) {
			return c.probeImages(ctx, client)
		}},
		{"storage", func(ctx context.Context) (string, error) {
			if c.Bucket == "" {
				return "skipped (pass --bucket to probe artifact access)", nil
			}
			hostID := c.HostID
			if err := resolveHostArg(ctx, client, &hostID); err != nil {
				return "", err
			}
			v, err := client.VerifyAccess(ctx, cco.VerifyAccessRequest{Bucket: c.Bucket, HostID: hostID})
			if err != nil {
				return "", fmt.Errorf("POST /v2/storage/verify-access: %w", err)
			}
			if !v.CanProceed {
				return "", fmt.Errorf("%s: %s", v.GSURI, v.Message)
			}
			return v.GSURI + " accessible", nil
		}},
	}
}

// probeImages fails when any enabled machine family cannot boot a host, since
// every create for that family is refused. A family that should not serve
// creates is disabled, which takes it out of the check.
func (c *DoctorCmd) probeImages(ctx context.Context, client *cco.Client) (string, error) {
	list, err := client.ListHostImageConfigs(ctx, cco.HostImageConfigListOptions{Limit: maxHostImageLimit, EnabledOnly: true})
	if err != nil {
		return "", fmt.Errorf("GET /v2/admin/host-image-configs: %w", err)
	}
	var ok, broken []string
	for _, cfg := range list.Items {
		if cfg.Resolved.Ok {
			ok = append(ok, cfg.MachineFamily+"="+cfg.Resolved.ImageName)
			continue
		}
		broken = append(broken, cfg.MachineFamily+": "+cfg.Resolved.Message)
	}
	if len(broken) > 0 {
		return "", fmt.Errorf("%d of %d enabled machine families cannot boot a host: %s",
			len(broken), len(list.Items), strings.Join(broken, "; "))
	}
	if len(ok) == 0 {
		return "", errors.New("no machine family has an enabled host image config, so every host create is refused")
	}
	return strings.Join(ok, ", "), nil
}

// runDoctorProbe runs one probe. Without a client, the auth row carries the
// construction error and every row that needs the client is skipped.
func runDoctorProbe(ctx context.Context, probe doctorProbe, clientErr error) doctorCheck {
	switch {
	case clientErr != nil && probe.name == doctorAuthCheck:
		return doctorCheck{Name: probe.name, Detail: clientErr.Error()}
	case clientErr != nil && probe.name != doctorSignerCheck:
		return doctorCheck{Name: probe.name, Skipped: true, Detail: "skipped (no API client: see auth)"}
	}
	detail, err := probe.run(ctx)
	if err != nil {
		return doctorCheck{Name: probe.name, Detail: err.Error()}
	}
	return doctorCheck{Name: probe.name, OK: true, Detail: detail}
}

// Run checks connectivity, auth, quota, host images and optionally storage
// access. It
// returns an error when any check fails, so CI can gate on it. It builds its
// own client so a credential failure becomes a failed auth row instead of
// ending the command before any check runs.
func (c *DoctorCmd) Run(ctx context.Context, cli *CLI, cfg *config.Config) error {
	client, clientErr := cco.NewClient(ctx, cli.Server, cfg, cli.Timeout)
	// A --host name that matches no host or several hosts ends the command
	// with exit 4 or 5 like any other host argument. A failed lookup request
	// is left to the storage row, which repeats it.
	if clientErr == nil && c.Bucket != "" {
		err := resolveHostArg(ctx, client, &c.HostID)
		if _, ok := errors.AsType[*nameNotFoundError](err); ok {
			return err
		}
		if _, ok := errors.AsType[*ambiguousNameError](err); ok {
			return err
		}
	}
	probes := c.probes(client, cfg)
	results := make([]doctorCheck, 0, len(probes))
	failed := 0
	for _, probe := range probes {
		check := runDoctorProbe(ctx, probe, clientErr)
		if !check.OK && !check.Skipped {
			failed++
		}
		results = append(results, check)
		if !cli.JSON {
			mark := "OK"
			switch {
			case check.Skipped:
				mark = "SKIP"
			case !check.OK:
				mark = "FAIL"
			}
			fmt.Printf("%-4s %-8s %s\n", mark, probe.name, check.Detail)
		}
	}

	if cli.JSON {
		if err := printJSON(results); err != nil {
			return err
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d checks failed", failed, len(probes))
	}
	if !cli.JSON {
		output.Success("All checks passed against %s", cli.Server)
	}
	return nil
}
