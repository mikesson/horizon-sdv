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

package hostconfigfeature

import (
	"context"
	"fmt"
	"log/slog"

	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

// SettingsStore reads and writes the operator-tunable system_config keys
// behind the admin settings surface. Satisfied by the platform
// SystemConfigRepository.
//
// SetMany is what every write of more than one key goes through: the groups
// here are read back as one fact, so a half-applied group is a policy nobody
// wrote. Set stays for the single-key writes, where the two are the same thing.
type SettingsStore interface {
	Get(ctx context.Context, key string) (*models.SystemConfig, error)
	Set(ctx context.Context, key, value, description string) error
	SetMany(ctx context.Context, entries []spanner.ConfigEntry) error
	// Fingerprint and SetManyIfMatch back the settings entity tags.
	Fingerprint(ctx context.Context, keys []string) (string, error)
	SetManyIfMatch(ctx context.Context, keys []string, ifMatch string, entries []spanner.ConfigEntry) (string, error)
}

// MachineCatalog is the catalog reads this slice makes. The default machine
// type is what an auto-provisioned host is built from unless a create's shape
// demands otherwise, and the packing density is an operator policy about that
// machine, so it cannot be reported (or usefully accepted) without it. The
// whole list is what a shape ladder's rungs are admitted against: the catalog
// is the only authority on which shapes can run Cuttlefish here, and a rung
// that is present but disabled has to be distinguishable from one that is
// absent.
//
// Get is the refusal's own vocabulary. A rung missing from the listing is out
// for one of three reasons with three different remedies, and only the catalog
// can say which; it is asked about the one rung a write is about to refuse,
// never per rung.
//
// internal/app wires the concrete repository in, so no feature imports a
// feature.
type MachineCatalog interface {
	GetDefault(ctx context.Context) (*machinetype.MachineType, error)
	List(ctx context.Context, filter machinetype.ListFilter) ([]*machinetype.MachineType, error)
	Get(ctx context.Context, id string) (*machinetype.MachineType, error)
}

// Service provides business logic for host image configuration management.
type Service struct {
	repo         hostconfig.Repository
	settings     SettingsStore
	images       *imageResolver
	machineTypes MachineCatalog
	logger       *slog.Logger

	// defaultArtifactsBucket is the deployment default (CCO_ARTIFACTS_BUCKET)
	// the per-type artifact bucket overrides fall back to.
	defaultArtifactsBucket string
}

// Images is where host image families resolve: the GCE lookup and the
// deployment's host image project (CCO_HOST_IMAGE_PROJECT), which a row
// without its own source_project resolves in.
type Images struct {
	// Source looks image families up; nil fails every resolution.
	Source  hostconfig.ImageSource
	Project string
}

// NewService creates a new host image config service. A nil machine catalog
// leaves the packing density unjudged, so it reports and accepts the
// operator's number verbatim, refuses every shape-ladder write, and leaves the
// image architecture unchecked.
// defaultArtifactsBucket is the deployment default (CCO_ARTIFACTS_BUCKET) the
// admin surface reports as the fallback for per-type artifact buckets.
func NewService(
	repo hostconfig.Repository,
	settings SettingsStore,
	images Images,
	machineTypes MachineCatalog,
	logger *slog.Logger,
	defaultArtifactsBucket string,
) *Service {
	return &Service{
		repo:                   repo,
		settings:               settings,
		images:                 newImageResolver(repo, images.Source, images.Project),
		machineTypes:           machineTypes,
		logger:                 logger,
		defaultArtifactsBucket: defaultArtifactsBucket,
	}
}

// List retrieves all host image configurations.
func (s *Service) List(ctx context.Context, filter hostconfig.ListFilter) ([]*hostconfig.HostImageConfig, int64, error) {
	configs, err := s.repo.List(ctx, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("listing host image configs: %w", err)
	}

	total, err := s.repo.Count(ctx, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("counting host image configs: %w", err)
	}

	archs := s.familyArchitectures(ctx)
	for _, config := range configs {
		s.attachStatus(ctx, config, archs[config.MachineFamily])
	}
	return configs, total, nil
}

// Get retrieves a host image configuration by machine family.
func (s *Service) Get(ctx context.Context, machineFamily string) (*hostconfig.HostImageConfig, error) {
	if err := hostconfig.ValidateMachineFamily(machineFamily); err != nil {
		return nil, fmt.Errorf("validating machine family: %w", err)
	}

	config, err := s.repo.Get(ctx, machineFamily)
	if err != nil {
		return nil, fmt.Errorf("getting host image config: %w", err)
	}

	s.attachStatus(ctx, config, s.familyArchitectures(ctx)[machineFamily])
	return config, nil
}

// Create creates a new host image configuration.
func (s *Service) Create(ctx context.Context, config *hostconfig.HostImageConfig) error {
	if err := hostconfig.ValidateHostImageConfig(config); err != nil {
		return fmt.Errorf("validating host image config: %w", err)
	}

	if err := s.checkImage(ctx, config); err != nil {
		return err
	}

	if err := s.repo.Create(ctx, config); err != nil {
		return fmt.Errorf("creating host image config: %w", err)
	}
	s.images.forget(config.MachineFamily)

	return nil
}

// Update updates an existing host image configuration.
func (s *Service) Update(ctx context.Context, config *hostconfig.HostImageConfig) error {
	if err := hostconfig.ValidateHostImageConfig(config); err != nil {
		return fmt.Errorf("validating host image config: %w", err)
	}

	if err := s.checkImage(ctx, config); err != nil {
		return err
	}

	if err := s.repo.Update(ctx, config); err != nil {
		return fmt.Errorf("updating host image config: %w", err)
	}
	s.images.forget(config.MachineFamily)

	return nil
}

// Delete deletes a host image configuration.
func (s *Service) Delete(ctx context.Context, machineFamily string) error {
	if err := hostconfig.ValidateMachineFamily(machineFamily); err != nil {
		return fmt.Errorf("validating machine family: %w", err)
	}

	if err := s.repo.Delete(ctx, machineFamily); err != nil {
		return fmt.Errorf("deleting host image config: %w", err)
	}
	s.images.forget(machineFamily)

	return nil
}

// ResolveImage answers which concrete image boots machineFamily on a machine
// of arch, or an *hostconfig.ImageUnavailableError saying why none does. It is
// the one resolution the host create preflight, the provision task, the arm
// readiness probe and the admin status all read. Successes are cached briefly
// per replica; refusals never are.
func (s *Service) ResolveImage(ctx context.Context, machineFamily string, arch machinetype.Architecture) (*hostconfig.Image, error) {
	return s.images.resolve(ctx, machineFamily, arch)
}

// checkImage refuses a write of an enabled row whose image cannot boot the
// family. A disabled row provisions nothing, so it is stored unchecked, and
// enabling it later is a write that runs this check.
func (s *Service) checkImage(ctx context.Context, config *hostconfig.HostImageConfig) error {
	if !config.Enabled {
		config.Resolved = disabledStatus(config, s.images.project)
		return nil
	}
	img, err := s.images.check(ctx, config, s.familyArchitectures(ctx)[config.MachineFamily])
	if err != nil {
		return err
	}
	config.Resolved = hostconfig.StatusOf(img, nil, hostconfig.EffectiveProject(config, s.images.project))
	return nil
}

// attachStatus fills a row's resolved block from the same resolution a host
// create runs.
func (s *Service) attachStatus(ctx context.Context, config *hostconfig.HostImageConfig, arch machinetype.Architecture) {
	if !config.Enabled {
		config.Resolved = disabledStatus(config, s.images.project)
		return
	}
	img, err := s.images.resolve(ctx, config.MachineFamily, arch)
	config.Resolved = hostconfig.StatusOf(img, err, hostconfig.EffectiveProject(config, s.images.project))
}

// disabledStatus is the status of a row that provisions nothing.
func disabledStatus(config *hostconfig.HostImageConfig, deploymentProject string) *hostconfig.ImageStatus {
	return hostconfig.StatusOf(nil, &hostconfig.ImageUnavailableError{
		MachineFamily: config.MachineFamily, Reason: hostconfig.ImageNoConfig,
	}, hostconfig.EffectiveProject(config, deploymentProject))
}

// familyArchitectures maps each machine family the catalog serves to its CPU
// architecture. A family the catalog does not serve, or a catalog that cannot
// be read, is absent, which leaves its architecture unchecked.
func (s *Service) familyArchitectures(ctx context.Context) map[string]machinetype.Architecture {
	archs := map[string]machinetype.Architecture{}
	if s.machineTypes == nil {
		return archs
	}
	shapes, err := s.machineTypes.List(ctx, machinetype.ListFilter{})
	if err != nil {
		s.logger.Warn("machine catalog unavailable; image architecture unchecked", slog.Any("error", err))
		return archs
	}
	for _, shape := range shapes {
		if shape.Architecture != "" {
			archs[shape.Family] = shape.Architecture
		}
	}
	return archs
}
