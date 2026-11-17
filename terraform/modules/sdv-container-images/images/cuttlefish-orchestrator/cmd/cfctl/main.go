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

// cfctl is a CLI tool for managing Cloud Cuttlefish hosts and devices.
package main

import (
	"context"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cli"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
)

// Version overrides the derived version when set via -ldflags -X main.Version.
var Version = ""

// version prefers the ldflags override, then the VCS stamp Go embeds in
// repo builds, then "dev".
func version() string {
	if Version != "" {
		return Version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	var revision string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if revision == "" {
		return "dev"
	}
	v := "dev+" + revision[:min(len(revision), 12)]
	if dirty {
		v += "-dirty"
	}
	return v
}

func main() {
	v := version()
	// The User-Agent header must match --version.
	cco.Version = v

	// The effective config is resolved from the file after flag parsing. A
	// load error fails only config-dependent commands, so --version, --help
	// and completion still work.
	file, fileErr := config.Load()

	var c cli.CLI
	c.ConfigErr = fileErr

	sigCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	parser, err := cli.NewParser(&c, file, v)
	if err != nil {
		panic(err) // A parser construction error is a programming error.
	}
	kongCtx, err := parser.Parse(os.Args[1:])
	parser.FatalIfErrorf(err)

	ctx, timeoutCancel := c.CommandContext(sigCtx)
	defer timeoutCancel()

	kongCtx.BindTo(ctx, (*context.Context)(nil))

	err = kongCtx.Run()
	// sigCtx ends only on a signal while Run is in progress.
	kongCtx.FatalIfErrorf(cli.WithExitCode(err, sigCtx.Err() != nil))
}
