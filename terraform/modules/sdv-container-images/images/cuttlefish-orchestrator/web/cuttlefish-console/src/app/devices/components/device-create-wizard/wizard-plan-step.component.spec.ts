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

import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {beforeEach, describe, expect, it} from 'vitest';
import type {Resolution} from '../../../shared/models/requirements.model';
import {defaultCreateIntent} from '../device-create-page/create-intent.model';
import {WizardPlanStepComponent} from './wizard-plan-step.component';

function resolutionWith(warnings: string[]): Resolution {
  return {
    buildName: 'AOSP Latest Release Phone',
    buildAction: 'use',
    hostAction: 'provision',
    machineType: 'n1-standard-8',
    zone: 'europe-west4-a',
    placementReasons: ['most free host capacity'],
    warnings,
  };
}

describe('WizardPlanStepComponent', () => {
  let fixture: ComponentFixture<WizardPlanStepComponent>;

  function render(resolution: Resolution | null, customizable = false): HTMLElement {
    fixture = TestBed.createComponent(WizardPlanStepComponent);
    fixture.componentRef.setInput('intent', defaultCreateIntent());
    fixture.componentRef.setInput('planning', false);
    fixture.componentRef.setInput('resolution', resolution);
    fixture.componentRef.setInput('planError', null);
    fixture.componentRef.setInput('selectedBuild', null);
    fixture.componentRef.setInput('selectedHost', null);
    fixture.componentRef.setInput('customizable', customizable);
    fixture.detectChanges();
    return fixture.nativeElement as HTMLElement;
  }

  beforeEach(() => {
    TestBed.configureTestingModule({});
  });

  // A machine built one shape down the fleet's order arrives here, in the words
  // the resolution already carries, both shapes and the reason. The plan is the
  // one place a create says what it is about to do, so a second renderer for
  // this would be a second place for it to say something different.
  it('shows a fallback to another machine shape as a plan warning', () => {
    const root = render(
      resolutionWith(['wanted n2-standard-8 · built n1-standard-8 (n2-standard-8 stocked out)'])
    );

    const warning = root.querySelector('.plan-warning');
    expect(warning?.textContent).toContain('wanted n2-standard-8 · built n1-standard-8');
    expect(warning?.textContent).toContain('stocked out');
  });

  // Nothing to say means nothing shown: a warning strip standing open on every
  // plan is a strip nobody reads on the plan that has one.
  it('shows no warning strip when the plan resolved cleanly', () => {
    const root = render(resolutionWith([]));

    expect(root.querySelector('.plan-warning')).toBeNull();
  });

  // With Advanced mode switched off for the deployment, the plan must not
  // offer a way into it.
  it('offers Customize in Advanced only when the page allows it', () => {
    expect(render(resolutionWith([]), false).querySelector('.customize-link')).toBeNull();
    expect(
      render(resolutionWith([]), true).querySelector('.customize-link')?.textContent
    ).toContain('Customize in Advanced');
  });
});
