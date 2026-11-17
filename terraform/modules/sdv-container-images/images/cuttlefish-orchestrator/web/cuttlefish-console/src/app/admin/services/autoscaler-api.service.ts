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

import {HttpClient} from '@angular/common/http';
import {inject, Service} from '@angular/core';
import type {Observable} from 'rxjs';
import {catchError, map} from 'rxjs/operators';
import {createApiErrorHandler, resolveBasePath} from '../../shared/utils';
import type {AutoscalerSnapshot, StandbyRecommendation} from '../models/autoscaler.model';
import {AutoscalerSnapshotSchema, StandbyRecommendationSchema} from '../models/autoscaler.schema';
import type {AutoscaleMode, FleetSchedule, FleetSettings} from '../models/control-plane.model';
import {FleetSettingsSchema} from '../models/control-plane.schema';

/**
 * Transport for the Autoscaler page: the live snapshot, the observation
 * window's recommendation, and the policy writes.
 *
 * The writes stay separate. Mode plus a pool's two bounds, the two tuning
 * scalars, a pool's window list and a pool's shape order are separate endpoints
 * because a whole-policy PUT from any one surface carries a stale copy of the
 * others' fields and silently reverts them. A client that read, overlaid and
 * wrote back would clear every window the moment somebody edited a horizon, and
 * clear the shape order the moment somebody edited a drain grace. The two lists
 * are the expensive half of that. A scalar reverted to a stale value is visible
 * on the page it was typed on, where a list emptied without a word is not.
 *
 * The three per-pool writes name their pool in the body, so one pool's card can
 * never commit another's. The server replaces the entry it names and leaves
 * every other pool's alone. One page may present them all; it must not merge
 * their commits.
 */
@Service()
export class AutoscalerApiService {
  private readonly http = inject(HttpClient);
  private readonly configUrl = resolveBasePath('/v2/admin/system-config/fleet');
  private readonly handleError = createApiErrorHandler('Autoscaler', 'AutoscalerApiService');

  /** The live picture: target, fleet, gate, census and activity. */
  snapshot(): Observable<AutoscalerSnapshot> {
    return this.http.get<unknown>(resolveBasePath('/v2/admin/autoscaler')).pipe(
      map(data => AutoscalerSnapshotSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /**
   * The observation window's decision. Loaded on demand rather than polled: it
   * is a week's statistic and it moves by the day, not by the second.
   */
  recommendation(): Observable<StandbyRecommendation> {
    return this.http.get<unknown>(resolveBasePath('/v2/admin/autoscaler/recommendation')).pipe(
      map(data => StandbyRecommendationSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /** The stored policy with its compiled bounds, the controls' baseline. */
  policy(): Observable<FleetSettings> {
    return this.http.get<unknown>(this.configUrl).pipe(
      map(data => FleetSettingsSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /**
   * Scope one: the mode and one pool's two lower bounds, the group an operator
   * moves while watching the fleet.
   *
   * All four go every time. The endpoint replaces the mode and the named pool's
   * bounds together, so a body carrying only the edited one would set the others
   * to zero, turning a mode change into "and hold nothing". Writing a pool's
   * bounds is also what switches that pool on, which is why there is no separate
   * enable verb.
   */
  saveAutoscaler(
    mode: AutoscaleMode,
    pool: string,
    standby: number,
    floor: number
  ): Observable<FleetSettings> {
    return this.http
      .put<unknown>(`${this.configUrl}/autoscaler`, {mode, pool, standby, floor})
      .pipe(
        map(data => FleetSettingsSchema.parse(data)),
        catchError(this.handleError)
      );
  }

  /**
   * A pool switched off: its bounds, its windows and its order all go in one
   * commit. Its own verb rather than a write of zeros, because zero bounds are a
   * pool held empty on purpose and this is a pool no longer running. Creates
   * stop being routed into it and its machines drain by ordinary reclamation.
   */
  disablePool(pool: string): Observable<FleetSettings> {
    return this.http.delete<unknown>(`${this.configUrl}/pools/${pool}`).pipe(
      map(data => FleetSettingsSchema.parse(data)),
      catchError(this.handleError)
    );
  }

  /** Scope two: how far back demand is measured, and the lease a reclaim leaves. */
  saveTuning(demandHorizonSec: number, drainGraceSec: number): Observable<FleetSettings> {
    return this.http
      .put<unknown>(`${this.configUrl}/tuning`, {
        demand_horizon_sec: demandHorizonSec,
        drain_grace_sec: drainGraceSec,
      })
      .pipe(
        map(data => FleetSettingsSchema.parse(data)),
        catchError(this.handleError)
      );
  }

  /**
   * Scope three: one pool's window list, replaced whole. An empty array is how
   * every window is removed. There is no other verb, so it is explicit.
   */
  saveSchedules(pool: string, schedules: FleetSchedule[]): Observable<FleetSettings> {
    return this.http
      .put<unknown>(`${this.configUrl}/schedules`, {
        pool,
        schedules: schedules.map(window => ({
          name: window.name,
          hosts: window.hosts,
          start: window.start,
          duration_sec: window.durationSec,
          days: window.days,
          location: window.location,
        })),
      })
      .pipe(
        map(data => FleetSettingsSchema.parse(data)),
        catchError(this.handleError)
      );
  }

  /**
   * Scope four: the machine shapes one pool prefers, in order, replaced whole.
   * An empty array restores the catalog's default shape, the same "there is no
   * other verb" rule the window list follows.
   *
   * The server decides which shapes it will take, and which shapes those are
   * depends on the pool that will own them. A refusal names the shape and the
   * rule it broke, and arrives as a 400 the caller renders beside the list.
   */
  saveLadder(pool: string, rungs: string[]): Observable<FleetSettings> {
    return this.http.put<unknown>(`${this.configUrl}/ladder`, {pool, shape_ladder: rungs}).pipe(
      map(data => FleetSettingsSchema.parse(data)),
      catchError(this.handleError)
    );
  }
}
