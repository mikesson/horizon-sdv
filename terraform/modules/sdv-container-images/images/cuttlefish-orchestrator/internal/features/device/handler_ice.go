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

package devicefeature

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"cloud-cuttlefish-orchestrator/internal/domain/auth"
	"cloud-cuttlefish-orchestrator/internal/platform/turn"
	"cloud-cuttlefish-orchestrator/internal/web"
)

// GetICEConfig returns the ICE configuration for a device's media: the servers
// each end of the connection is given, and the transport policy the client is
// held to.
//
// The two lists are not interchangeable. The client leg carries the addresses a
// browser can route to and the device leg the addresses a host VM can; a caller
// sends the device leg on to the device in request-offer.
func (h *DeviceHandler) GetICEConfig(ctx context.Context, input *GetICEConfigInput) (*GetICEConfigOutput, error) {
	principal, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, huma.Error401Unauthorized("Authentication required")
	}

	// Verify the device is live before issuing credentials: one minted for a
	// device that is gone would be refused at its first use by a relay that
	// cannot explain itself.
	if _, err := liveDevice(ctx, h.service.deviceRepo, input.ID); err != nil {
		return nil, web.MapError(err)
	}

	return &GetICEConfigOutput{
		Body: h.ice.Compose(principal.Email, turn.DeviceSubject(input.ID)),
	}, nil
}
