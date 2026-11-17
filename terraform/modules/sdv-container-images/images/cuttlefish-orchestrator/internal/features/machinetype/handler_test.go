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

package machinetypefeature

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

type fakeMachineTypeRepo struct {
	machinetype.Repository // panic on unstubbed calls

	listFilter     *machinetype.ListFilter
	families       []*machinetype.Family
	familyCatalogs []*machinetype.FamilyCatalog
	getResult      *machinetype.MachineType
	adminID        string
	adminUpdate    *machinetype.AdminUpdate
}

func (f *fakeMachineTypeRepo) ListFamilyCatalog(context.Context) ([]*machinetype.FamilyCatalog, error) {
	return f.familyCatalogs, nil
}

func (f *fakeMachineTypeRepo) Get(ctx context.Context, id string) (*machinetype.MachineType, error) {
	if f.getResult != nil {
		return f.getResult, nil
	}
	return &machinetype.MachineType{ID: id, Family: "n1", IsEnabled: true}, nil
}

func (f *fakeMachineTypeRepo) List(ctx context.Context, filter machinetype.ListFilter) ([]*machinetype.MachineType, error) {
	f.listFilter = &filter
	return []*machinetype.MachineType{{ID: "n1-standard-8", Family: "n1"}}, nil
}

func (f *fakeMachineTypeRepo) Count(ctx context.Context, filter machinetype.ListFilter) (int64, error) {
	return 1, nil
}

func (f *fakeMachineTypeRepo) ListFamilies(ctx context.Context) ([]*machinetype.Family, error) {
	return f.families, nil
}

func (f *fakeMachineTypeRepo) UpdateAdminSettings(ctx context.Context, id string, update machinetype.AdminUpdate) (*machinetype.MachineType, error) {
	f.adminID = id
	f.adminUpdate = &update
	return &machinetype.MachineType{ID: id, Family: "n1"}, nil
}

func newTestAPI(repo machinetype.Repository) (huma.API, chi.Router) {
	_, api, r := newTestAPIWithRefresher(repo, nil)
	return api, r
}

// fakeRefresher records the rediscovery a curation write triggers.
type fakeRefresher struct {
	calls int
	err   error
}

func (f *fakeRefresher) RefreshStructural(context.Context) error {
	f.calls++
	return f.err
}

// newTestAPIWithRefresher wires the handler over a recording refresher, so a
// test can prove that curation reaches it.
func newTestAPIWithRefresher(repo machinetype.Repository, refresher ShapeRefresher) (*MachineTypeHandler, huma.API, chi.Router) {
	r := chi.NewRouter()
	api := humachi.New(r, huma.DefaultConfig("Test API", "1.0.0"))
	h := NewMachineTypeHandler(repo, nil, nil)
	h.SetShapeRefresher(refresher)
	h.Register(api)
	return h, api, r
}

func TestListMachineTypesFilters(t *testing.T) {
	t.Parallel()
	repo := &fakeMachineTypeRepo{}
	_, router := newTestAPI(repo)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v2/machine-types?gpu_support=true&zone=us-central1-a", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if repo.listFilter == nil {
		t.Fatal("List was not called")
	}
	if repo.listFilter.GPUSupport == nil || !*repo.listFilter.GPUSupport {
		t.Errorf("GPUSupport filter = %v, want true", repo.listFilter.GPUSupport)
	}
	if repo.listFilter.IsEnabled != nil {
		t.Errorf("IsEnabled filter = %v, want nil when the enabled param is omitted", repo.listFilter.IsEnabled)
	}
	if repo.listFilter.Zone == nil || *repo.listFilter.Zone != "us-central1-a" {
		t.Errorf("Zone filter = %v, want us-central1-a", repo.listFilter.Zone)
	}
}

func TestListMachineFamilies(t *testing.T) {
	t.Parallel()
	repo := &fakeMachineTypeRepo{
		families: []*machinetype.Family{
			{
				Family: "n1", Name: "N1 General Purpose",
				Category: machinetype.CategoryGeneralPurpose, Architecture: machinetype.ArchX86,
				GPUSupport: true, TypeCount: 3,
			},
		},
	}
	_, router := newTestAPI(repo)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v2/machine-families", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Families []FamilySummary `json:"families"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Families) != 1 {
		t.Fatalf("families = %d, want 1", len(body.Families))
	}
	got := body.Families[0]
	if got.Family != "n1" || got.Name != "N1 General Purpose" || !got.GPUSupport || got.TypeCount != 3 {
		t.Errorf("family summary = %+v", got)
	}
}

// The curation listing carries a family the fleet does not offer, with the
// operator's own facts about it. The catalog's family listing cannot: it
// aggregates the entries composed for callers, and a withdrawn family has
// none, so the row an operator needs the switch on would be the one row
// missing.
func TestListMachineFamiliesAdminCarriesAWithdrawnFamily(t *testing.T) {
	t.Parallel()
	repo := &fakeMachineTypeRepo{
		familyCatalogs: []*machinetype.FamilyCatalog{{
			Curation: &machinetype.FamilyCuration{
				Family: "c4a", Category: machinetype.CategoryGeneralPurpose,
				Architecture: machinetype.ArchArm, CPUType: "Google Axion",
				BootDiskType: "hyperdisk-balanced", GPUMode: machinetype.GPUModeNone,
				IsEnabled: false, SortOrder: 20,
			},
			Shapes: []machinetype.FamilyShape{
				{MachineType: "c4a-highcpu-2", VCPUs: 2, MemoryGB: 4},
				{MachineType: "c4a-standard-48-metal", VCPUs: 48, MemoryGB: 192, Fit: 23, Viable: true, IsDefault: true},
			},
			Viable: 1,
		}},
	}
	_, router := newTestAPI(repo)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v2/admin/machine-families", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Families []AdminFamily `json:"families"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Families) != 1 {
		t.Fatalf("families = %d, want the withdrawn one", len(body.Families))
	}
	got := body.Families[0]
	for _, check := range []struct{ what, have, want string }{
		{"name", got.Name, "C4A General Purpose"},
		{"architecture", got.Architecture, "arm"},
		{"cpu type", got.CPUType, "Google Axion"},
		{"boot disk type", got.BootDiskType, "hyperdisk-balanced"},
	} {
		if check.have != check.want {
			t.Errorf("%s = %q, want %q", check.what, check.have, check.want)
		}
	}
	if got.IsEnabled {
		t.Error("the withdrawn family reports as enabled")
	}
	if got.ShapeCount != 2 || got.ViableCount != 1 {
		t.Errorf("counts = %d shapes, %d viable; want 2 and 1", got.ShapeCount, got.ViableCount)
	}

	if len(got.Shapes) != 2 {
		t.Fatalf("shapes = %d, want both reported", len(got.Shapes))
	}
	if got.Shapes[0].Viable || got.Shapes[0].Fit != 0 {
		t.Errorf("the below-floor shape reports viable %v holding %d", got.Shapes[0].Viable, got.Shapes[0].Fit)
	}
	if metal := got.Shapes[1]; metal.Fit != 23 || !metal.IsDefault {
		t.Errorf("the metal shape = fit %d, default %v; want the server's fit and the election", metal.Fit, metal.IsDefault)
	}
}

func TestUpdateMachineTypeAdmin(t *testing.T) {
	t.Parallel()
	repo := &fakeMachineTypeRepo{}
	_, router := newTestAPI(repo)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPatch,
		"/v2/admin/machine-types/n1-standard-8", strings.NewReader(`{"is_enabled":false,"max_devices":3}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if repo.adminID != "n1-standard-8" {
		t.Errorf("id = %q", repo.adminID)
	}
	if repo.adminUpdate == nil || repo.adminUpdate.IsEnabled == nil || *repo.adminUpdate.IsEnabled {
		t.Errorf("IsEnabled = %v, want false", repo.adminUpdate)
	}
	if repo.adminUpdate.MaxDevices == nil || *repo.adminUpdate.MaxDevices != 3 {
		t.Errorf("MaxDevices = %v, want 3", repo.adminUpdate.MaxDevices)
	}
	if repo.adminUpdate.IsDefault != nil {
		t.Errorf("IsDefault = %v, want nil when omitted", repo.adminUpdate.IsDefault)
	}
}

// Display order and GPU policy are curated on the family, and the per-shape
// PATCH no longer declares them. Huma refuses a body key no input field
// declares, so each one answers 422 before the handler runs. Re-adding any of
// these fields to the input makes its case fail, because the request would then
// reach the handler and the repository.
func TestUpdateMachineTypeAdminRefusesFamilyFields(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{"sort_order":7}`,
		`{"max_gpus":2}`,
		`{"default_gpu_type":"nvidia-tesla-t4"}`,
		`{"default_gpu_count":1}`,
		`{"is_enabled":true,"sort_order":7}`,
	} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			repo := &fakeMachineTypeRepo{}
			_, router := newTestAPI(repo)

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPatch,
				"/v2/admin/machine-types/n1-standard-8", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (body: %s)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "unexpected property") {
				t.Errorf("refusal = %s, want Huma's unexpected-property error", rec.Body.String())
			}
			if repo.adminUpdate != nil {
				t.Error("a refused patch reached the repository")
			}
		})
	}
}

func TestUpdateMachineTypeAdminRejections(t *testing.T) {
	t.Parallel()
	repo := &fakeMachineTypeRepo{}
	_, router := newTestAPI(repo)

	repo.getResult = &machinetype.MachineType{ID: "n1-standard-8", Family: "n1", IsDefault: true, IsEnabled: true}
	for name, body := range map[string]string{
		"empty patch":              `{}`,
		"clearing default":         `{"is_default":false}`,
		"disabling the default":    `{"is_enabled":false}`,
		"electing while disabling": `{"is_default":true,"is_enabled":false}`,
	} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPatch,
			"/v2/admin/machine-types/n1-standard-8", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body: %s)", name, rec.Code, rec.Body.String())
		}
		if repo.adminUpdate != nil {
			t.Errorf("%s: repository was called", name)
		}
	}
}

// A curation write rediscovers Compute Engine's shapes before it answers.
//
// Without it, a shape an operator just admitted stays undiscovered for up to a
// structural refresh interval (six hours), and every create naming it is
// refused by a catalog that has never heard of it. The refusal blamed zone
// capacity, which is the wrong cause, so this test asserts the
// wiring: drop SetShapeRefresher, or the call in the handler, and it goes
// red.
func TestCurationWritesRediscoverShapes(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		path string
		body string
	}{
		"a per-shape curation write": {
			path: "/v2/admin/machine-types/n1-standard-8",
			body: `{"is_enabled":true}`,
		},
	}
	for name, tc := range cases {
		refresher := &fakeRefresher{}
		_, _, router := newTestAPIWithRefresher(&fakeMachineTypeRepo{}, refresher)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body: %s", name, rec.Code, rec.Body.String())
		}
		if refresher.calls != 1 {
			t.Errorf("%s: rediscovery calls = %d, want 1", name, refresher.calls)
		}
	}
}

// A rediscovery that failed must not fail the write. The curation is committed
// and correct; reporting a Compute Engine hiccup as a failed PATCH would
// invite the operator to repeat a change that already landed.
func TestCurationWriteSurvivesAFailedRediscovery(t *testing.T) {
	t.Parallel()
	refresher := &fakeRefresher{err: errors.New("compute engine is unreachable")}
	_, _, router := newTestAPIWithRefresher(&fakeMachineTypeRepo{}, refresher)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPatch,
		"/v2/admin/machine-types/n1-standard-8", strings.NewReader(`{"is_enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 despite the failed rediscovery (body: %s)", rec.Code, rec.Body.String())
	}
	if refresher.calls != 1 {
		t.Errorf("rediscovery calls = %d, want 1", refresher.calls)
	}
}
