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

package hostconfig

import (
	"context"
	"errors"
	"fmt"

	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// ImageReason says why a machine family has no bootable host image.
type ImageReason string

// The reasons a host image cannot be used. The ImageStatus.Reason enum tag
// mirrors this list.
const (
	// ImageNotFound: the image family holds no image in the project.
	ImageNotFound ImageReason = "not_found"
	// ImagePermissionDenied: the orchestrator may not read images there.
	ImagePermissionDenied ImageReason = "permission_denied"
	// ImageArchMismatch: the image is built for another CPU architecture.
	ImageArchMismatch ImageReason = "arch_mismatch"
	// ImageNoConfig: the machine family has no enabled host image row.
	ImageNoConfig ImageReason = "no_config"
)

// ImageReasons returns every reason, in the order the enum tag lists them.
func ImageReasons() []ImageReason {
	return []ImageReason{ImageNotFound, ImagePermissionDenied, ImageArchMismatch, ImageNoConfig}
}

// Image is the concrete GCE image an image family points at right now.
type Image struct {
	// SelfLink is the full image URL a VM insert takes as its source image.
	SelfLink string
	Name     string
	Family   string
	Project  string
	// Architecture is empty when the image declares none, which GCE treats
	// as x86.
	Architecture machinetype.Architecture
	Labels       map[string]string
}

// ImageSource resolves an image family to the image it points at. A missing
// family or a refused read comes back as an *ImageUnavailableError with no
// MachineFamily; any other error is a failed lookup that says nothing about
// the image. Satisfied by the platform gce.ImageClient.
type ImageSource interface {
	ResolveFamily(ctx context.Context, project, family string) (*Image, error)
}

// ImageUnavailableError says a machine family cannot boot a host here, and
// why. Every field is part of the message, because the operator reading it
// has to know which setting to change.
type ImageUnavailableError struct {
	MachineFamily string
	ImageFamily   string
	Project       string
	Reason        ImageReason
	// Detail is what the lookup reported, or the two architectures.
	Detail string
	// Status is the HTTP status GCE answered the image lookup with, and 0 when
	// the refusal is CCO's own (no_config, arch_mismatch). /v1 answers with it
	// and Detail, as upstream does when its VM insert refuses the image.
	Status int
}

func (e *ImageUnavailableError) Error() string {
	return e.Problem() + ". " + e.Fix()
}

// Problem is the refusal without its remedy.
func (e *ImageUnavailableError) Problem() string {
	var what string
	switch e.Reason {
	case ImageNoConfig:
		what = "no enabled host image config"
	case ImageNotFound:
		what = fmt.Sprintf("image family %q has no image in project %q", e.ImageFamily, e.Project)
	case ImagePermissionDenied:
		what = fmt.Sprintf("the orchestrator may not read images in project %q (image family %q)", e.Project, e.ImageFamily)
	case ImageArchMismatch:
		what = fmt.Sprintf("image family %q in project %q is built for another architecture", e.ImageFamily, e.Project)
	default:
		what = fmt.Sprintf("image family %q in project %q is unusable", e.ImageFamily, e.Project)
	}
	msg := fmt.Sprintf("machine family %s cannot boot a host: %s", e.MachineFamily, what)
	if e.Detail != "" {
		msg += " (" + e.Detail + ")"
	}
	return msg
}

// Fix names the setting that resolves the error.
func (e *ImageUnavailableError) Fix() string {
	switch e.Reason {
	case ImageNoConfig:
		return fmt.Sprintf("Create or enable its row with `cfctl admin host-image create %s IMAGE_FAMILY`", e.MachineFamily)
	case ImagePermissionDenied:
		return "Grant the orchestrator service account roles/compute.imageUser in that project, or point the family at a project it can read"
	case ImageArchMismatch:
		return fmt.Sprintf("Map %s to an image family built for its architecture with `cfctl admin host-image update %s --image-family ...`", e.MachineFamily, e.MachineFamily)
	default:
		return fmt.Sprintf("Publish the image family in that project, set CCO_HOST_IMAGE_PROJECT to the project that holds it, or override this family with `cfctl admin host-image update %s --source-project PROJECT`", e.MachineFamily)
	}
}

// EffectiveProject is the project a row's image family resolves in: the
// row's own source_project when set, else the deployment's host image project
// (CCO_HOST_IMAGE_PROJECT).
func EffectiveProject(cfg *HostImageConfig, deploymentProject string) string {
	if cfg != nil && cfg.SourceProject != "" {
		return cfg.SourceProject
	}
	return deploymentProject
}

// ResolveImage is the one rule for which image boots a machine family: the
// family's enabled row names the image family, EffectiveProject names the
// project, and the image must match arch. An empty arch skips the
// architecture check, for a family the catalog cannot classify.
//
// cfg nil or disabled is no_config. There is no fallback image: a family with
// no enabled row is not provisionable.
func ResolveImage(ctx context.Context, src ImageSource, cfg *HostImageConfig, machineFamily string, arch machinetype.Architecture, deploymentProject string) (*Image, error) {
	if cfg == nil || !cfg.Enabled || cfg.ImageFamily == "" {
		return nil, &ImageUnavailableError{MachineFamily: machineFamily, Reason: ImageNoConfig}
	}
	project := EffectiveProject(cfg, deploymentProject)
	if src == nil {
		return nil, errors.New("no image source configured")
	}
	img, err := src.ResolveFamily(ctx, project, cfg.ImageFamily)
	if err != nil {
		if unavailable, ok := errors.AsType[*ImageUnavailableError](err); ok {
			unavailable.MachineFamily = machineFamily
			return nil, unavailable
		}
		return nil, fmt.Errorf("resolving image family %s in project %s for machine family %s: %w",
			cfg.ImageFamily, project, machineFamily, err)
	}
	if !ArchitectureMatches(arch, img.Architecture) {
		return nil, &ImageUnavailableError{
			MachineFamily: machineFamily, ImageFamily: cfg.ImageFamily, Project: project,
			Reason: ImageArchMismatch,
			Detail: fmt.Sprintf("image %s is %s, machine family %s is %s",
				img.Name, archName(img.Architecture), machineFamily, arch),
		}
	}
	return img, nil
}

// ArchitectureMatches reports whether an image of imageArch boots a machine
// of machineArch. An image that declares no architecture is x86 to GCE, so it
// serves x86 and never arm.
func ArchitectureMatches(machineArch, imageArch machinetype.Architecture) bool {
	switch machineArch {
	case machinetype.ArchArm:
		return imageArch == machinetype.ArchArm
	case machinetype.ArchX86:
		return imageArch != machinetype.ArchArm
	default:
		return true
	}
}

func archName(a machinetype.Architecture) string {
	if a == "" {
		return "undeclared (x86)"
	}
	return string(a)
}

// ImageStatus is what the admin surfaces report about a row: the image it
// boots today, or why it boots nothing. A lookup that failed for another
// reason leaves Reason empty and carries the failure in Message.
type ImageStatus struct {
	OK           bool        `json:"ok" doc:"Whether hosts of this machine family can boot right now"`
	Project      string      `json:"project" doc:"Project the image family resolves in: the row's source_project, else CCO_HOST_IMAGE_PROJECT"`
	ImageName    string      `json:"image_name,omitempty" doc:"The image the family points at"`
	SelfLink     string      `json:"self_link,omitempty" doc:"Full URL of that image"`
	Architecture string      `json:"architecture,omitempty" doc:"The image's architecture (x86 or arm); empty when it declares none"`
	Reason       ImageReason `json:"reason,omitempty" enum:"not_found,permission_denied,arch_mismatch,no_config" doc:"Why the family cannot boot"`
	Message      string      `json:"message,omitempty" doc:"The full refusal, naming the setting that fixes it"`
}

// StatusOf renders one resolution as an ImageStatus.
func StatusOf(img *Image, err error, project string) *ImageStatus {
	if err == nil && img != nil {
		return &ImageStatus{
			OK: true, Project: project, ImageName: img.Name, SelfLink: img.SelfLink,
			Architecture: string(img.Architecture),
		}
	}
	st := &ImageStatus{Project: project}
	if err != nil {
		st.Message = err.Error()
	}
	if unavailable, ok := errors.AsType[*ImageUnavailableError](err); ok {
		st.Reason = unavailable.Reason
	}
	return st
}
