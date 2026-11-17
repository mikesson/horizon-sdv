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

package gce

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/googleapi"

	"cloud-cuttlefish-orchestrator/internal/domain/hostconfig"
	"cloud-cuttlefish-orchestrator/internal/domain/machinetype"
)

// ImageClient resolves host image families to concrete images, keeping the
// Compute SDK out of the features that decide which image a host boots.
type ImageClient struct {
	images *compute.ImagesClient
}

// NewImageClient wraps the shared Compute images client.
func NewImageClient(images *compute.ImagesClient) *ImageClient {
	return &ImageClient{images: images}
}

var _ hostconfig.ImageSource = (*ImageClient)(nil)

// ResolveFamily returns the image the family points at in project. A 404
// and a 403 are statements about the image, so they come back as a typed
// *hostconfig.ImageUnavailableError; every other failure is wrapped and says
// nothing about whether the image exists.
func (c *ImageClient) ResolveFamily(ctx context.Context, project, family string) (*hostconfig.Image, error) {
	img, err := c.images.GetFromFamily(ctx, &computepb.GetFromFamilyImageRequest{
		Project: project,
		Family:  family,
	})
	if err != nil {
		if apiErr, ok := errors.AsType[*googleapi.Error](err); ok {
			reason := map[int]hostconfig.ImageReason{
				http.StatusNotFound:  hostconfig.ImageNotFound,
				http.StatusForbidden: hostconfig.ImagePermissionDenied,
			}[apiErr.Code]
			if reason != "" {
				return nil, &hostconfig.ImageUnavailableError{
					ImageFamily: family, Project: project, Reason: reason,
					Detail: apiErr.Message, Status: apiErr.Code,
				}
			}
		}
		return nil, fmt.Errorf("reading image family %s in project %s: %w", family, project, err)
	}
	return &hostconfig.Image{
		SelfLink:     img.GetSelfLink(),
		Name:         img.GetName(),
		Family:       family,
		Project:      project,
		Architecture: imageArchitecture(img.GetArchitecture()),
		Labels:       img.GetLabels(),
	}, nil
}

// imageArchitecture maps an image's architecture onto the catalog's
// vocabulary. An image that declares none answers empty.
func imageArchitecture(architecture string) machinetype.Architecture {
	switch architecture {
	case computepb.Image_X86_64.String():
		return machinetype.ArchX86
	case computepb.Image_ARM64.String():
		return machinetype.ArchArm
	default:
		return ""
	}
}
