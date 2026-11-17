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

// Error-path coverage for the host image config service.
//
// Yuan et al. found that 92% of catastrophic failures in distributed systems
// came from incorrectly handled non-fatal errors, and that over half were
// reachable by simple tests of the error-handling code. This slice was the
// clearest instance of that gap in the module: 60 of its 62 error branches
// were unexecuted. Every dependency here is an interface, so the branches need
// injection rather than infrastructure.
//
// The assertions are on propagation and on what a failure must NOT do -- a
// failed validation must not reach the repository, and a failed dependency
// must surface rather than be swallowed into a success.

package hostconfigfeature_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"

	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	hostconfigfeature "cloud-cuttlefish-orchestrator/internal/features/hostconfig"
	cspanner "cloud-cuttlefish-orchestrator/internal/platform/spanner"
	"cloud-cuttlefish-orchestrator/internal/platform/spanner/models"
)

var errBoom = errors.New("boom")

// failingRepo returns errBoom from whichever methods are switched on, and
// records every call so a test can assert a call was never made.
type failingRepo struct {
	getErr, listErr, countErr, createErr, updateErr, deleteErr bool
	calls                                                      []string
}

func (r *failingRepo) record(name string) { r.calls = append(r.calls, name) }

func (r *failingRepo) called(name string) bool { return slices.Contains(r.calls, name) }

func (r *failingRepo) Get(_ context.Context, _ string) (*hostconfig.HostImageConfig, error) {
	r.record("Get")
	if r.getErr {
		return nil, errBoom
	}
	return &hostconfig.HostImageConfig{MachineFamily: "n1"}, nil
}

func (r *failingRepo) List(_ context.Context, _ hostconfig.ListFilter) ([]*hostconfig.HostImageConfig, error) {
	r.record("List")
	if r.listErr {
		return nil, errBoom
	}
	return nil, nil
}

func (r *failingRepo) Count(_ context.Context, _ hostconfig.ListFilter) (int64, error) {
	r.record("Count")
	if r.countErr {
		return 0, errBoom
	}
	return 0, nil
}

func (r *failingRepo) Create(_ context.Context, _ *hostconfig.HostImageConfig) error {
	r.record("Create")
	if r.createErr {
		return errBoom
	}
	return nil
}

func (r *failingRepo) Update(_ context.Context, _ *hostconfig.HostImageConfig) error {
	r.record("Update")
	if r.updateErr {
		return errBoom
	}
	return nil
}

func (r *failingRepo) Delete(_ context.Context, _ string) error {
	r.record("Delete")
	if r.deleteErr {
		return errBoom
	}
	return nil
}

var _ hostconfig.Repository = (*failingRepo)(nil)

// failingSettings is the settings store these tests do not exercise.
type failingSettings struct{}

func (s *failingSettings) Get(_ context.Context, key string) (*models.SystemConfig, error) {
	return &models.SystemConfig{Key: key, Value: "configured-project"}, nil
}

func (s *failingSettings) Set(_ context.Context, _, _, _ string) error { return nil }

func (s *failingSettings) SetMany(_ context.Context, _ []cspanner.ConfigEntry) error { return nil }

func (s *failingSettings) Fingerprint(_ context.Context, _ []string) (string, error) { return "", nil }

func (s *failingSettings) SetManyIfMatch(_ context.Context, _ []string, _ string, _ []cspanner.ConfigEntry) (string, error) {
	return "", nil
}

// failingImages resolves image families, refusing every one while err is set,
// and records the project it was asked about so the project rule can be
// observed rather than inferred.
type failingImages struct {
	err         bool
	lastProject string
	timesCalled int
}

func (i *failingImages) ResolveFamily(_ context.Context, project, family string) (*hostconfig.Image, error) {
	i.timesCalled++
	i.lastProject = project
	if i.err {
		return nil, &hostconfig.ImageUnavailableError{ImageFamily: family, Project: project, Reason: hostconfig.ImageNotFound}
	}
	return &hostconfig.Image{
		SelfLink: "https://www.googleapis.com/compute/v1/projects/" + project + "/global/images/" + family + "-1",
		Name:     family + "-1", Family: family, Project: project,
	}, nil
}

// deploymentProject stands in for CCO_HOST_IMAGE_PROJECT.
const deploymentProject = "deploy-project"

func newService(repo hostconfig.Repository, settings hostconfigfeature.SettingsStore, images hostconfig.ImageSource) *hostconfigfeature.Service {
	return hostconfigfeature.NewService(repo, settings,
		hostconfigfeature.Images{Source: images, Project: deploymentProject}, nil, slog.New(slog.DiscardHandler), "bucket")
}

func validConfig() *hostconfig.HostImageConfig {
	return &hostconfig.HostImageConfig{
		MachineFamily: "n1",
		ImageFamily:   "cco-host",
		SourceProject: "some-project",
		Enabled:       true,
	}
}

// Each repository method's failure must reach the caller. List is the one that
// makes two calls, so it gets both.
func TestRepositoryFailuresPropagate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		repo *failingRepo
		call func(*hostconfigfeature.Service) error
	}{
		{"List", &failingRepo{listErr: true}, func(s *hostconfigfeature.Service) error {
			_, _, err := s.List(t.Context(), hostconfig.ListFilter{})
			return err
		}},
		{"Count", &failingRepo{countErr: true}, func(s *hostconfigfeature.Service) error {
			_, _, err := s.List(t.Context(), hostconfig.ListFilter{})
			return err
		}},
		{"Get", &failingRepo{getErr: true}, func(s *hostconfigfeature.Service) error {
			_, err := s.Get(t.Context(), "n1")
			return err
		}},
		{"Create", &failingRepo{createErr: true}, func(s *hostconfigfeature.Service) error {
			return s.Create(t.Context(), validConfig())
		}},
		{"Update", &failingRepo{updateErr: true}, func(s *hostconfigfeature.Service) error {
			return s.Update(t.Context(), validConfig())
		}},
		{"Delete", &failingRepo{deleteErr: true}, func(s *hostconfigfeature.Service) error {
			return s.Delete(t.Context(), "n1")
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := newService(tc.repo, &failingSettings{}, &failingImages{})
			err := tc.call(svc)
			if err == nil {
				t.Fatalf("%s failed in the repository but the service reported success", tc.name)
			}
			if !errors.Is(err, errBoom) {
				t.Errorf("error does not wrap the cause with %%w: %v", err)
			}
		})
	}
}

// A rejected input must not reach storage. Without this, validation could be
// deleted and the repository-failure tests above would still pass.
func TestValidationFailuresNeverReachTheRepository(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		forbid string
		call   func(*hostconfigfeature.Service) error
	}{
		{"Get", "Get", func(s *hostconfigfeature.Service) error {
			_, err := s.Get(t.Context(), "")
			return err
		}},
		{"Delete", "Delete", func(s *hostconfigfeature.Service) error {
			return s.Delete(t.Context(), "NOT VALID")
		}},
		{"Create", "Create", func(s *hostconfigfeature.Service) error {
			c := validConfig()
			c.MachineFamily = ""
			return s.Create(t.Context(), c)
		}},
		{"Update", "Update", func(s *hostconfigfeature.Service) error {
			c := validConfig()
			c.ImageFamily = ""
			return s.Update(t.Context(), c)
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := &failingRepo{}
			svc := newService(repo, &failingSettings{}, &failingImages{})

			if err := tc.call(svc); err == nil {
				t.Fatal("invalid input was accepted")
			}
			if repo.called(tc.forbid) {
				t.Errorf("%s reached the repository despite failing validation", tc.forbid)
			}
		})
	}
}

// An enabled row whose image does not resolve must block the write, not warn
// past it: a config naming an image family that does not exist provisions
// hosts that cannot boot.
func TestImageResolutionFailureBlocksWrites(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"Create", "Update"} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			repo := &failingRepo{}
			svc := newService(repo, &failingSettings{}, &failingImages{err: true})

			var err error
			if verb == "Create" {
				err = svc.Create(t.Context(), validConfig())
			} else {
				err = svc.Update(t.Context(), validConfig())
			}
			unavailable, ok := errors.AsType[*hostconfig.ImageUnavailableError](err)
			if !ok {
				t.Fatalf("want an ImageUnavailableError, got %v", err)
			}
			if unavailable.MachineFamily != "n1" || unavailable.Project != "some-project" {
				t.Errorf("refusal = %+v, want it to name n1 and some-project", unavailable)
			}
			if repo.called(verb) {
				t.Errorf("%s wrote a config whose image family does not exist", verb)
			}
		})
	}
}

// A disabled row provisions nothing, so it is stored without a lookup.
func TestDisabledRowIsStoredWithoutALookup(t *testing.T) {
	t.Parallel()
	repo := &failingRepo{}
	images := &failingImages{err: true}
	svc := newService(repo, &failingSettings{}, images)

	cfg := validConfig()
	cfg.Enabled = false
	if err := svc.Create(t.Context(), cfg); err != nil {
		t.Fatalf("a disabled row must be stored unchecked: %v", err)
	}
	if images.timesCalled != 0 {
		t.Errorf("image source called %d times for a disabled row", images.timesCalled)
	}
	if cfg.Resolved == nil || cfg.Resolved.Reason != hostconfig.ImageNoConfig {
		t.Errorf("resolved = %+v, want no_config", cfg.Resolved)
	}
}

// A row with no source_project resolves in the deployment's project. Asserting
// the project proves the rule ran, which a bare "no error" cannot.
func TestEmptySourceProjectResolvesInTheDeploymentProject(t *testing.T) {
	t.Parallel()
	images := &failingImages{}
	svc := newService(&failingRepo{}, &failingSettings{}, images)

	cfg := validConfig()
	cfg.SourceProject = ""
	if err := svc.Create(t.Context(), cfg); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if images.lastProject != deploymentProject {
		t.Errorf("resolved in %q, want the deployment project %q", images.lastProject, deploymentProject)
	}
	if cfg.Resolved == nil || !cfg.Resolved.OK || cfg.Resolved.Project != deploymentProject {
		t.Errorf("resolved = %+v, want ok in %s", cfg.Resolved, deploymentProject)
	}
}
