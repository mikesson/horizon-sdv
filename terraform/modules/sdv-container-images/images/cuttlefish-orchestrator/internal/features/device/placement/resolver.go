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

package placement

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"cloud-cuttlefish-orchestrator/internal/domain/batch"
	"cloud-cuttlefish-orchestrator/internal/domain/build"
	"cloud-cuttlefish-orchestrator/internal/domain/device"
	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/features/device/validation"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// PolicyReader reads operator policy defaults from system_config.
type PolicyReader interface {
	Get(ctx context.Context, key string) (*models.SystemConfig, error)
}

// ZoneRanker ranks placement zones for a host shape. Satisfied by the
// gcecatalog feature's zone ranker and wired in main, because features must
// not import features.
type ZoneRanker interface {
	RankZones(ctx context.Context, req gcecatalog.ValidationRequest, opts gcecatalog.RankOptions) ([]gcecatalog.ZoneCandidate, error)
}

// StockoutMemory is placement's record of which machine families are out of
// capacity in which zones. The shape ladder consults it before probing a
// rung, so a shape GCE has just refused everywhere is skipped without asking
// again. Satisfied by the Spanner zone-health repository and
// wired in main, because features must not import features.
type StockoutMemory interface {
	ListCapacityCells(ctx context.Context, machineFamilies []string) ([]*gcecatalog.ZoneHealth, error)
}

// ARMCapability answers whether this deployment can build an arm64 host.
// Satisfied by the host feature's probe and wired in main, because features
// must not import features. Nil reads as not ready, which refuses an arm
// create rather than routing it onto hardware that cannot run it.
type ARMCapability interface {
	Ready(ctx context.Context) bool
}

// TrackingBuildRegistrar registers a tracking build through the build
// feature's service (validation + audit), so placement never writes build
// rows directly. Satisfied by the build service and wired in main, because
// features must not import features.
type TrackingBuildRegistrar interface {
	Register(ctx context.Context, req build.CreateRequest) (*build.Build, error)
}

// ResolverDeps is the dependency set a Resolver is built from.
type ResolverDeps struct {
	Capacity     CapacityStore
	Builds       build.Reader
	Policy       PolicyReader
	MachineTypes machinetype.Catalog
	Ranker       ZoneRanker
	Registrar    TrackingBuildRegistrar
	ZoneHealth   StockoutMemory
	ARM          ARMCapability
	Logger       *slog.Logger
}

// Resolver resolves declarative requirements into concrete resources: a
// registered build, a host with capacity, or the provisioning template for a
// fresh host. It decides and never commits. The device service owns the
// create/provision writes a resolution leads to.
type Resolver struct {
	capacity     CapacityStore
	builds       build.Reader
	policy       PolicyReader
	machineTypes machinetype.Catalog
	ranker       ZoneRanker
	registrar    TrackingBuildRegistrar
	zoneHealth   StockoutMemory
	arm          ARMCapability
	logger       *slog.Logger
}

// NewResolver creates a placement resolver.
func NewResolver(deps ResolverDeps) *Resolver {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Resolver{
		capacity:     deps.Capacity,
		builds:       deps.Builds,
		policy:       deps.Policy,
		machineTypes: deps.MachineTypes,
		ranker:       deps.Ranker,
		registrar:    deps.Registrar,
		zoneHealth:   deps.ZoneHealth,
		arm:          deps.ARM,
		logger:       logger,
	}
}

// Compiled fallbacks for databases predating the policy seed rows.
const (
	fallbackUpstreamBranch = "aosp-android-latest-release"
	fallbackUpstreamTarget = "aosp_cf_x86_64_only_phone-userdebug"
)

// ResolveBuild resolves the build requirement into a registered build and
// stamps it onto the request and resolution. A tracking requirement with no
// registered tracking build registers one (the resolution's one side effect,
// routed through the build service).
func (r *Resolver) ResolveBuild(ctx context.Context, br device.BuildRequirement, createdBy string, req *device.CreateRequest, res *device.Resolution) error {
	if instancesBringBuilds(br, req) {
		res.BuildAction = device.BuildActionUse
		return nil
	}
	var (
		b   *build.Build
		err error
	)
	switch {
	case br.Registered != "":
		b, err = r.builds.Get(ctx, br.Registered)
		if err != nil {
			return fmt.Errorf("resolving requirements.build.registered: %w", err)
		}
		res.BuildAction = device.BuildActionUse

	case br.TrackBranch != "":
		b, res.BuildAction, err = r.findOrRegisterTracking(ctx, br.TrackBranch, br.TrackTarget, createdBy, false)
		if err != nil {
			return err
		}

	default:
		var found bool
		b, found, err = r.defaultBuild(ctx)
		if err != nil {
			return err
		}
		if found {
			res.BuildAction = device.BuildActionUse
			break
		}
		branch, target, perr := r.upstreamDefault(ctx)
		if perr != nil {
			return perr
		}
		total, cerr := r.builds.Count(ctx, build.ListFilter{})
		if cerr != nil {
			return fmt.Errorf("counting builds: %w", cerr)
		}
		// First build in an empty registry becomes the default, so later
		// defaultless creates resolve to it instead of re-deriving.
		b, res.BuildAction, err = r.findOrRegisterTracking(ctx, branch, target, createdBy, total == 0)
		if err != nil {
			return err
		}
	}

	req.BuildID = b.ID
	req.BuildTarget = b.Target
	res.BuildID, res.BuildName, res.BuildTarget = b.ID, b.Name, b.Target
	return nil
}

// PlanBuild is ResolveBuild without side effects: a build that would be
// registered is described (name, action) but not created, so the resolution
// carries no ID for it.
func (r *Resolver) PlanBuild(ctx context.Context, br device.BuildRequirement, req *device.CreateRequest, res *device.Resolution) error {
	use := func(b *build.Build) {
		req.BuildID, req.BuildTarget = b.ID, b.Target
		res.BuildID, res.BuildName, res.BuildAction = b.ID, b.Name, device.BuildActionUse
	}
	wouldRegister := func(branch, target string) {
		req.BuildTarget = target
		res.BuildName = branch + " (latest)"
		res.BuildAction = device.BuildActionRegister
	}

	if instancesBringBuilds(br, req) {
		res.BuildAction = device.BuildActionUse
		return nil
	}

	switch {
	case br.Registered != "":
		b, err := r.builds.Get(ctx, br.Registered)
		if err != nil {
			return fmt.Errorf("resolving requirements.build.registered: %w", err)
		}
		use(b)

	case br.TrackBranch != "":
		b, found, err := r.findTracking(ctx, br.TrackBranch, br.TrackTarget)
		if err != nil {
			return err
		}
		if found {
			use(b)
			break
		}
		wouldRegister(br.TrackBranch, br.TrackTarget)

	default:
		b, found, err := r.defaultBuild(ctx)
		if err != nil {
			return err
		}
		if found {
			use(b)
			break
		}
		branch, target, err := r.upstreamDefault(ctx)
		if err != nil {
			return err
		}
		b, found, err = r.findTracking(ctx, branch, target)
		if err != nil {
			return err
		}
		if found {
			use(b)
			break
		}
		wouldRegister(branch, target)
	}
	return nil
}

// instancesBringBuilds reports whether a create with no build requirement
// already names a build for every instance of its env_config. Such a create
// gets no device-level build. The payload fills each instance's remaining
// gaps from the device-level build, so the platform default would merge its
// host package and disk fields into a group that chose its own. The action
// reads "use" because each instance uses the build it named; the resolution
// carries no build id since no single build is the device's.
func instancesBringBuilds(br device.BuildRequirement, req *device.CreateRequest) bool {
	if br.Registered != "" || br.TrackBranch != "" {
		return false
	}
	return validation.EveryInstanceHasBuild(req.EnvConfig)
}

// findTracking returns the existing tracking build (branch/target, no pinned
// build ID) for the pair; found=false when none is registered.
func (r *Resolver) findTracking(ctx context.Context, branch, target string) (*build.Build, bool, error) {
	src := build.SourceAB
	existing, err := r.builds.List(ctx, build.ListFilter{Branch: &branch, Target: &target, Source: &src, Limit: 50})
	if err != nil {
		return nil, false, fmt.Errorf("looking up tracking build %s/%s: %w", branch, target, err)
	}
	for _, eb := range existing {
		if eb.BuildID == "" {
			return eb, true, nil
		}
	}
	return nil, false, nil
}

// findOrRegisterTracking returns the tracking build (branch/target, no pinned
// build ID) for the pair, registering one through the build service when none
// exists (which validates and audits the registration). A concurrent
// first-use race can register a duplicate tracking row, which is benign: both
// fetch the same latest artifacts and later resolutions consistently pick one.
func (r *Resolver) findOrRegisterTracking(ctx context.Context, branch, target, createdBy string, asDefault bool) (*build.Build, string, error) {
	if existing, found, err := r.findTracking(ctx, branch, target); err != nil {
		return nil, "", err
	} else if found {
		return existing, device.BuildActionUse, nil
	}

	nb, err := r.registrar.Register(ctx, build.CreateRequest{
		Name:      branch + " (latest)",
		Source:    build.SourceAB,
		Branch:    branch,
		Target:    target,
		IsDefault: asDefault,
		CreatedBy: createdBy,
	})
	if err != nil {
		return nil, "", fmt.Errorf("registering tracking build %s/%s: %w", branch, target, err)
	}
	r.logger.Info("auto-registered tracking build",
		slog.String("build_id", nb.ID),
		slog.String("branch", branch),
		slog.String("target", target),
		slog.Bool("is_default", asDefault))
	return nb, device.BuildActionRegister, nil
}

// defaultBuild returns the registry's default build; found=false when none
// is flagged.
func (r *Resolver) defaultBuild(ctx context.Context) (*build.Build, bool, error) {
	isDefault := true
	builds, err := r.builds.List(ctx, build.ListFilter{IsDefault: &isDefault, Limit: 1})
	if err != nil {
		return nil, false, fmt.Errorf("resolving default build: %w", err)
	}
	if len(builds) == 0 {
		return nil, false, nil
	}
	return builds[0], true, nil
}

// upstreamDefault reads the operator's upstream build policy, falling back to
// the compiled defaults for databases predating the seed rows.
func (r *Resolver) upstreamDefault(ctx context.Context) (string, string, error) {
	branch, err := r.policyValue(ctx, models.ConfigKeyDefaultUpstreamBranch)
	if err != nil {
		return "", "", err
	}
	target, err := r.policyValue(ctx, models.ConfigKeyDefaultUpstreamTarget)
	if err != nil {
		return "", "", err
	}
	if branch == "" {
		branch = fallbackUpstreamBranch
	}
	if target == "" {
		target = fallbackUpstreamTarget
	}
	return branch, target, nil
}

// policyValue reads one system_config key; absence reads as empty.
func (r *Resolver) policyValue(ctx context.Context, key string) (string, error) {
	cfg, err := r.policy.Get(ctx, key)
	if err != nil {
		if errors.Is(err, cspanner.ErrKeyNotFound) {
			return "", nil
		}
		return "", fmt.Errorf("reading policy %q: %w", key, err)
	}
	return cfg.AsString(), nil
}

// policyBool reads one boolean system_config key; absence reads as false.
func (r *Resolver) policyBool(ctx context.Context, key string) (bool, error) {
	cfg, err := r.policy.Get(ctx, key)
	if err != nil {
		if errors.Is(err, cspanner.ErrKeyNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("reading policy %q: %w", key, err)
	}
	return cfg.AsBool(), nil
}

// Pick is one placement decision: the chosen host (empty = no candidate),
// whether it holds the wanted fetch key (the fact the resolution's placement
// reasons surface), and whether a GPU preference went unmet.
type Pick struct {
	HostID string
	Warm   bool

	// Degraded reports that this create wanted a GPU and landed on a host
	// without one. The typed twin of Warning below, and the one a count reads:
	// matching the prose would silently stop counting the day it is reworded,
	// and it is only half the population anyway. A resolution that provisions
	// rather than places reports its own degradation separately.
	Degraded bool
	Warning  string
}

// PlaceHost picks a host with capacity; an empty Pick.HostID means no
// candidate. With gpuWanted it prefers GPU-capable hosts and, unless the GPU
// is a hard requirement, degrades to a CPU host with a warning.
func (r *Resolver) PlaceHost(ctx context.Context, gpuWanted, gpuRequired bool, excluded []string, prefs Prefs) (Pick, error) {
	if gpuWanted {
		id, warm, err := r.capacity.PickHostForPlacement(ctx, true, excluded, prefs)
		if err != nil {
			return Pick{}, fmt.Errorf("placing on a GPU host: %w", err)
		}
		if id != "" || gpuRequired {
			// gpuRequired with no candidate falls through to provisioning,
			// which either provisions a GPU shape or fails.
			return Pick{HostID: id, Warm: warm}, nil
		}
	}
	id, warm, err := r.capacity.PickHostForPlacement(ctx, false, excluded, prefs)
	if err != nil {
		return Pick{}, fmt.Errorf("placing device: %w", err)
	}
	pick := Pick{HostID: id, Warm: warm}
	if id != "" && gpuWanted {
		pick.Degraded = true
		pick.Warning = "no GPU capacity available; placed on a CPU host (software rendering)"
	}
	return pick, nil
}

// PlaceableCapacity is the batch pack planner's fleet view, passed through
// from the capacity store.
func (r *Resolver) PlaceableCapacity(ctx context.Context, gpuOnly bool, prefs Prefs) ([]batch.HostSlot, error) {
	slots, err := r.capacity.ListPlaceableCapacity(ctx, gpuOnly, prefs)
	if err != nil {
		return nil, fmt.Errorf("listing placeable capacity: %w", err)
	}
	return slots, nil
}

// maxWarmKeys bounds the warm plane's plan: the default build plus a handful
// of hot pinned builds. At ~3-4 GB per bundle, more would just churn the
// host-side 25 GB cache's FIFO eviction.
const maxWarmKeys = 8

// WarmKeys plans the warm plane's prefetch: the default build's fetch key
// first (the key a build-less declarative create resolves to, tracking or
// pinned as the registry has it), then the distinct pinned keys behind
// recentBuildIDs. A build id that no longer resolves, or resolves tracking,
// simply contributes nothing: warming is an optimization and must never fail
// a keeper cycle over one stale id.
func (r *Resolver) WarmKeys(ctx context.Context, recentBuildIDs []string) ([]build.WarmKey, error) {
	var keys []build.WarmKey
	seen := make(map[string]bool)
	add := func(k build.WarmKey) {
		if k.FetchKey != "" && !seen[k.FetchKey] && len(keys) < maxWarmKeys {
			seen[k.FetchKey] = true
			keys = append(keys, k)
		}
	}

	if b, found, err := r.defaultBuild(ctx); err != nil {
		return nil, err
	} else if found {
		add(warmKeyFor(b))
	} else {
		branch, target, err := r.upstreamDefault(ctx)
		if err != nil {
			return nil, err
		}
		if branch == "" || target == "" {
			branch, target = fallbackUpstreamBranch, fallbackUpstreamTarget
		}
		add(build.WarmKey{FetchKey: build.FetchKey(branch, target)})
	}

	for _, id := range recentBuildIDs {
		b, err := r.builds.Get(ctx, id)
		if err != nil {
			continue
		}
		if k := warmKeyFor(b); k.Pinned {
			add(k)
		}
	}
	return keys, nil
}

// warmKeyFor renders a registered build as a warm key: pinned when its
// residency is ledgerable, tracking otherwise (branch-keyed AB builds), and
// empty for anything unfetchable.
func warmKeyFor(b *build.Build) build.WarmKey {
	if key, ok := b.PinnedFetchKey(); ok {
		return build.WarmKey{FetchKey: key, RegistryID: b.ID, Pinned: true}
	}
	if b.Source == build.SourceAB && b.Branch != "" && b.Target != "" {
		return build.WarmKey{FetchKey: build.FetchKey(b.Branch, b.Target)}
	}
	return build.WarmKey{}
}

// AffineFetchKey resolves the pinned fetch key placement's affinity term
// orders by. It is empty for tracking builds (their cached artifacts may be
// stale the moment latest advances), URL builds, unregistered references, and
// any lookup failure. Affinity is an ordering optimization and must never fail
// or filter a create.
func (r *Resolver) AffineFetchKey(ctx context.Context, buildID string) string {
	if buildID == "" {
		return ""
	}
	b, err := r.builds.Get(ctx, buildID)
	if err != nil {
		return ""
	}
	key, _ := b.PinnedFetchKey()
	return key
}
