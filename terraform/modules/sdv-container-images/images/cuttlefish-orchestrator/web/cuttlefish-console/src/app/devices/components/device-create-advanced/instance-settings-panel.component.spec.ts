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

import type {WritableSignal} from '@angular/core';
import {signal} from '@angular/core';
import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {form} from '@angular/forms/signals';
import {describe, expect, it} from 'vitest';
import type {TemplateName} from '../../models/cvd-config.schema';
import type {InstanceDraft} from './instance-draft.model';
import {instanceDraftFromTemplate, MAX_DISPLAYS} from './instance-draft.model';
import {InstanceSettingsPanelComponent} from './instance-settings-panel.component';

interface Rendered {
  fixture: ComponentFixture<InstanceSettingsPanelComponent>;
  model: WritableSignal<InstanceDraft>;
}

async function render(template: TemplateName): Promise<Rendered> {
  TestBed.resetTestingModule(); // a test may render more than one instance
  await TestBed.configureTestingModule({
    imports: [InstanceSettingsPanelComponent],
  }).compileComponents();
  const model = signal(instanceDraftFromTemplate(template, 'device-1'));
  const field = TestBed.runInInjectionContext(() => form(model));
  const fixture = TestBed.createComponent(InstanceSettingsPanelComponent);
  fixture.componentRef.setInput('field', field);
  fixture.detectChanges();
  return {fixture, model};
}

function displayHeadings(fixture: ComponentFixture<InstanceSettingsPanelComponent>): string[] {
  return [...fixture.nativeElement.querySelectorAll('.display-header > span')].map((el: Element) =>
    (el.textContent ?? '').trim()
  );
}

function addButton(fixture: ComponentFixture<InstanceSettingsPanelComponent>): HTMLButtonElement {
  return fixture.nativeElement.querySelector('.add-display-btn') as HTMLButtonElement;
}

function removeButtons(
  fixture: ComponentFixture<InstanceSettingsPanelComponent>
): HTMLButtonElement[] {
  return [...fixture.nativeElement.querySelectorAll('.display-header button')];
}

describe('InstanceSettingsPanelComponent', () => {
  it('renders one editable group per screen the template seeds', async () => {
    expect(displayHeadings((await render('phone')).fixture)).toEqual(['Display 1']);
    expect(displayHeadings((await render('auto')).fixture)).toEqual(['Display 1', 'Display 2']);
  });

  it('adds a screen to the bound model, up to the backend ceiling', async () => {
    const {fixture, model} = await render('phone');

    for (let i = 1; i < MAX_DISPLAYS; i++) {
      addButton(fixture).click();
      fixture.detectChanges();
    }

    expect(model().displays.length).toBe(MAX_DISPLAYS);
    expect(displayHeadings(fixture).length).toBe(MAX_DISPLAYS);
    expect(addButton(fixture).disabled).toBe(true);
  });

  it('removes the chosen screen and never offers to remove the last one', async () => {
    const {fixture, model} = await render('auto');

    removeButtons(fixture)[0].click();
    fixture.detectChanges();

    // Signal Forms tags each bound array item with a hidden identity symbol,
    // so the screens are compared field by field rather than whole.
    expect(model().displays.map(d => d.width)).toEqual([400]);
    expect(removeButtons(fixture)).toEqual([]);
  });
});
