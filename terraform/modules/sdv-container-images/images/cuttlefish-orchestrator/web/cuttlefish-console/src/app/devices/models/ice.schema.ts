/**
 * Copyright 2026 Google LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     https://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import {z} from 'zod';
import type {IceConfig, IceServer} from './device.model';

/**
 * Zod schema for one ICE server entry.
 *
 * username and credential are absent on STUN entries. The backend omits them
 * instead of sending empty strings, because an empty credential pair on a
 * turn: URI is a server that can never authenticate.
 */
export const IceServerSchema: z.ZodType<IceServer> = z.object({
  urls: z.array(z.string()),
  username: z.string().optional(),
  credential: z.string().optional(),
});

/** Zod schema for GET /v2/devices/{id}/ice-config. */
export const IceConfigSchema = z
  .object({
    client_servers: z.array(IceServerSchema),
    device_servers: z.array(IceServerSchema),
    ice_transport_policy: z.enum(['all', 'relay']),
  })
  .transform(
    (data): IceConfig => ({
      clientServers: data.client_servers,
      deviceServers: data.device_servers,
      policy: data.ice_transport_policy,
    })
  );
