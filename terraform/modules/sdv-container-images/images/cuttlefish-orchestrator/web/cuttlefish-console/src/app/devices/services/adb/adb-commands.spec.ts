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
 * Pins the shell command strings the domain ops produce, above all that
 * every device-controlled value (file names, package names) is
 * single-quote escaped, since apps can create files named `$(...)`.
 */

import type {Adb} from '@yume-chan/adb';
import {describe, expect, it} from 'vitest';
import {createDirectory, deleteDirectory, deleteFile} from './adb-files';
import {splitNameFor} from './adb-install';
import {clearAppData, forceStopApp, launchApp, uninstallApp} from './adb-packages';

type FakeResult = string | {stdout?: string; stderr?: string; exitCode?: number};

function fakeDevice(outputs: FakeResult[] = []): {device: Adb; commands: string[]} {
  const commands: string[] = [];
  const device = {
    subprocess: {
      shellProtocol: {
        spawnWaitText: (command: string) => {
          commands.push(command);
          const next = outputs.shift() ?? '';
          const result = typeof next === 'string' ? {stdout: next} : next;
          return Promise.resolve({
            stdout: result.stdout ?? '',
            stderr: result.stderr ?? '',
            exitCode: result.exitCode ?? 0,
          });
        },
      },
    },
  } as unknown as Adb;
  return {device, commands};
}

const HOSTILE = `/sdcard/it's a "$(reboot)" \`file\`.txt`;
const HOSTILE_ESCAPED = `'/sdcard/it'\\''s a "$(reboot)" \`file\`.txt'`;

describe('adb file commands', () => {
  it('escapes hostile paths in deleteFile', async () => {
    const {device, commands} = fakeDevice();
    await deleteFile(device, HOSTILE);
    expect(commands).toEqual([`rm -f ${HOSTILE_ESCAPED}`]);
  });

  it('escapes hostile paths in deleteDirectory', async () => {
    const {device, commands} = fakeDevice();
    await deleteDirectory(device, HOSTILE);
    expect(commands).toEqual([`rm -rf ${HOSTILE_ESCAPED}`]);
  });

  it('escapes hostile paths in createDirectory', async () => {
    const {device, commands} = fakeDevice();
    await createDirectory(device, '/sdcard/new dir');
    expect(commands).toEqual([`mkdir '/sdcard/new dir'`]);
  });

  it('surfaces a nonzero exit as an error with the stderr text', async () => {
    const {device} = fakeDevice([{stderr: 'rm: /data/x: Permission denied\n', exitCode: 1}]);
    await expect(deleteFile(device, '/data/x')).rejects.toThrow('Permission denied');
  });

  it('surfaces mkdir failures instead of reporting silent success', async () => {
    const {device} = fakeDevice([{stderr: 'mkdir: read-only file system', exitCode: 1}]);
    await expect(createDirectory(device, '/system/nope')).rejects.toThrow('read-only');
  });
});

describe('adb package commands', () => {
  it('escapes package names', async () => {
    const {device, commands} = fakeDevice(['', 'Success']);
    await forceStopApp(device, 'com.example;reboot');
    await clearAppData(device, 'com.example;reboot');
    expect(commands).toEqual([
      `am force-stop 'com.example;reboot'`,
      `pm clear 'com.example;reboot'`,
    ]);
  });

  it('reports uninstall failure output', async () => {
    const {device} = fakeDevice(['Failure [DELETE_FAILED_INTERNAL_ERROR]']);
    await expect(uninstallApp(device, 'com.example')).rejects.toThrow('DELETE_FAILED');
  });

  it('reports pm clear failure, which pm signals in output rather than exit code', async () => {
    const {device} = fakeDevice(['Failed']);
    await expect(clearAppData(device, 'com.example')).rejects.toThrow('Failed');
  });

  it('escapes both package name and resolved activity in launchApp', async () => {
    const {device, commands} = fakeDevice(['com.example/.MainActivity\n']);
    await launchApp(device, 'com.example');
    expect(commands).toEqual([
      `cmd package resolve-activity --brief 'com.example' | tail -n 1`,
      `am start -n 'com.example/.MainActivity'`,
    ]);
  });
});

describe('splitNameFor', () => {
  it('derives pm install-write split names from file names', () => {
    expect(splitNameFor('base.apk')).toBe('base');
    expect(splitNameFor('config.arm64_v8a.apk')).toBe('config.arm64_v8a');
    expect(splitNameFor('weird name (1).apk')).toBe('weird_name__1_');
    expect(splitNameFor('.apk')).toBe('base');
  });
});
