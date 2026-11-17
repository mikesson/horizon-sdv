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
	"fmt"
	"os"
	"path/filepath"
	"time"

	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/cco"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/config"
	"cloud-cuttlefish-orchestrator/cmd/cfctl/internal/output"
)

// The completion value kinds the shell scripts can ask for.
const (
	kindMachineType = "machine-type"
	kindZone        = "zone"
	kindHost        = "host"
	kindDevice      = "device"
	// kindProfile is read from the local config file and never cached.
	kindProfile = "profile"
)

// completionTTL is how long a cached value list stays fresh, per kind. Host and
// device names change often, so their window only covers repeated tab presses.
var completionTTL = map[string]time.Duration{
	kindMachineType: time.Hour,
	kindZone:        time.Hour,
	kindHost:        30 * time.Second,
	kindDevice:      30 * time.Second,
}

// completionFetchBound caps the server round trip. Stale values serve a shell
// better than a hung prompt.
const completionFetchBound = 2 * time.Second

// CompletionValuesCmd implements 'cfctl completion values', the hidden
// callback the completion scripts call for server-backed values. It prints
// nothing on failure, since any output would land on the user's command line.
type CompletionValuesCmd struct {
	Kind string `arg:"" enum:"machine-type,zone,host,device,profile" help:"Value kind: machine-type, zone, host, device, or profile"`
}

// completionCache is the on-disk shape of completion-cache.json in the config
// directory.
// Entries are keyed by the normalized server URL, then by kind, so switching
// servers never offers another deployment's IDs.
type completionCache struct {
	Servers map[string]map[string]completionEntry `json:"servers"`
}

type completionEntry struct {
	Values    []string  `json:"values"`
	FetchedAt time.Time `json:"fetched_at"`
}

// Run prints one completion value per line: fresh cache if present, else a
// bounded server fetch, else whatever stale values the cache still holds.
func (c *CompletionValuesCmd) Run(ctx context.Context, cli *CLI, cfg *config.Config) error {
	if c.Kind == kindProfile {
		printValues(cli.configFile.Names())
		return nil
	}
	server, err := normalizeServerURL(cli.Server, cfg.AuthNone)
	if err != nil {
		output.Debug("completion skipped: %v", err)
		return nil
	}
	cachePath, cache := loadCompletionCache()
	kinds := cache.Servers[server]
	if kinds == nil {
		kinds = map[string]completionEntry{}
		cache.Servers[server] = kinds
	}
	entry := kinds[c.Kind]
	if time.Since(entry.FetchedAt) < completionTTL[c.Kind] {
		printValues(entry.Values)
		return nil
	}

	values, err := fetchCompletionValues(ctx, server, cfg, c.Kind)
	if err != nil {
		output.Debug("completion fetch failed, serving stale cache: %v", err)
		printValues(entry.Values)
		return nil
	}
	printValues(values)

	kinds[c.Kind] = completionEntry{Values: values, FetchedAt: time.Now()}
	saveCompletionCache(cachePath, cache)
	return nil
}

// completionFetchers maps each kind to its server read. Catalog kinds offer
// only enabled entries, since a create rejects a disabled machine type or zone.
var completionFetchers = map[string]func(context.Context, *cco.Client) ([]string, error){
	kindMachineType: func(ctx context.Context, client *cco.Client) ([]string, error) {
		mts, err := client.ListMachineTypes(ctx, cco.MachineTypeListOptions{})
		if err != nil {
			return nil, fmt.Errorf("listing machine types: %w", err)
		}
		values := make([]string, 0, len(mts))
		for i := range mts {
			if mts[i].IsEnabled {
				values = append(values, mts[i].ID)
			}
		}
		return values, nil
	},
	kindZone: func(ctx context.Context, client *cco.Client) ([]string, error) {
		zones, err := client.ListZoneAvailability(ctx, "", "")
		if err != nil {
			return nil, fmt.Errorf("listing zones: %w", err)
		}
		values := make([]string, 0, len(zones))
		for _, z := range zones {
			if z.IsEnabled {
				values = append(values, z.Zone)
			}
		}
		return values, nil
	},
	kindHost: func(ctx context.Context, client *cco.Client) ([]string, error) {
		hosts, err := client.ListHosts(ctx, cco.HostListOptions{})
		if err != nil {
			return nil, fmt.Errorf("listing hosts: %w", err)
		}
		names := make([]string, 0, len(hosts))
		for i := range hosts {
			if hosts[i].Status != cco.StatusDeleted {
				names = append(names, hosts[i].Name)
			}
		}
		return uniqueNames(names), nil
	},
	kindDevice: func(ctx context.Context, client *cco.Client) ([]string, error) {
		devices, err := client.ListDevices(ctx, cco.DeviceListOptions{})
		if err != nil {
			return nil, fmt.Errorf("listing devices: %w", err)
		}
		names := make([]string, 0, len(devices))
		for i := range devices {
			if devices[i].Status != cco.StatusDeleted {
				names = append(names, devices[i].Name)
			}
		}
		return uniqueNames(names), nil
	},
}

// uniqueNames drops empty and repeated names, keeping the first of each in
// order. A repeated name still completes; using it fails with the matches.
func uniqueNames(names []string) []string {
	seen := map[string]bool{"": true}
	out := names[:0]
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// fetchCompletionValues builds a client and asks the server for the kind's
// values, bounded by completionFetchBound.
func fetchCompletionValues(ctx context.Context, server string, cfg *config.Config, kind string) ([]string, error) {
	fetch, ok := completionFetchers[kind]
	if !ok {
		return nil, fmt.Errorf("unknown completion kind %q", kind)
	}
	ctx, cancel := context.WithTimeout(ctx, completionFetchBound)
	defer cancel()
	client, err := cco.NewClient(ctx, server, cfg, 0)
	if err != nil {
		return nil, fmt.Errorf("building client: %w", err)
	}
	return fetch(ctx, client)
}

func printValues(values []string) {
	for _, v := range values {
		fmt.Println(v)
	}
}

// completionCachePath is completion-cache.json beside the config file.
func completionCachePath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", fmt.Errorf("resolving config dir: %w", err)
	}
	return filepath.Join(dir, "completion-cache.json"), nil
}

// loadCompletionCache reads the cache. A missing or corrupt cache yields an
// empty one, which forces a fetch.
func loadCompletionCache() (string, *completionCache) {
	cache := &completionCache{Servers: map[string]map[string]completionEntry{}}
	path, err := completionCachePath()
	if err != nil {
		return "", cache
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return path, cache
	}
	if err := json.Unmarshal(data, cache); err != nil || cache.Servers == nil {
		cache.Servers = map[string]map[string]completionEntry{}
	}
	return path, cache
}

// saveCompletionCache writes the cache atomically with mode 0600 and ignores
// errors, so completion never fails because of the cache.
func saveCompletionCache(path string, cache *completionCache) {
	if path == "" {
		return
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "completion-cache-*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err == nil {
		if _, err := tmp.Write(data); err == nil && tmp.Close() == nil {
			_ = os.Rename(tmp.Name(), path)
			return
		}
	}
	_ = tmp.Close()
}
