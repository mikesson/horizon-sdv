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

package gcecatalogfeature

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"cloud-cuttlefish-orchestrator/internal/domain/gcecatalog"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// GPUMode constants for machine type GPU classification (aliases of the
// domain source of truth so the two stay in lockstep).
const (
	GPUModeNone       = machinetype.GPUModeNone
	GPUModeAttachable = machinetype.GPUModeAttachable
	GPUModeIntegrated = machinetype.GPUModeIntegrated
)

// Request field names, doubling as log/metric attribute keys and the zone
// curation audit kind (goconst).
const (
	attrZone        = "zone"
	attrMachineType = "machine_type"
	attrGPUType     = "gpu_type"
	attrGPUCount    = "gpu_count"
)

// ValidatorConfig holds configuration for the catalog validator.
type ValidatorConfig struct {
	// UseExternalIP marks a deployment whose hosts take external addresses, so
	// zone ranking counts an IN_USE_ADDRESSES demand per host.
	UseExternalIP bool

	// QuotaStaleThreshold is how old a quota row from the Compute Engine
	// snapshot may be before ranking stops trusting it and passes the region
	// advisorily (typically twice the quota refresh interval; 0 = a compiled
	// default).
	QuotaStaleThreshold time.Duration

	// SpotAdviceStaleThreshold is how old a synced spot_advice cell may be
	// before ranking stops consulting it (typically a few multiples of the
	// advice sync interval; 0 = a compiled default).
	SpotAdviceStaleThreshold time.Duration

	// ShapeStaleThreshold is how old the Compute Engine shape reading may be
	// before a refusal stops claiming to be about the zone (typically twice
	// the structural refresh interval; 0 = a compiled default). It changes what
	// a refusal SAYS, never whether one happens: an old reading is still the
	// only reading there is.
	ShapeStaleThreshold time.Duration
}

// EnabledZones is the durable half of the catalog the validator and ranker
// consult: the zones an operator has admitted for host creation. The
// ZonePolicy in this package satisfies it.
type EnabledZones interface {
	ListEnabledZones(ctx context.Context) ([]string, error)
}

// ShapeCatalog is what Compute Engine offers, read from the in-process
// snapshot the platform keeps: every zone it offers the project with its
// status, three-state resource availability (a zone Compute Engine never
// reported is not a zone that offers nothing), the regional quota rows
// placement judges headroom on, and how current the whole reading is. Every
// answer is a map lookup, so none of them can fail. Absence is an answer
// here, not an error.
type ShapeCatalog interface {
	Zones() map[string]gcecatalog.ZoneStatus
	MachineTypeAvailability(zone, machineType string) gcecatalog.Availability
	AcceleratorAvailability(zone, acceleratorType string) gcecatalog.Availability
	ZoneAccelerators(zone string) []*gcecatalog.Accelerator
	RegionalQuota(region, quotaType string) *gcecatalog.RegionalQuota
	RegionalQuotas(region string) []*gcecatalog.RegionalQuota
	BuiltAt() time.Time
	Partial() []string
}

// CatalogValidator validates host creation requests against the GCE catalog.
// It is the admission half of the catalog brain. Placement ranking lives on its
// ZoneRanker; the validator delegates RankZones (alternative-zone advice, the
// domain port) and shares its quota-judgment engine.
type CatalogValidator struct {
	zones        EnabledZones
	machineTypes machinetype.Catalog
	config       ValidatorConfig
	logger       *slog.Logger
	ranker       *ZoneRanker
}

// ValidatorOption configures a CatalogValidator.
type ValidatorOption func(*CatalogValidator)

// WithZoneHealthStore wires the zone_health memory into ranking.
func WithZoneHealthStore(store gcecatalog.ZoneHealthStore) ValidatorOption {
	return func(v *CatalogValidator) {
		v.ranker.zoneHealth = store
	}
}

// WithPolicyReader wires the operator placement policy (system_config) into
// ranking.
func WithPolicyReader(policy PolicyReader) ValidatorOption {
	return func(v *CatalogValidator) {
		v.ranker.policy = policy
	}
}

// WithSpotAdviceStore wires synced Spot capacity advice into ranking.
func WithSpotAdviceStore(store gcecatalog.SpotAdviceStore) ValidatorOption {
	return func(v *CatalogValidator) {
		v.ranker.spotAdvice = store
	}
}

// WithShapeCatalog wires the Compute Engine snapshot into ranking and
// admission. Unwired, every cell reads as unknown, which removes no zone.
func WithShapeCatalog(shapes ShapeCatalog) ValidatorOption {
	return func(v *CatalogValidator) {
		v.ranker.shapes = shapes
	}
}

// NewCatalogValidator creates the catalog validator and its zone ranker as
// one assembled unit (the two share the catalog reads, configuration, and
// quota engine; main wires a single instance into every consumer).
func NewCatalogValidator(
	zones EnabledZones,
	machineTypes machinetype.Catalog,
	config ValidatorConfig,
	logger *slog.Logger,
	opts ...ValidatorOption,
) *CatalogValidator {
	v := &CatalogValidator{
		zones:        zones,
		machineTypes: machineTypes,
		config:       config,
		logger:       logger,
		ranker: &ZoneRanker{
			zones:        zones,
			machineTypes: machineTypes,
			config:       config,
			logger:       logger,
		},
	}
	for _, opt := range opts {
		opt(v)
	}
	v.ranker.metrics = newPlacementMetrics(v.ranker.zoneHealth)
	return v
}

// Ranker returns the placement half for callers that only rank zones.
func (v *CatalogValidator) Ranker() *ZoneRanker { return v.ranker }

// RankZones implements the domain Validator port by delegating to the ranker.
func (v *CatalogValidator) RankZones(ctx context.Context, req gcecatalog.ValidationRequest, opts gcecatalog.RankOptions) ([]gcecatalog.ZoneCandidate, error) {
	return v.ranker.RankZones(ctx, req, opts)
}

// Validate checks if a host configuration is valid against the GCE catalog.
func (v *CatalogValidator) Validate(ctx context.Context, req gcecatalog.ValidationRequest) (*gcecatalog.ValidationResult, error) {
	result := &gcecatalog.ValidationResult{Valid: true}

	// 0. Validate provisioning model if specified.
	if !v.validateProvisioningModel(req.ProvisioningModel, result) {
		return result, nil
	}

	// 1. Validate zone exists and is enabled.
	ok, err := v.validateZone(ctx, req.Zone, result)
	if err != nil {
		return nil, fmt.Errorf("validating zone: %w", err)
	}
	if !ok {
		// Zone validation failed, find alternatives.
		result.AlternativeZones = v.rankedAlternatives(ctx, req)
		return result, nil
	}

	// 2. Get machine type and validate availability.
	mt, err := v.validateMachineType(ctx, req.Zone, req.MachineType, result)
	if err != nil {
		return nil, fmt.Errorf("validating machine type: %w", err)
	}
	if mt == nil {
		// Machine type validation failed.
		result.AlternativeZones = v.rankedAlternatives(ctx, req)
		return result, nil
	}

	// 3. Validate GPU configuration based on machine type's GPU mode.
	v.validateGPU(req, mt, result)
	if !result.Valid {
		result.AlternativeZones = v.rankedAlternatives(ctx, req)
		return result, nil
	}

	// 4. Validate regional quotas (including Spot VM quotas).
	v.validateQuotas(req, mt, result)

	return result, nil
}

// validateProvisioningModel checks if the provisioning model is valid.
func (v *CatalogValidator) validateProvisioningModel(provisioningModel string, result *gcecatalog.ValidationResult) bool {
	if provisioningModel == "" || provisioningModel == "STANDARD" || provisioningModel == "SPOT" {
		return true
	}

	result.Valid = false
	result.Errors = append(result.Errors, gcecatalog.ValidationError{
		Field:      "provisioning_model",
		Code:       gcecatalog.ErrorCodeInvalidProvisioningModel,
		Message:    fmt.Sprintf("Invalid provisioning model '%s'", provisioningModel),
		Suggestion: "Use 'STANDARD' or 'SPOT'",
	})
	return false
}

// rankedAlternatives returns the ranked viable-zone names for error advice,
// excluding the zone the request failed in. Best-effort: advice never fails
// the validation that wanted it.
func (v *CatalogValidator) rankedAlternatives(ctx context.Context, req gcecatalog.ValidationRequest) []string {
	candidates, err := v.RankZones(ctx, req, gcecatalog.RankOptions{ExcludeZone: req.Zone})
	if err != nil {
		v.logger.Warn("ranking alternative zones failed", slog.Any("error", err))
		return nil
	}
	names := make([]string, 0, len(candidates))
	for _, c := range candidates {
		names = append(names, c.Zone)
	}
	return names
}

// validateZone checks that the zone is one Compute Engine offers, that the
// operator enabled it, and that it is not down. It reports whether the zone
// passed; a non-nil error is an infrastructure failure, never a verdict.
//
// Existence is judged first and only against a snapshot that has landed: with
// no snapshot the name is unverifiable, and refusing an unverifiable name
// would make a cold instance reject every create.
func (v *CatalogValidator) validateZone(ctx context.Context, zoneName string, result *gcecatalog.ValidationResult) (bool, error) {
	universe := v.ranker.zoneUniverse()
	if len(universe) > 0 {
		if _, offered := universe[zoneName]; !offered {
			result.Valid = false
			result.Errors = append(result.Errors, gcecatalog.ValidationError{
				Field:      attrZone,
				Code:       gcecatalog.ErrorCodeZoneNotFound,
				Message:    fmt.Sprintf("Zone '%s' is not available", zoneName),
				Suggestion: "Select a different zone from the available options",
			})
			return false, nil
		}
	}

	enabled, err := v.zones.ListEnabledZones(ctx)
	if err != nil {
		return false, fmt.Errorf("listing enabled zones: %w", err)
	}
	if !slices.Contains(enabled, zoneName) {
		result.Valid = false
		result.Errors = append(result.Errors, gcecatalog.ValidationError{
			Field:      attrZone,
			Code:       gcecatalog.ErrorCodeZoneDisabled,
			Message:    fmt.Sprintf("Zone '%s' is not enabled for host creation", zoneName),
			Suggestion: "Select a different zone from the enabled zones",
		})
		return false, nil
	}

	if universe[zoneName] == gcecatalog.ZoneStatusDown {
		result.Valid = false
		result.Errors = append(result.Errors, gcecatalog.ValidationError{
			Field:      attrZone,
			Code:       gcecatalog.ErrorCodeZoneDown,
			Message:    fmt.Sprintf("Zone '%s' is currently down", zoneName),
			Suggestion: "Select a different zone or try again later",
		})
		return false, nil
	}

	return true, nil
}

// validateMachineType checks if the machine type is available in the zone.
// Returns (nil, nil) if validation failed (errors added to result); a non-nil
// error is an infrastructure failure, never a validation verdict.
//
//nolint:nilnil // Intentional: nil,nil means validation failed, check result.Valid
func (v *CatalogValidator) validateMachineType(ctx context.Context, zoneName, machineTypeName string, result *gcecatalog.ValidationResult) (*machinetype.MachineType, error) {
	// First, get the machine type from our catalog.
	mt, err := v.machineTypes.Get(ctx, machineTypeName)
	if errors.Is(err, machinetype.ErrNotFound) {
		result.Valid = false
		result.Errors = append(result.Errors, gcecatalog.ValidationError{
			Field:      attrMachineType,
			Code:       gcecatalog.ErrorCodeMachineTypeInvalid,
			Message:    fmt.Sprintf("Machine type '%s' is not supported", machineTypeName),
			Suggestion: "Select a supported machine type from the available options",
		})
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("looking up machine type %q: %w", machineTypeName, err)
	}

	if !mt.IsEnabled {
		result.Valid = false
		result.Errors = append(result.Errors, gcecatalog.ValidationError{
			Field:      attrMachineType,
			Code:       gcecatalog.ErrorCodeMachineTypeInvalid,
			Message:    fmt.Sprintf("Machine type '%s' is not enabled", machineTypeName),
			Suggestion: "Select an enabled machine type",
		})
		return nil, nil
	}

	// Check if machine type is available in the zone. An unknown cell (no
	// snapshot yet, or a scope Compute Engine did not report) is not evidence
	// against the zone, so it warns and admits.
	switch v.ranker.shapeAvailability(zoneName, machineTypeName) {
	case gcecatalog.AvailabilityUnknown:
		result.Warnings = append(result.Warnings, gcecatalog.ValidationWarning{
			Code:    gcecatalog.WarningCodeCatalogStale,
			Message: "Could not verify machine type availability in zone",
		})
		return mt, nil
	case gcecatalog.AvailabilityNotOffered:
		result.Valid = false
		result.Errors = append(result.Errors, gcecatalog.ValidationError{
			Field:      attrMachineType,
			Code:       gcecatalog.ErrorCodeMachineTypeNotInZone,
			Message:    v.shapeNotOfferedMessage(machineTypeName, zoneName),
			Suggestion: "Select a different machine type or zone",
		})
		return nil, nil
	case gcecatalog.AvailabilityOffered:
	}

	return mt, nil
}

// shapeNotOfferedMessage says which of the two things went wrong, because they
// have different fixes and used to read the same.
//
// Compute Engine not stocking the shape in that zone is a placement
// fact and the caller changes the request. A reading old enough to predate the
// operator's own curation is CCO's problem, and the sentence has to say so.
// Blaming the zone there sends an operator who just admitted a family to go
// looking at zone capacity, which is the wrong place to look.
func (v *CatalogValidator) shapeNotOfferedMessage(machineTypeName, zoneName string) string {
	age, read := v.ranker.catalogReading()
	if !read {
		return fmt.Sprintf(
			"Machine type '%s' has not been read from Compute Engine yet, so it cannot be placed in zone '%s'",
			machineTypeName, zoneName)
	}
	staleAfter := v.config.ShapeStaleThreshold
	if staleAfter <= 0 {
		staleAfter = defaultShapeStale
	}
	if age > staleAfter {
		return fmt.Sprintf(
			"Machine type '%s' is not in this deployment's reading of zone '%s', but that reading is %s old and may predate the shape",
			machineTypeName, zoneName, age.Round(time.Minute))
	}
	return fmt.Sprintf("Compute Engine does not offer machine type '%s' in zone '%s'", machineTypeName, zoneName)
}

// defaultShapeStale mirrors two structural refresh intervals for a deployment
// whose threshold is not wired through.
const defaultShapeStale = 12 * time.Hour

// validateGPU checks GPU configuration based on machine type's GPU mode.
func (v *CatalogValidator) validateGPU(req gcecatalog.ValidationRequest, mt *machinetype.MachineType, result *gcecatalog.ValidationResult) {
	gpuMode := mt.GPUMode
	if gpuMode == "" {
		gpuMode = GPUModeNone
	}

	switch gpuMode {
	case GPUModeNone:
		validateGPUUnsupported(req, mt, result)
	case GPUModeIntegrated:
		validateGPUIntegrated(req, mt, result)
	case GPUModeAttachable:
		v.validateGPUAttachable(req, mt, result)
	}
}

// validateGPUUnsupported rejects any GPU request against a machine type that
// has no GPU support.
func validateGPUUnsupported(req gcecatalog.ValidationRequest, mt *machinetype.MachineType, result *gcecatalog.ValidationResult) {
	if req.GPUType != "" || req.GPUCount > 0 {
		result.Valid = false
		result.Errors = append(result.Errors, gcecatalog.ValidationError{
			Field:      attrGPUType,
			Code:       gcecatalog.ErrorCodeGPUNotSupported,
			Message:    fmt.Sprintf("Machine type '%s' does not support GPU attachment", mt.ID),
			Suggestion: "Use an N1 machine type for GPU attachment, or a G2/A2 machine with integrated GPU",
		})
	}
}

// validateGPUIntegrated rejects a GPU request that disagrees with the machine
// type's built-in accelerator, which is neither swappable nor resizable.
func validateGPUIntegrated(req gcecatalog.ValidationRequest, mt *machinetype.MachineType, result *gcecatalog.ValidationResult) {
	if req.GPUType != "" && req.GPUType != mt.IntegratedGPUType {
		result.Valid = false
		result.Errors = append(result.Errors, gcecatalog.ValidationError{
			Field:      attrGPUType,
			Code:       gcecatalog.ErrorCodeGPUTypeFixed,
			Message:    fmt.Sprintf("Machine type '%s' has integrated %s GPU", mt.ID, mt.IntegratedGPUType),
			Suggestion: "GPU type cannot be changed for this machine type; use a different machine type for custom GPU selection",
		})
	}
	if req.GPUCount > 0 && req.GPUCount != mt.IntegratedGPUCount {
		result.Valid = false
		result.Errors = append(result.Errors, gcecatalog.ValidationError{
			Field:      attrGPUCount,
			Code:       gcecatalog.ErrorCodeGPUCountExceeded,
			Message:    fmt.Sprintf("Machine type '%s' has fixed GPU count of %d", mt.ID, mt.IntegratedGPUCount),
			Suggestion: "GPU count cannot be changed for this machine type",
		})
	}
}

// validateGPUAttachable checks a requested accelerator against zone
// availability and the machine type's attachment limit.
func (v *CatalogValidator) validateGPUAttachable(req gcecatalog.ValidationRequest, mt *machinetype.MachineType, result *gcecatalog.ValidationResult) {
	if req.GPUType == "" {
		return
	}

	switch v.ranker.acceleratorAvailability(req.Zone, req.GPUType) {
	case gcecatalog.AvailabilityUnknown:
		result.Warnings = append(result.Warnings, gcecatalog.ValidationWarning{
			Code:    gcecatalog.WarningCodeCatalogStale,
			Message: "Could not verify GPU availability in zone",
		})
	case gcecatalog.AvailabilityNotOffered:
		result.Valid = false
		result.Errors = append(result.Errors, gcecatalog.ValidationError{
			Field:      attrGPUType,
			Code:       gcecatalog.ErrorCodeGPUNotInZone,
			Message:    fmt.Sprintf("GPU type '%s' is not available in zone '%s'", req.GPUType, req.Zone),
			Suggestion: "Select a different GPU type or zone",
		})
	case gcecatalog.AvailabilityOffered:
	}

	if req.GPUCount > mt.MaxGPUs && mt.MaxGPUs > 0 {
		result.Valid = false
		result.Errors = append(result.Errors, gcecatalog.ValidationError{
			Field:      attrGPUCount,
			Code:       gcecatalog.ErrorCodeGPUCountExceeded,
			Message:    fmt.Sprintf("GPU count %d exceeds maximum of %d for machine type '%s'", req.GPUCount, mt.MaxGPUs, mt.ID),
			Suggestion: fmt.Sprintf("Reduce GPU count to %d or less", mt.MaxGPUs),
		})
	}
}

// quotaNearLimitPercent is the usage percentage at which a passing quota
// judgment still warns the caller.
const quotaNearLimitPercent = 85

// validateQuotas checks the shape's regional quota headroom on the same
// declarative demand model ranking uses (bindingDemand: CPUs, GPUs when the
// shape carries them, an in-use address when device media takes external
// IPs), judged per metric for the requested zone's region. Shared semantics:
// a Spot spec judges the preemptible pools with the standard pools as
// fallback when no preemptible grant (or a zero-limit one) is reported; an
// untracked metric passes silently; a stale row passes with a staleness
// warning, because a dead quota refresh must not block creates.
// Validation-only semantics: a post-fallback zero-limit row fails (a fresh row
// granting 0 is a real no-grant and not refresh lag, while ranking stays
// advisory there), no reserve margin (an explicitly chosen zone is judged on
// real capacity; the operator margin is an automatic-placement reserve), and the
// near-limit warnings.
func (v *CatalogValidator) validateQuotas(req gcecatalog.ValidationRequest, mt *machinetype.MachineType, result *gcecatalog.ValidationResult) {
	region := gcecatalog.RegionFromZone(req.Zone)
	isSpot := req.ProvisioningModel == provisioningModelSpot
	staleAfter := v.config.QuotaStaleThreshold
	if staleAfter <= 0 {
		staleAfter = defaultQuotaStale
	}

	for _, d := range v.ranker.bindingDemand(req, mt) {
		mv := v.ranker.judgeMetric(region, d, 0, staleAfter)
		switch mv.state {
		case metricUnknown:
			// An untracked pool is not evidence of missing capacity.
		case metricStale:
			result.Warnings = append(result.Warnings, gcecatalog.ValidationWarning{
				Code: gcecatalog.WarningCodeCatalogStale,
				Message: fmt.Sprintf("%s quota data in region '%s' is stale; creation may still fail on quota",
					mv.judgedType, region),
			})
		case metricZeroLimit:
			// Post-fallback zero limit is an ungranted pool. The create would
			// fail at GCE with QUOTA_EXCEEDED, so fail here with the axis's
			// error ("available 0").
			result.Valid = false
			result.Errors = append(result.Errors, quotaExceededError(d, mv, region, isSpot))
		case metricJudged:
			if !mv.fits {
				result.Valid = false
				result.Errors = append(result.Errors, quotaExceededError(d, mv, region, isSpot))
				continue
			}
			if mv.row.IsNearLimit(quotaNearLimitPercent) {
				result.Warnings = append(result.Warnings, gcecatalog.ValidationWarning{
					Code: gcecatalog.WarningCodeQuotaNearLimit,
					Message: fmt.Sprintf("%s quota in region '%s' is at %.0f%% capacity",
						quotaAxisName(d, mv.judgedType), region, mv.row.Percentage()),
				})
			}
		}
	}
}

// quotaExceededError renders one failed quota judgment as the axis's
// validation error: the field the caller can change, the (Spot-aware) error
// code, and the judged pool in the message.
func quotaExceededError(d metricDemand, mv metricVerdict, region string, spot bool) gcecatalog.ValidationError {
	switch d.healthMetric {
	case gcecatalog.ZoneMetricQuotaGPU:
		code := gcecatalog.ErrorCodeQuotaGPUExceeded
		suggestion := "Request a quota increase or reduce GPU count"
		if spot {
			code = gcecatalog.ErrorCodeSpotQuotaGPUExceeded
			suggestion = "Request a preemptible GPU quota increase, reduce GPU count, or switch to a standard VM"
		}
		return gcecatalog.ValidationError{
			Field: attrGPUCount,
			Code:  code,
			Message: fmt.Sprintf("Insufficient %s GPU quota in region '%s': need %d, available %d",
				quotaTypeDisplayName(mv.judgedType), region, d.amount, mv.row.Available()),
			Suggestion: suggestion,
		}
	case gcecatalog.ZoneMetricQuotaIP:
		return gcecatalog.ValidationError{
			Field: attrZone,
			Code:  gcecatalog.ErrorCodeQuotaIPExceeded,
			Message: fmt.Sprintf("Insufficient in-use IP address quota in region '%s': need %d, available %d",
				region, d.amount, mv.row.Available()),
			Suggestion: "Request an IN_USE_ADDRESSES quota increase or select a zone in another region",
		}
	case gcecatalog.ZoneMetricQuotaSSD, gcecatalog.ZoneMetricQuotaHyperdisk:
		// The machine type is the field to change: the boot disk pool follows
		// from the shape's disk type, so a different shape is what moves this.
		return gcecatalog.ValidationError{
			Field: attrMachineType,
			Code:  gcecatalog.ErrorCodeQuotaDiskExceeded,
			Message: fmt.Sprintf("Insufficient %s boot disk quota in region '%s': need %d GB, available %d GB",
				mv.judgedType, region, d.amount, mv.row.Available()),
			Suggestion: fmt.Sprintf("Request a %s quota increase or select a zone in another region", mv.judgedType),
		}
	default:
		code := gcecatalog.ErrorCodeQuotaCPUExceeded
		suggestion := "Request a quota increase or use a smaller machine type"
		if spot {
			code = gcecatalog.ErrorCodeSpotQuotaCPUExceeded
			suggestion = "Request a preemptible CPU quota increase, use a smaller machine type, or switch to a standard VM"
		}
		return gcecatalog.ValidationError{
			Field: attrMachineType,
			Code:  code,
			Message: fmt.Sprintf("Insufficient %s CPU quota in region '%s': need %d, available %d",
				quotaTypeDisplayName(mv.judgedType), region, d.amount, mv.row.Available()),
			Suggestion: suggestion,
		}
	}
}

// quotaAxisName names a demand axis for warning messages ("standard CPU",
// "preemptible GPU", "in-use IP address", "HDB_TOTAL_GB boot disk").
func quotaAxisName(d metricDemand, judgedType string) string {
	switch d.healthMetric {
	case gcecatalog.ZoneMetricQuotaGPU:
		return quotaTypeDisplayName(judgedType) + " GPU"
	case gcecatalog.ZoneMetricQuotaIP:
		return "in-use IP address"
	case gcecatalog.ZoneMetricQuotaSSD, gcecatalog.ZoneMetricQuotaHyperdisk:
		return judgedType + " boot disk"
	default:
		return quotaTypeDisplayName(judgedType) + " CPU"
	}
}

// quotaTypeDisplayName returns a human-readable name for a quota type.
func quotaTypeDisplayName(quotaType string) string {
	if gcecatalog.IsPreemptibleQuotaType(quotaType) {
		return "preemptible"
	}
	return "standard"
}

// Ensure CatalogValidator implements gcecatalog.Validator.
var _ gcecatalog.Validator = (*CatalogValidator)(nil)
