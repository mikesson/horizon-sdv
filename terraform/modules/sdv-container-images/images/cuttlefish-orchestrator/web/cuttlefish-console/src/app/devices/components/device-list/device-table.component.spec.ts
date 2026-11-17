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
 * The table is a pure projection, so what it owes its container is exactly
 * three things: the rows it was handed, the columns it was told to show, and
 * one emission per row command carrying that row's OWN device.
 *
 * Every emission test therefore renders two rows and acts on the second. A
 * one-row table cannot tell "emits the right device" from "emits the only
 * device", which is the shape of green assertion this repo keeps shipping.
 */

import type {ComponentFixture} from '@angular/core/testing';
import {TestBed} from '@angular/core/testing';
import {provideRouter} from '@angular/router';
import {afterEach, beforeEach, describe, expect, it} from 'vitest';
import type {Device} from '../../models/device.model';
import {DeviceTableComponent} from './device-table.component';

/** What the device list passes; the table renders whatever subset it is given. */
const COLUMNS = [
  'select',
  'status',
  'name',
  'host',
  'region',
  'lifecycle',
  'pin',
  'build',
  'gpu',
  'ports',
  'created',
  'actions',
];

const CREATED_AT = new Date('2026-03-04T09:15:00Z');
const ONE_HOUR_MS = 3_600_000;

function makeDevice(overrides: Partial<Device> = {}): Device {
  return {
    id: 'dev-1',
    name: 'alpha',
    hostId: 'host-1',
    hostName: 'cf-host-alpha',
    hostZone: 'europe-west4-a',
    status: 'running',
    gpuMode: 'gfxstream',
    adbPort: 6520,
    webrtcPort: 8443,
    buildTarget: 'cf_x86_64_phone-userdebug',
    createdBy: 'user@example.com',
    createdAt: CREATED_AT,
    updatedAt: CREATED_AT,
    lifecycle: {
      mode: 'ephemeral',
      ttlSeconds: 14400,
      // Far enough out that the row timer stays healthy: a default row that
      // emitted `expired` would make the lapsed-lease test vacuous.
      expiresAt: new Date(Date.now() + ONE_HOUR_MS),
      extendedCount: 0,
    },
    ...overrides,
  };
}

/** Every output, in emission order, so a wrong device shows up as a wrong id. */
interface Recorded {
  connectAdb: Device[];
  copyId: Device[];
  startDevice: Device[];
  stopDevice: Device[];
  deleteDevice: Device[];
  pin: Device[];
  unpin: Device[];
  extend: Device[];
  expired: Device[];
  toggleRow: string[];
  toggleAll: boolean[];
}

describe('DeviceTableComponent', () => {
  let fixture: ComponentFixture<DeviceTableComponent>;

  function record(table: DeviceTableComponent): Recorded {
    const seen: Recorded = {
      connectAdb: [],
      copyId: [],
      startDevice: [],
      stopDevice: [],
      deleteDevice: [],
      pin: [],
      unpin: [],
      extend: [],
      expired: [],
      toggleRow: [],
      toggleAll: [],
    };
    table.connectAdb.subscribe(d => seen.connectAdb.push(d));
    table.copyId.subscribe(d => seen.copyId.push(d));
    table.startDevice.subscribe(d => seen.startDevice.push(d));
    table.stopDevice.subscribe(d => seen.stopDevice.push(d));
    table.deleteDevice.subscribe(d => seen.deleteDevice.push(d));
    table.pin.subscribe(d => seen.pin.push(d));
    table.unpin.subscribe(d => seen.unpin.push(d));
    table.extend.subscribe(d => seen.extend.push(d));
    table.expired.subscribe(d => seen.expired.push(d));
    table.toggleRow.subscribe(id => seen.toggleRow.push(id));
    table.toggleAll.subscribe(checked => seen.toggleAll.push(checked));
    return seen;
  }

  function render(
    devices: Device[],
    opts: {columns?: string[]; selected?: ReadonlySet<string>} = {}
  ): {root: HTMLElement; seen: Recorded} {
    fixture = TestBed.createComponent(DeviceTableComponent);
    fixture.componentRef.setInput('devices', devices);
    fixture.componentRef.setInput('displayedColumns', opts.columns ?? COLUMNS);
    fixture.componentRef.setInput('selectedIds', opts.selected ?? new Set<string>());
    // Subscribed before the first pass: a lapsed lease emits `expired` from the
    // row timer's ngOnInit, which runs inside this detectChanges.
    const seen = record(fixture.componentInstance);
    fixture.detectChanges();
    return {root: fixture.nativeElement as HTMLElement, seen};
  }

  function pick<T extends HTMLElement>(scope: ParentNode, selector: string): T {
    const el = scope.querySelector<T>(selector);
    if (!el) {
      throw new Error(`nothing matched ${selector}`);
    }
    return el;
  }

  function rows(root: HTMLElement): HTMLElement[] {
    return Array.from(root.querySelectorAll<HTMLElement>('tbody tr'));
  }

  function cells(root: HTMLElement, column: string): HTMLElement[] {
    return Array.from(root.querySelectorAll<HTMLElement>(`tbody td.mat-column-${column}`));
  }

  function textOf(el: Element | null | undefined): string {
    return (el?.textContent ?? '').replace(/\s+/g, ' ').trim();
  }

  /** Opens one row's actions menu and returns its items in order. */
  function openRowMenu(root: HTMLElement, rowIndex: number): HTMLElement[] {
    pick(cells(root, 'actions')[rowIndex], 'button[aria-label="Device actions"]').click();
    fixture.detectChanges();
    const panels = document.querySelectorAll<HTMLElement>('.mat-mdc-menu-panel');
    const panel = panels[panels.length - 1];
    if (!panel) {
      throw new Error('the actions menu did not open');
    }
    return Array.from(panel.querySelectorAll<HTMLElement>('[mat-menu-item]'));
  }

  /** The label span, not the item: textContent would carry the icon ligature. */
  function menuLabels(items: HTMLElement[]): string[] {
    return items.map(item => textOf(item.querySelector('span')));
  }

  function menuItem(items: HTMLElement[], label: string): HTMLElement {
    const found = items.find(item => textOf(item.querySelector('span')) === label);
    if (!found) {
      throw new Error(`the menu has no ${label} item, only ${menuLabels(items).join(', ')}`);
    }
    return found;
  }

  beforeEach(() => {
    TestBed.configureTestingModule({
      imports: [DeviceTableComponent],
      providers: [provideRouter([])],
    });
  });

  afterEach(() => {
    // Each row's lifecycle timer holds a 1s interval that only ngOnDestroy
    // clears, and the actions menu leaves an overlay behind.
    fixture.destroy();
  });

  describe('rows and columns', () => {
    it('renders one row per device, in the order it was given', () => {
      const {root} = render([
        makeDevice({id: 'dev-1', name: 'alpha'}),
        makeDevice({id: 'dev-2', name: 'bravo'}),
        makeDevice({id: 'dev-3', name: 'charlie'}),
      ]);

      expect(rows(root)).toHaveLength(3);
      expect(cells(root, 'name').map(c => textOf(c))).toEqual(['alpha', 'bravo', 'charlie']);
    });

    it('renders the header the device list asks for', () => {
      const {root} = render([makeDevice()]);

      const headers = Array.from(root.querySelectorAll('thead th')).map(th => textOf(th));
      expect(headers).toEqual([
        '',
        '',
        'Name',
        'Host',
        'Region',
        'TTL',
        '',
        'Build',
        'GPU Mode',
        'Ports',
        'Created',
        '',
      ]);
    });

    it('renders only the columns it is given', () => {
      const {root} = render([makeDevice()], {columns: ['name', 'status']});

      expect(root.querySelectorAll('thead th')).toHaveLength(2);
      expect(cells(root, 'name')).toHaveLength(1);
      expect(cells(root, 'gpu')).toHaveLength(0);
      expect(cells(root, 'actions')).toHaveLength(0);
    });

    it('renders the header but no rows for an empty fleet', () => {
      const {root} = render([]);

      expect(root.querySelectorAll('thead th')).toHaveLength(COLUMNS.length);
      expect(rows(root)).toHaveLength(0);
    });
  });

  describe('cells', () => {
    it('links each row to its own detail route and its host', () => {
      const {root} = render([
        makeDevice({id: 'dev-1', hostId: 'host-1'}),
        makeDevice({id: 'dev-2', hostId: 'host-2'}),
      ]);

      expect(cells(root, 'name').map(c => pick(c, 'a').getAttribute('href'))).toEqual([
        '/dev-1',
        '/dev-2',
      ]);
      expect(cells(root, 'host').map(c => pick(c, 'a').getAttribute('href'))).toEqual([
        '/hosts/host-1',
        '/hosts/host-2',
      ]);
    });

    it('falls back to the host id when the host has no name', () => {
      const {root} = render([
        makeDevice({id: 'dev-1', hostName: 'cf-host-alpha'}),
        makeDevice({id: 'dev-2', hostName: undefined, hostId: 'host-unnamed'}),
      ]);

      expect(cells(root, 'host').map(c => textOf(c))).toEqual(['cf-host-alpha', 'host-unnamed']);
    });

    it('shows the group size under the name, and nothing for a lone instance', () => {
      const {root} = render([
        makeDevice({id: 'dev-1', name: 'solo', instanceCount: 1}),
        makeDevice({id: 'dev-2', name: 'unstated', instanceCount: undefined}),
        makeDevice({id: 'dev-3', name: 'group', instanceCount: 3}),
      ]);

      const counts = cells(root, 'name').map(c => textOf(c.querySelector('.instance-count')));
      expect(counts).toEqual(['', '', '3 instances']);
    });

    it('shows a short region for a placed device and a dash for an unplaced one', () => {
      const {root} = render([
        makeDevice({id: 'dev-1', hostZone: 'europe-west4-a'}),
        makeDevice({id: 'dev-2', hostZone: undefined}),
      ]);

      const region = cells(root, 'region');
      expect(textOf(region[0])).toBe('🇳🇱 EU-W4');
      expect(textOf(region[1])).toBe('-');
      expect(region[1].querySelector('.region-display.unknown')).not.toBeNull();
    });

    it('shows the build target, or a default marker when the device names none', () => {
      const {root} = render([
        makeDevice({id: 'dev-1', buildTarget: 'cf_x86_64_phone-userdebug'}),
        makeDevice({id: 'dev-2', buildTarget: undefined}),
      ]);

      const build = cells(root, 'build');
      expect(textOf(build[0])).toBe('cf_x86_64_phone-userdebug');
      expect(build[0].querySelector('.build-info.default')).toBeNull();
      expect(textOf(build[1])).toBe('-');
      expect(build[1].querySelector('.build-info.default')).not.toBeNull();
    });

    it('carries the status onto the indicator class', () => {
      const {root} = render([
        makeDevice({id: 'dev-1', status: 'running'}),
        makeDevice({id: 'dev-2', status: 'error'}),
      ]);

      // Sorted: the [class] binding does not preserve the template's order.
      const classes = cells(root, 'status').map(c =>
        Array.from(pick(c, '.status-indicator').classList).sort()
      );
      expect(classes).toEqual([
        ['running', 'status-indicator'],
        ['error', 'status-indicator'],
      ]);
    });

    it('renders the creation date through the short format, not a raw Date', () => {
      const {root} = render([makeDevice()]);

      // Timezone-independent: what fails here is the pipe going missing, which
      // would print "Wed Mar 04 2026 ...".
      expect(textOf(cells(root, 'created')[0])).toMatch(/^\d{1,2}\/\d{1,2}\/\d{2}, /);
    });
  });

  describe('per-instance columns', () => {
    it('labels the GPU mode of a single-instance device', () => {
      const {root} = render([makeDevice({gpuMode: 'drm_virgl'})]);

      const chip = pick(cells(root, 'gpu')[0], 'mat-chip');
      expect(textOf(chip)).toBe('VirGL');
      expect(chip.classList.contains('drm_virgl')).toBe(true);
    });

    it('renders only the ports the device actually has', () => {
      const {root} = render([
        makeDevice({id: 'dev-1', adbPort: 6520, webrtcPort: 8443}),
        makeDevice({id: 'dev-2', adbPort: 6521, webrtcPort: undefined}),
        makeDevice({id: 'dev-3', adbPort: undefined, webrtcPort: undefined}),
      ]);

      const ports = cells(root, 'ports');
      expect(textOf(ports[0])).toBe('ADB: 6520 WebRTC: 8443');
      expect(textOf(ports[1])).toBe('ADB: 6521');
      expect(ports[1].querySelector('.port-badge.webrtc')).toBeNull();
      expect(ports[2].querySelectorAll('.port-badge')).toHaveLength(0);
    });

    it('replaces both per-instance columns with a hyphen for a group', () => {
      const {root} = render([makeDevice({instanceCount: 4, adbPort: 6520, webrtcPort: 8443})]);

      const gpu = cells(root, 'gpu')[0];
      const ports = cells(root, 'ports')[0];
      expect(textOf(gpu)).toBe('-');
      expect(gpu.querySelector('mat-chip')).toBeNull();
      expect(textOf(ports)).toBe('-');
      expect(ports.querySelectorAll('.port-badge')).toHaveLength(0);
    });
  });

  describe('selection', () => {
    function headerCheckbox(root: HTMLElement): HTMLInputElement {
      return pick<HTMLInputElement>(root, 'thead th.mat-column-select input[type="checkbox"]');
    }

    function rowCheckboxes(root: HTMLElement): HTMLInputElement[] {
      return cells(root, 'select').map(c => pick<HTMLInputElement>(c, 'input[type="checkbox"]'));
    }

    const three = (): Device[] => [
      makeDevice({id: 'dev-1'}),
      makeDevice({id: 'dev-2'}),
      makeDevice({id: 'dev-3'}),
    ];

    it('leaves the header clear when nothing is selected', () => {
      const {root} = render(three());

      const header = headerCheckbox(root);
      expect(header.checked).toBe(false);
      expect(header.indeterminate).toBe(false);
    });

    it('marks the header indeterminate when only some rows are selected', () => {
      const {root} = render(three(), {selected: new Set(['dev-2'])});

      const header = headerCheckbox(root);
      expect(header.checked).toBe(false);
      expect(header.indeterminate).toBe(true);
    });

    it('checks the header, determinately, when every row is selected', () => {
      const {root} = render(three(), {selected: new Set(['dev-1', 'dev-2', 'dev-3'])});

      const header = headerCheckbox(root);
      expect(header.checked).toBe(true);
      expect(header.indeterminate).toBe(false);
    });

    it('ignores a selection that names rows the table is not showing', () => {
      const {root} = render(three(), {selected: new Set(['dev-1', 'dev-2', 'gone', 'also-gone'])});

      // Stale ids outnumber the missing one; all-selected must still be false.
      const header = headerCheckbox(root);
      expect(header.checked).toBe(false);
      expect(header.indeterminate).toBe(true);
    });

    it('never reports all-selected for an empty fleet', () => {
      const {root} = render([], {selected: new Set(['dev-1'])});

      // `[].every()` is vacuously true, so this is the row-count guard's test.
      const header = headerCheckbox(root);
      expect(header.checked).toBe(false);
      expect(header.indeterminate).toBe(false);
    });

    it('names the device each checkbox selects', () => {
      const {root} = render([
        makeDevice({id: 'dev-1', name: 'alpha'}),
        makeDevice({id: 'dev-2', name: 'bravo'}),
      ]);

      // Twelve identical "Select" controls are one control to a screen reader.
      expect(rowCheckboxes(root).map(c => c.getAttribute('aria-label'))).toEqual([
        'Select alpha',
        'Select bravo',
      ]);
      expect(headerCheckbox(root).getAttribute('aria-label')).toBe('Select all devices');
    });

    it('checks exactly the selected rows', () => {
      const {root} = render(three(), {selected: new Set(['dev-1', 'dev-3'])});

      expect(rowCheckboxes(root).map(c => c.checked)).toEqual([true, false, true]);
    });

    it('emits the id of the row whose checkbox was toggled', () => {
      const {root, seen} = render(three());

      rowCheckboxes(root)[1].click();

      expect(seen.toggleRow).toEqual(['dev-2']);
      expect(seen.toggleAll).toEqual([]);
    });

    it('emits the header checkbox state it moved to', () => {
      const {root, seen} = render(three());

      headerCheckbox(root).click();
      expect(seen.toggleAll).toEqual([true]);

      const {root: allRoot, seen: allSeen} = render(three(), {
        selected: new Set(['dev-1', 'dev-2', 'dev-3']),
      });
      headerCheckbox(allRoot).click();
      expect(allSeen.toggleAll).toEqual([false]);
    });
  });

  describe('lifecycle column', () => {
    it('offers the extend button only while the device is running', () => {
      const {root} = render([
        makeDevice({id: 'dev-1', status: 'running'}),
        makeDevice({id: 'dev-2', status: 'stopped'}),
      ]);

      const lifecycle = cells(root, 'lifecycle');
      expect(lifecycle[0].querySelector('.extend-button')).not.toBeNull();
      expect(lifecycle[1].querySelector('.extend-button')).toBeNull();
    });

    it('emits extend with the row that asked for it', () => {
      const devices = [makeDevice({id: 'dev-1'}), makeDevice({id: 'dev-2'})];
      const {root, seen} = render(devices);

      pick(cells(root, 'lifecycle')[1], '.extend-button').click();

      expect(seen.extend).toEqual([devices[1]]);
    });

    it('emits expired only for the row whose lease has run out', () => {
      const lapsed = makeDevice({
        id: 'dev-lapsed',
        lifecycle: {
          mode: 'ephemeral',
          ttlSeconds: 14400,
          expiresAt: new Date(Date.now() - ONE_HOUR_MS),
          extendedCount: 0,
        },
      });
      const {seen} = render([makeDevice({id: 'dev-live'}), lapsed]);

      expect(seen.expired).toEqual([lapsed]);
    });

    it('disables the pin button unless the device is running', () => {
      const {root} = render([
        makeDevice({id: 'dev-1', status: 'running'}),
        makeDevice({id: 'dev-2', status: 'creating'}),
      ]);

      expect(cells(root, 'pin').map(c => pick<HTMLButtonElement>(c, 'button').disabled)).toEqual([
        false,
        true,
      ]);
    });

    it('emits pin for an ephemeral row and unpin for a pinned one', () => {
      const devices = [
        makeDevice({id: 'dev-1'}),
        makeDevice({
          id: 'dev-2',
          lifecycle: {mode: 'pinned', ttlSeconds: 14400, extendedCount: 0},
        }),
      ];
      const {root, seen} = render(devices);
      const pinCells = cells(root, 'pin');

      pick(pinCells[0], 'button').click();
      pick(pinCells[1], 'button').click();

      expect(seen.pin).toEqual([devices[0]]);
      expect(seen.unpin).toEqual([devices[1]]);
    });
  });

  describe('row actions', () => {
    it('shows the player shortcut only for running devices', () => {
      const {root} = render([
        makeDevice({id: 'dev-1', status: 'running'}),
        makeDevice({id: 'dev-2', status: 'stopped'}),
      ]);

      const actions = cells(root, 'actions');
      expect(pick(actions[0], '.open-player').getAttribute('href')).toBe('/dev-1/player');
      expect(actions[1].querySelector('.open-player')).toBeNull();
    });

    it('offers a running device its ADB, player and stop commands', () => {
      const {root} = render([makeDevice({status: 'running', adbPort: 6520})]);

      expect(menuLabels(openRowMenu(root, 0))).toEqual([
        'View Details',
        'Copy ID',
        'Connect ADB',
        'Open player',
        'Stop',
        'Delete',
      ]);
    });

    it('offers a stopped device only start and delete', () => {
      const {root} = render([makeDevice({status: 'stopped'})]);

      expect(menuLabels(openRowMenu(root, 0))).toEqual([
        'View Details',
        'Copy ID',
        'Start',
        'Delete',
      ]);
    });

    it('withholds Connect ADB from a running device with no ADB port', () => {
      const {root} = render([makeDevice({status: 'running', adbPort: undefined})]);

      expect(menuLabels(openRowMenu(root, 0))).not.toContain('Connect ADB');
    });

    it('leaves an erroring device deletable and nothing else', () => {
      const {root} = render([makeDevice({status: 'error'})]);

      expect(menuLabels(openRowMenu(root, 0))).toEqual(['View Details', 'Copy ID', 'Delete']);
    });

    it('emits each command with the row the menu belongs to', () => {
      const devices = [
        makeDevice({id: 'dev-1', status: 'running'}),
        makeDevice({id: 'dev-2', status: 'running', adbPort: 6521}),
      ];
      const {root, seen} = render(devices);

      const items = openRowMenu(root, 1);
      menuItem(items, 'Connect ADB').click();
      menuItem(items, 'Copy ID').click();
      menuItem(items, 'Stop').click();
      menuItem(items, 'Delete').click();

      expect(seen.connectAdb).toEqual([devices[1]]);
      expect(seen.copyId).toEqual([devices[1]]);
      expect(seen.stopDevice).toEqual([devices[1]]);
      expect(seen.deleteDevice).toEqual([devices[1]]);
      expect(seen.startDevice).toEqual([]);
    });

    it('emits startDevice from a stopped row', () => {
      const devices = [
        makeDevice({id: 'dev-1', status: 'running'}),
        makeDevice({id: 'dev-2', status: 'stopped'}),
      ];
      const {root, seen} = render(devices);

      menuItem(openRowMenu(root, 1), 'Start').click();

      expect(seen.startDevice).toEqual([devices[1]]);
      expect(seen.stopDevice).toEqual([]);
    });
  });
});
