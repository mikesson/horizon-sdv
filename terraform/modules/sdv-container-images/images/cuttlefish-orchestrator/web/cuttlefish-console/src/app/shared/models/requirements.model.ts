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

/**
 * The declarative placement vocabulary. Device create and batch create send
 * the same `requirements` object, so these types are shared rather than owned
 * by either feature.
 */

/** Interactive steers placement toward GPU capacity, headless toward plain compute. */
export type Interaction = 'interactive' | 'headless';

/** Provisioning economics for hosts a create provisions; auto defers to operator policy. */
export type CapacityModel = 'auto' | 'spot' | 'standard';

/** CPU architecture of the guest; an omitted one is x86. */
export type GuestArchitecture = 'x86' | 'arm';

/**
 * Which Android to run. 'default' resolves the platform default build,
 * auto-registering a tracking build when the registry is empty.
 */
export type BuildRequirement =
  | {kind: 'default'}
  | {kind: 'registered'; id: string}
  | {kind: 'track'; branch: string; target: string};

/**
 * The declarative `requirements` object. A device create sends one; a batch
 * create sends one for the whole fleet. Mutually exclusive with a chosen host.
 */
export interface PlacementRequirements {
  interaction: Interaction;
  build?: BuildRequirement;
  /** Hard constraint: fail instead of degrading when no GPU capacity exists. */
  gpuRequired?: boolean;
  /**
   * Preferred GCE region (e.g. us-central1). It is a placement preference and
   * never a constraint. Dominates ranking for interactive intent, tie-breaks
   * headless.
   */
  preferredRegion?: string;
  /**
   * Provisioning economics for an auto-provisioned host: spot (discounted,
   * preemptible), standard (on-demand), or auto, where operator policy decides
   * by interaction class.
   */
  capacity?: CapacityModel;
  /** Architecture of the guest; absent is x86. An arm64 guest needs the arm pool. */
  architecture?: GuestArchitecture;
  /**
   * The pool this create's host must belong to. Absent routes it from what it
   * declared it needs; a named pool this deployment does not offer is refused,
   * which is why the closed set lives on the server rather than in this type.
   */
  pool?: string;
}

type BuildAction = 'use' | 'register';
type HostAction = 'place' | 'provision';

/**
 * What a declarative create resolved (or, on a dry run, would resolve) the
 * intent to. IDs are absent on a plan for resources that do not exist yet.
 * A batch resolves one of these for the whole fleet: its hosts are a single
 * equivalence class, so the zone is ranked once and replicated.
 */
export interface Resolution {
  buildId?: string;
  buildName?: string;
  buildTarget?: string;
  buildAction: BuildAction;
  hostId?: string;
  hostName?: string;
  hostAction: HostAction;
  /**
   * The pool the derivation answered, 'default' included. A create that named
   * no pool still resolves to one. Absent only on an idempotent replay.
   */
  pool?: string;
  /** Provisioning shape, set when hostAction is 'provision'. */
  machineType?: string;
  zone?: string;
  /** Provisioning economics for the provisioned host: 'spot' or 'standard'. */
  capacityModel?: string;
  /** The zone ranking's trace for the chosen zone (why placement picked it). */
  placementReasons: string[];
  warnings: string[];
}
