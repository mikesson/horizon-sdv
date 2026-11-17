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
 * CVD Configuration Schemas
 *
 * Zod schemas for Cuttlefish Virtual Device (CVD) configuration.
 * These schemas match the upstream proto definition in:
 * base/cvd/cuttlefish/host/commands/cvd/cli/parser/load_config.proto
 */

import {z} from 'zod';

// ============================================================================
// Template Names (Built-in device templates)
// ============================================================================

/**
 * Built-in device templates from launch_cvd_templates.cpp
 */
const TemplateNameSchema = z
  .enum(['phone', 'tablet', 'tv', 'wearable', 'auto', 'slim', 'go', 'foldable'])
  .meta({
    title: 'Template',
    description:
      'Built-in device template to inherit from (phone, tablet, tv, wearable, auto, slim, go, foldable)',
  });

export type TemplateName = z.infer<typeof TemplateNameSchema>;

// ============================================================================
// Display Configuration
// ============================================================================

/**
 * Display overlay for multi-instance display embedding
 */
const DisplayOverlaySchema = z
  .object({
    vm_index: z.number().int().min(0).meta({
      title: 'VM Index',
      description: 'Source VM index (0-based) for display overlay',
    }),
    display_index: z.number().int().min(0).meta({
      title: 'Display Index',
      description: 'Source display index (0-based) for display overlay',
    }),
  })
  .strict()
  .meta({
    title: 'Display Overlay',
    description: 'Embed a display from another VM instance',
  });

/**
 * Display configuration for a single screen
 */
const DisplaySchema = z
  .object({
    width: z
      .number()
      .int()
      .min(320)
      .max(7680)
      .optional()
      .meta({
        title: 'Width',
        description: 'Display width in pixels (320-7680)',
        examples: [720, 1080, 1920, 2560],
      }),
    height: z
      .number()
      .int()
      .min(320)
      .max(4320)
      .optional()
      .meta({
        title: 'Height',
        description: 'Display height in pixels (320-4320)',
        examples: [1280, 1920, 2560, 1800],
      }),
    dpi: z
      .number()
      .int()
      .min(120)
      .max(640)
      .optional()
      .meta({
        title: 'DPI',
        description: 'Dots per inch / screen density (120-640)',
        examples: [160, 240, 320, 480],
      }),
    refresh_rate_hertz: z
      .number()
      .int()
      .min(30)
      .max(120)
      .optional()
      .meta({
        title: 'Refresh Rate',
        description: 'Screen refresh rate in Hz (30-120)',
        examples: [60, 90, 120],
      }),
    overlays: z.array(DisplayOverlaySchema).optional().meta({
      title: 'Overlays',
      description: 'Display overlays from other VM instances',
    }),
  })
  .strict()
  .meta({
    title: 'Display',
    description: 'Display/screen configuration',
  });

// ============================================================================
// VM Configuration
// ============================================================================

/**
 * The switch for guest-to-guest vsock, and therefore for whether
 * `connectivity.vsock.guest_group` means anything at all.
 *
 * A string rather than a boolean because `auto` is a third value and the
 * default. The host resolves it at launch from the guest image and the guest
 * architecture. On x86_64 it resolves OFF unless the image itself asks for it,
 * and the host never reports the resolved value back.
 */
const VhostUserVsockSchema = z.enum(['auto', 'true', 'false']);

/**
 * Crosvm VMM-specific settings
 */
const CrosvmSchema = z
  .object({
    enable_sandbox: z.boolean().optional().describe('Enable crosvm sandbox (default: false)'),
    simple_media_device: z.boolean().optional().describe('Use simple media device'),
    v4l2_proxy: z.string().optional().describe('V4L2 proxy path'),
    vhost_user_vsock: VhostUserVsockSchema.optional().describe(
      'Guest-to-guest vsock layer (default: auto, which resolves off on x86_64)'
    ),
  })
  .strict();

export type VhostUserVsock = z.infer<typeof VhostUserVsockSchema>;

/**
 * Gem5 VMM settings (cycle-accurate CPU simulator)
 */
const Gem5Schema = z.object({}).strict();

/**
 * QEMU VMM settings
 */
const QemuSchema = z.object({}).strict();

/**
 * Device state for custom actions (e.g., foldable hinge states)
 */
const DeviceStateSchema = z
  .object({
    lid_switch_open: z.boolean().optional().describe('Lid switch state'),
    hinge_angle_value: z
      .number()
      .int()
      .min(0)
      .max(360)
      .optional()
      .describe('Hinge angle in degrees'),
  })
  .strict();

/**
 * Button definition for custom actions
 */
const ButtonSchema = z
  .object({
    command: z.string().optional().describe('Command identifier'),
    title: z.string().optional().describe('Button title'),
    icon_name: z.string().optional().describe('Material icon name'),
  })
  .strict();

/**
 * Custom action for WebRTC UI buttons
 */
const CustomActionSchema = z
  .object({
    shell_command: z.string().optional().describe('Shell command to execute'),
    button: ButtonSchema.optional().describe('Single button definition'),
    server: z.string().optional().describe('Action server name'),
    buttons: z.array(ButtonSchema).optional().describe('Multiple button definitions'),
    device_states: z.array(DeviceStateSchema).optional().describe('Device states for this action'),
  })
  .strict();

/**
 * Setup wizard mode options
 */
const SetupWizardModeSchema = z.enum(['DISABLED', 'OPTIONAL', 'REQUIRED']);

export type SetupWizardMode = z.infer<typeof SetupWizardModeSchema>;

/**
 * VM configuration
 */
const VmSchema = z
  .object({
    cpus: z
      .number()
      .int()
      .min(1)
      .max(64)
      .optional()
      .meta({
        title: 'vCPUs',
        description: 'Number of virtual CPUs (1-64)',
        examples: [2, 4, 8],
      }),
    memory_mb: z
      .number()
      .int()
      .min(1024)
      .max(131072)
      .optional()
      .meta({
        title: 'Memory (MB)',
        description: 'RAM in megabytes (1024-131072)',
        examples: [2048, 4096, 8192, 16384],
      }),
    use_sdcard: z.boolean().optional().meta({
      title: 'SD Card',
      description: 'Enable virtual SD card storage',
    }),
    setupwizard_mode: SetupWizardModeSchema.optional().meta({
      title: 'Setup Wizard',
      description: 'Android setup wizard behavior (DISABLED, OPTIONAL, REQUIRED)',
    }),
    uuid: z.string().optional().meta({
      title: 'UUID',
      description: 'Custom VM UUID (auto-generated if not specified)',
    }),
    crosvm: CrosvmSchema.optional().meta({
      title: 'Crosvm',
      description: 'Crosvm virtual machine monitor settings',
    }),
    gem5: Gem5Schema.optional().meta({
      title: 'Gem5',
      description: 'Gem5 cycle-accurate CPU simulator settings',
    }),
    qemu: QemuSchema.optional().meta({
      title: 'QEMU',
      description: 'QEMU virtual machine monitor settings',
    }),
    custom_actions: z.array(CustomActionSchema).optional().meta({
      title: 'Custom Actions',
      description: 'Custom WebRTC UI buttons and actions',
    }),
  })
  .strict()
  .meta({
    title: 'VM',
    description: 'Virtual machine configuration (CPU, memory, VMM settings)',
  });

// ============================================================================
// Graphics Configuration
// ============================================================================

/**
 * GPU rendering mode options, in the console's vocabulary
 */
const GpuModeSchema = z.enum(['gfxstream', 'drm_virgl', 'swiftshader', 'auto']);

export type GpuMode = z.infer<typeof GpuModeSchema>;

/**
 * What a config may carry. `guest_swiftshader` is cvd's own name for software
 * rendering, accepted so a config written for cvd imports. The server
 * translates `swiftshader` to it, so the console never emits it.
 */
const WireGpuModeSchema = z.enum([...GpuModeSchema.options, 'guest_swiftshader']);

export type WireGpuMode = z.infer<typeof WireGpuModeSchema>;

/**
 * Graphics configuration
 */
const GraphicsSchema = z
  .object({
    displays: z.array(DisplaySchema).min(1).max(4).optional().describe('Display configurations'),
    record_screen: z.boolean().optional().describe('Record screen to file'),
    gpu_mode: WireGpuModeSchema.optional().describe('GPU rendering mode'),
  })
  .strict();

// ============================================================================
// Boot Configuration
// ============================================================================

/**
 * Build reference for kernel/bootloader
 */
const BuildSchema = z
  .object({
    build: z.string().optional().describe('Build reference'),
  })
  .strict();

/**
 * Boot configuration
 */
const BootSchema = z
  .object({
    kernel: BuildSchema.optional().describe('Custom kernel build'),
    enable_bootanimation: z.boolean().optional().describe('Enable boot animation'),
    extra_bootconfig_args: z.string().max(1024).optional().describe('Extra bootconfig arguments'),
    build: z.string().optional().describe('Boot build reference'),
    bootloader: BuildSchema.optional().describe('Custom bootloader'),
    android_efi_loader: BuildSchema.optional().describe('Android EFI loader'),
  })
  .strict();

// ============================================================================
// Security Configuration
// ============================================================================

/**
 * Security configuration
 */
const SecuritySchema = z
  .object({
    serial_number: z.string().optional().describe('Device serial number'),
    use_random_serial: z.boolean().optional().describe('Generate random serial number'),
    guest_enforce_security: z.boolean().optional().describe('Enforce security in guest'),
  })
  .strict();

// ============================================================================
// Disk Configuration
// ============================================================================

/**
 * Super partition override
 */
const SuperSchema = z
  .object({
    system: z.string().optional().describe('System image override'),
  })
  .strict();

/**
 * Disk configuration
 */
const DiskSchema = z
  .object({
    default_build: z.string().optional().describe('Build reference (@ab/build_id/target or path)'),
    super: SuperSchema.optional().describe('Super partition override'),
    download_img_zip: z.boolean().optional().describe('Download img zip'),
    download_target_files_zip: z.boolean().optional().describe('Download target files zip'),
    blank_data_image_mb: z
      .number()
      .int()
      .min(512)
      .max(65536)
      .optional()
      .describe('Size of blank data image in MB'),
    otatools: z.string().optional().describe('OTA tools path'),
  })
  .strict();

// ============================================================================
// Connectivity Configuration
// ============================================================================

/**
 * Vsock configuration for inter-VM communication
 */
const VsockSchema = z
  .object({
    guest_group: z.string().optional().describe('Vsock guest group (format: g1+g2)'),
  })
  .strict();

/**
 * Connectivity configuration
 */
const ConnectivitySchema = z
  .object({
    vsock: VsockSchema.optional().describe('Vsock configuration'),
  })
  .strict();

// ============================================================================
// Streaming Configuration
// ============================================================================

/**
 * WebRTC streaming configuration
 */
const StreamingSchema = z
  .object({
    device_id: z.string().optional().describe('WebRTC device ID'),
  })
  .strict();

// ============================================================================
// Instance Configuration
// ============================================================================

/**
 * Instance name pattern: lowercase letters, numbers, hyphens
 */
const INSTANCE_NAME_PATTERN = /^[a-z][a-z0-9-]*$/;

/**
 * Single CVD instance configuration
 */
const InstanceSchema = z
  .object({
    '@import': TemplateNameSchema.optional().meta({
      title: '@import',
      description:
        'Template to inherit from (phone, tablet, tv, wearable, auto, slim, go, foldable)',
    }),
    '@build': z.string().min(1).optional().meta({
      title: '@build',
      description:
        'Id of a registered build whose artifacts fill this instance. CCO only: removed before the config reaches the host, and not allowed together with disk.default_build',
    }),
    name: z
      .string()
      .regex(INSTANCE_NAME_PATTERN, 'Must start with letter, contain only a-z, 0-9, -')
      .min(1)
      .max(63)
      .optional()
      .meta({
        title: 'Name',
        description: 'Instance name (lowercase letters, numbers, hyphens)',
        examples: ['device-1', 'my-phone', 'test-tablet'],
      }),
    vm: VmSchema.optional().meta({
      title: 'VM',
      description: 'Virtual machine configuration',
    }),
    boot: BootSchema.optional().meta({
      title: 'Boot',
      description: 'Boot and kernel configuration',
    }),
    security: SecuritySchema.optional().meta({
      title: 'Security',
      description: 'Security and serial number settings',
    }),
    disk: DiskSchema.optional().meta({
      title: 'Disk',
      description: 'Disk and build artifact configuration',
    }),
    graphics: GraphicsSchema.optional().meta({
      title: 'Graphics',
      description: 'Display and GPU configuration',
    }),
    streaming: StreamingSchema.optional().meta({
      title: 'Streaming',
      description: 'WebRTC streaming configuration',
    }),
    connectivity: ConnectivitySchema.optional().meta({
      title: 'Connectivity',
      description: 'Network and vsock configuration',
    }),
  })
  .strict()
  .meta({
    title: 'Instance',
    description: 'CVD instance configuration',
  });

export type Instance = z.infer<typeof InstanceSchema>;

// ============================================================================
// Common Configuration
// ============================================================================

/**
 * Common configuration shared across all instances
 */
const CommonSchema = z
  .object({
    group_name: z
      .string()
      .regex(/^[a-z][a-z0-9-]*$/)
      .max(63)
      .optional()
      .describe('CVD group name'),
    host_package: z.string().optional().describe('Host package path'),
  })
  .strict();

// ============================================================================
// Fetch Configuration
// ============================================================================

/**
 * Fetch configuration for build artifacts
 */
const FetchSchema = z
  .object({
    api_key: z.string().optional().describe('API key for fetch'),
    credential_source: z.string().optional().describe('Credential source'),
    wait_retry_period: z.number().int().optional().describe('Wait retry period in seconds'),
    keep_downloaded_archives: z.boolean().optional().describe('Keep downloaded archives'),
    api_base_url: z.string().optional().describe('API base URL'),
    project_id: z.string().optional().describe('Project ID'),
  })
  .strict();

// ============================================================================
// Metrics Configuration
// ============================================================================

/**
 * Metrics configuration
 */
const MetricsSchema = z
  .object({
    enable: z.boolean().optional().describe('Enable metrics collection'),
  })
  .strict();

// ============================================================================
// Environment Specification (Root)
// ============================================================================

/**
 * Root CVD configuration (EnvironmentSpecification from proto)
 */
export const EnvironmentSpecificationSchema = z
  .object({
    instances: z.array(InstanceSchema).min(1).max(8).meta({
      title: 'Instances',
      description: 'CVD instance configurations (1-8 instances)',
    }),
    common: CommonSchema.optional().meta({
      title: 'Common',
      description: 'Configuration shared across all instances',
    }),
    fetch: FetchSchema.optional().meta({
      title: 'Fetch',
      description: 'Build artifact fetch configuration',
    }),
    metrics: MetricsSchema.optional().meta({
      title: 'Metrics',
      description: 'Metrics collection configuration',
    }),
    netsim_bt: z.boolean().optional().meta({
      title: 'Bluetooth Simulator',
      description: 'Enable netsim Bluetooth network simulator',
    }),
    netsim_uwb: z.boolean().optional().meta({
      title: 'UWB Simulator',
      description: 'Enable netsim UWB (Ultra-Wideband) network simulator',
    }),
  })
  .strict()
  .meta({
    title: 'CVD Configuration',
    description: 'Cuttlefish Virtual Device environment specification',
  });

export type EnvironmentSpecification = z.infer<typeof EnvironmentSpecificationSchema>;

// ============================================================================
// Template Defaults
// ============================================================================

/**
 * Default configurations for each built-in template
 * Values from launch_cvd_templates.cpp
 */
export const TEMPLATE_DEFAULTS: Record<TemplateName, Partial<Instance>> = {
  phone: {
    vm: {memory_mb: 4096, use_sdcard: true},
    graphics: {
      displays: [{width: 720, height: 1280, dpi: 320, refresh_rate_hertz: 60}],
    },
  },
  tablet: {
    vm: {memory_mb: 4096, use_sdcard: true},
    graphics: {
      displays: [{width: 2560, height: 1800, dpi: 320, refresh_rate_hertz: 60}],
    },
  },
  tv: {
    vm: {memory_mb: 2048, use_sdcard: true},
    graphics: {
      displays: [{width: 1920, height: 1080, dpi: 213, refresh_rate_hertz: 60}],
    },
  },
  wearable: {
    vm: {memory_mb: 1536, use_sdcard: false},
    graphics: {
      displays: [{width: 450, height: 450, dpi: 320, refresh_rate_hertz: 60}],
    },
  },
  auto: {
    vm: {memory_mb: 4096, use_sdcard: true},
    graphics: {
      displays: [
        {width: 1080, height: 600, dpi: 120, refresh_rate_hertz: 60},
        {width: 400, height: 600, dpi: 120, refresh_rate_hertz: 60},
      ],
    },
  },
  slim: {
    vm: {memory_mb: 2048, use_sdcard: false},
    graphics: {
      displays: [{width: 720, height: 1280, dpi: 320, refresh_rate_hertz: 60}],
    },
  },
  go: {
    vm: {memory_mb: 2048, use_sdcard: true},
    graphics: {
      displays: [{width: 720, height: 1280, dpi: 320, refresh_rate_hertz: 60}],
    },
  },
  foldable: {
    vm: {memory_mb: 4096, use_sdcard: true},
    graphics: {
      displays: [
        {width: 1768, height: 2208, dpi: 374, refresh_rate_hertz: 60},
        {width: 832, height: 2268, dpi: 387, refresh_rate_hertz: 60},
      ],
    },
  },
};
