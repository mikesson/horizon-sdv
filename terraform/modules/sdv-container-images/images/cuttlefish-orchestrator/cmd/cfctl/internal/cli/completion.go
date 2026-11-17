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

package cli

import (
	"fmt"
)

// The completion scripts below are maintained by hand. completion_test.go fails
// when a command from the Kong tree or a key from configSettings is missing
// from any of them.

const bashCompletionScript = `# cfctl bash completion
# Add to ~/.bashrc: eval "$(cfctl completion bash)"

_cfctl_completions() {
    local cur prev words cword
    _init_completion || return

    local commands="setup auth config host device build batch operation machine-type zone quota storage admin status doctor completion"
    local auth_commands="login whoami logout status"
    local config_commands="show profiles use create delete rename set-server set-audience set-auth set-credentials clear-credentials set-impersonate clear-impersonate"
    local host_commands="list get create delete start stop retry sync update extend ssh cvds"
    local device_commands="list get create delete start stop restart powerwash powerbtn extend pin unpin lifecycle screenshot bugreport record snapshot display connect disconnect connections webrtc"
    local device_record_commands="start stop list fetch"
    local device_snapshot_commands="take list delete restore"
    local device_display_commands="list add remove"
    local build_commands="push register list get update delete"
    local batch_commands="create list get devices delete"
    local operation_commands="get list"
    local machine_type_commands="list get default families"
    local zone_commands="list"
    local quota_commands="show set"
    local storage_commands="ls rm"
    local admin_commands="audit capacity catalog-freshness config control-plane host-image machine-type provisioning reclaimable stats tasks zone"
    local admin_tasks_commands="list retry delete stats"
    local admin_zone_commands="list enable disable clear-cooldown"
    local admin_machine_type_commands="update"
    local admin_host_image_commands="list get create update delete"
    local admin_config_commands="get set"
    local admin_config_keys="zone-pin quota-margin v1-zone-hint spot-auto-default default-gpu-machine-type default-upstream-build spot-min-uptime lifetimes docker-host artifact-buckets sweeps fleet"
    local completion_commands="bash zsh fish"

    # Server-backed values, cached in the config directory by 'completion values'.
    case "${prev}" in
        --machine-type)
            COMPREPLY=($(compgen -W "$(cfctl completion values machine-type 2>/dev/null)" -- "${cur}"))
            return
            ;;
        --zone)
            COMPREPLY=($(compgen -W "$(cfctl completion values zone 2>/dev/null)" -- "${cur}"))
            return
            ;;
        --host)
            COMPREPLY=($(compgen -W "$(cfctl completion values host 2>/dev/null)" -- "${cur}"))
            return
            ;;
        --profile)
            COMPREPLY=($(compgen -W "$(cfctl completion values profile 2>/dev/null)" -- "${cur}"))
            return
            ;;
    esac

    case "${COMP_CWORD}" in
        1)
            COMPREPLY=($(compgen -W "${commands}" -- "${cur}"))
            ;;
        2)
            case "${prev}" in
                auth)
                    COMPREPLY=($(compgen -W "${auth_commands}" -- "${cur}"))
                    ;;
                config)
                    COMPREPLY=($(compgen -W "${config_commands}" -- "${cur}"))
                    ;;
                host)
                    COMPREPLY=($(compgen -W "${host_commands}" -- "${cur}"))
                    ;;
                device)
                    COMPREPLY=($(compgen -W "${device_commands}" -- "${cur}"))
                    ;;
                build)
                    COMPREPLY=($(compgen -W "${build_commands}" -- "${cur}"))
                    ;;
                batch)
                    COMPREPLY=($(compgen -W "${batch_commands}" -- "${cur}"))
                    ;;
                operation)
                    COMPREPLY=($(compgen -W "${operation_commands}" -- "${cur}"))
                    ;;
                machine-type)
                    COMPREPLY=($(compgen -W "${machine_type_commands}" -- "${cur}"))
                    ;;
                zone)
                    COMPREPLY=($(compgen -W "${zone_commands}" -- "${cur}"))
                    ;;
                quota)
                    COMPREPLY=($(compgen -W "${quota_commands}" -- "${cur}"))
                    ;;
                storage)
                    COMPREPLY=($(compgen -W "${storage_commands}" -- "${cur}"))
                    ;;
                admin)
                    COMPREPLY=($(compgen -W "${admin_commands}" -- "${cur}"))
                    ;;
                completion)
                    COMPREPLY=($(compgen -W "${completion_commands}" -- "${cur}"))
                    ;;
            esac
            ;;
        3)
            if [ "${COMP_WORDS[1]}" = "admin" ] && [ "${prev}" = "tasks" ]; then
                COMPREPLY=($(compgen -W "${admin_tasks_commands}" -- "${cur}"))
            elif [ "${COMP_WORDS[1]}" = "admin" ] && [ "${prev}" = "config" ]; then
                COMPREPLY=($(compgen -W "${admin_config_commands}" -- "${cur}"))
            elif [ "${COMP_WORDS[1]}" = "admin" ] && [ "${prev}" = "zone" ]; then
                COMPREPLY=($(compgen -W "${admin_zone_commands}" -- "${cur}"))
            elif [ "${COMP_WORDS[1]}" = "admin" ] && [ "${prev}" = "machine-type" ]; then
                COMPREPLY=($(compgen -W "${admin_machine_type_commands}" -- "${cur}"))
            elif [ "${COMP_WORDS[1]}" = "admin" ] && [ "${prev}" = "host-image" ]; then
                COMPREPLY=($(compgen -W "${admin_host_image_commands}" -- "${cur}"))
            elif [ "${COMP_WORDS[1]}" = "device" ] && [ "${prev}" = "record" ]; then
                COMPREPLY=($(compgen -W "${device_record_commands}" -- "${cur}"))
            elif [ "${COMP_WORDS[1]}" = "device" ] && [ "${prev}" = "snapshot" ]; then
                COMPREPLY=($(compgen -W "${device_snapshot_commands}" -- "${cur}"))
            elif [ "${COMP_WORDS[1]}" = "device" ] && [ "${prev}" = "display" ]; then
                COMPREPLY=($(compgen -W "${device_display_commands}" -- "${cur}"))
            elif [ "${COMP_WORDS[1]}" = "config" ]; then
                case "${prev}" in
                    use|delete|rename)
                        COMPREPLY=($(compgen -W "$(cfctl completion values profile 2>/dev/null)" -- "${cur}"))
                        ;;
                esac
            elif [ "${COMP_WORDS[1]}" = "host" ]; then
                case "${prev}" in
                    get|delete|start|stop|retry|sync|update|extend|ssh|cvds)
                        COMPREPLY=($(compgen -W "$(cfctl completion values host 2>/dev/null)" -- "${cur}"))
                        ;;
                esac
            elif [ "${COMP_WORDS[1]}" = "device" ]; then
                case "${prev}" in
                    get|delete|start|stop|restart|powerwash|powerbtn|extend|pin|unpin|lifecycle|screenshot|bugreport|connect|disconnect|webrtc)
                        COMPREPLY=($(compgen -W "$(cfctl completion values device 2>/dev/null)" -- "${cur}"))
                        ;;
                esac
            elif [ "${COMP_WORDS[1]}" = "machine-type" ] && [ "${prev}" = "get" ]; then
                COMPREPLY=($(compgen -W "$(cfctl completion values machine-type 2>/dev/null)" -- "${cur}"))
            fi
            ;;
        4)
            if [ "${COMP_WORDS[1]}" = "admin" ] && [ "${COMP_WORDS[2]}" = "config" ]; then
                COMPREPLY=($(compgen -W "${admin_config_keys}" -- "${cur}"))
            fi
            ;;
    esac
}

complete -F _cfctl_completions cfctl
`

// Run prints the bash completion script.
func (c *CompletionBashCmd) Run() error {
	fmt.Print(bashCompletionScript)
	return nil
}

const zshCompletionScript = `#compdef cfctl

# cfctl zsh completion
# Add to ~/.zshrc: eval "$(cfctl completion zsh)"

_cfctl() {
    # Server-backed values, cached in the config directory by 'completion values'.
    case "$words[CURRENT-1]" in
        --machine-type)
            compadd -- $(cfctl completion values machine-type 2>/dev/null)
            return
            ;;
        --zone)
            compadd -- $(cfctl completion values zone 2>/dev/null)
            return
            ;;
        --host)
            compadd -- $(cfctl completion values host 2>/dev/null)
            return
            ;;
        --profile)
            compadd -- $(cfctl completion values profile 2>/dev/null)
            return
            ;;
    esac

    local -a commands
    commands=(
        'setup:Interactive setup wizard'
        'auth:Authentication commands'
        'config:Manage CLI configuration'
        'host:Manage host VMs'
        'device:Manage Cuttlefish devices'
        'build:Manage the build registry'
        'batch:Manage batches of devices (CI/fleet)'
        'operation:Inspect long-running operations'
        'machine-type:Browse the machine-type catalog'
        'zone:Browse the zone catalog'
        'quota:Host-capacity quota'
        'storage:Browse the artifact buckets'
        'admin:Admin operations'
        'status:Check server health'
        'doctor:Diagnose connectivity, auth, quota and storage'
        'completion:Generate shell completion scripts'
    )

    if (( CURRENT == 2 )); then
        _describe -t commands "commands" commands
    elif (( CURRENT == 3 )); then
        case "$words[2]" in
            auth)
                local -a auth_cmds
                auth_cmds=(
                    'login:Set up Application Default Credentials'
                    'whoami:Show current user information'
                    'logout:Delete cached tokens and revoke ADC'
                    'status:Show authentication status'
                )
                _describe -t commands "auth commands" auth_cmds
                ;;
            config)
                local -a config_cmds
                config_cmds=(
                    'show:Show each effective setting and its source'
                    'profiles:List profiles, marking the current one'
                    'use:Make a profile the current one'
                    'create:Create a profile without prompts'
                    'delete:Delete a profile'
                    'rename:Rename a profile'
                    'set-server:Set the orchestrator API server URL'
                    'set-audience:Set the IAP OAuth Client ID (ID token audience)'
                    'set-auth:Set the client auth mode (auto or none)'
                    'set-credentials:Set path to Google Service Account key JSON'
                    'clear-credentials:Clear stored credentials path'
                    'set-impersonate:Set service account to impersonate'
                    'clear-impersonate:Clear service account impersonation'
                )
                _describe -t commands "config commands" config_cmds
                ;;
            host)
                local -a host_cmds
                host_cmds=(
                    'list:List all hosts'
                    'get:Get host details'
                    'create:Create a new host VM'
                    'delete:Delete a host VM'
                    'start:Start a host VM'
                    'stop:Stop a host VM'
                    'retry:Re-provision a preempted or failed host'
                    'sync:Reconcile a host against GCE now'
                    'update:Change how many devices a host accepts'
                    'extend:Extend a reserved host lease'
                    'ssh:SSH into a host VM'
                    'cvds:List every CVD on a host, including ones created by cvdr'
                )
                _describe -t commands "host commands" host_cmds
                ;;
            device)
                local -a device_cmds
                device_cmds=(
                    'list:List all devices'
                    'get:Get device details'
                    'create:Create a new device'
                    'delete:Delete a device'
                    'start:Start a device'
                    'stop:Stop a device'
                    'restart:Restart a device in place'
                    'powerwash:Reset a device to first-boot state'
                    'powerbtn:Press the power button'
                    'extend:Extend a device TTL'
                    'pin:Fence a device from reclamation'
                    'unpin:Return a device to normal reclamation'
                    'lifecycle:Show TTL, expiry and pin state'
                    'screenshot:Capture a display screenshot'
                    'bugreport:Capture a bugreport to the artifact bucket'
                    'record:Manage screen recordings'
                    'snapshot:Manage device snapshots'
                    'display:Manage a device displays'
                    'connect:Connect to device via ADB'
                    'disconnect:Stop background ADB connections'
                    'connections:List background ADB connections'
                    'webrtc:Open device WebRTC in browser'
                )
                _describe -t commands "device commands" device_cmds
                ;;
            build)
                local -a build_cmds
                build_cmds=(
                    'push:Push local build artifacts and register them'
                    'register:Register an Android CI build by build ID and target'
                    'list:List registered builds'
                    'get:Get build details'
                    'update:Change name, description, default or artifacts'
                    'delete:Delete a build from the registry'
                )
                _describe -t commands "build commands" build_cmds
                ;;
            batch)
                local -a batch_cmds
                batch_cmds=(
                    'create:Create a batch of devices'
                    'list:List batches'
                    'get:Get batch status'
                    'devices:List batch devices'
                    'delete:Delete a batch'
                )
                _describe -t commands "batch commands" batch_cmds
                ;;
            operation)
                local -a operation_cmds
                operation_cmds=(
                    'get:Show an operation status'
                    'list:List operations newest first'
                )
                _describe -t commands "operation commands" operation_cmds
                ;;
            machine-type)
                local -a machine_type_cmds
                machine_type_cmds=(
                    'list:List the machine-type catalog'
                    'get:Get machine type details'
                    'default:Show the default machine type'
                    'families:List machine families'
                )
                _describe -t commands "machine-type commands" machine_type_cmds
                ;;
            zone)
                local -a zone_cmds
                zone_cmds=(
                    'list:List zones and their availability'
                )
                _describe -t commands "zone commands" zone_cmds
                ;;
            quota)
                local -a quota_cmds
                quota_cmds=(
                    'show:Show host-capacity quota and current usage'
                    'set:Set the platform host quota'
                )
                _describe -t commands "quota commands" quota_cmds
                ;;
            storage)
                local -a storage_cmds
                storage_cmds=(
                    'ls:List buckets or a bucket'\''s objects'
                    'rm:Delete an object from a bucket'
                )
                _describe -t commands "storage commands" storage_cmds
                ;;
            admin)
                local -a admin_cmds
                admin_cmds=(
                    'tasks:Task-queue operations'
                    'config:Platform settings (system-config)'
                    'stats:Platform statistics'
                    'provisioning:Recent provisioning outcomes'
                    'reclaimable:Resources an operator can reclaim'
                    'audit:Query the audit log'
                    'capacity:Where the fleet can grow, and the quota behind it'
                    'control-plane:Queue, live work and leadership snapshot'
                    'zone:Curate the placement zones: catalog enablement and capacity cooldowns'
                    'machine-type:Curate the machine-type catalog'
                    'catalog-freshness:Per-region catalog sync state'
                    'host-image:Curate machine-family image mappings'
                )
                _describe -t commands "admin commands" admin_cmds
                ;;
            completion)
                local -a comp_cmds
                comp_cmds=(
                    'bash:Generate bash completion script'
                    'zsh:Generate zsh completion script'
                    'fish:Generate fish completion script'
                )
                _describe -t commands "completion commands" comp_cmds
                ;;
        esac
    elif (( CURRENT == 4 )); then
        if [[ "$words[2]" == "admin" && "$words[3]" == "tasks" ]]; then
            local -a admin_tasks_cmds
            admin_tasks_cmds=(
                'list:List dead-lettered tasks'
                'retry:Retry a dead-lettered task'
                'delete:Delete a task from the queue'
                'stats:Show task-queue depth'
            )
            _describe -t commands "admin tasks commands" admin_tasks_cmds
        elif [[ "$words[2]" == "admin" && "$words[3]" == "config" ]]; then
            local -a admin_config_cmds
            admin_config_cmds=(
                'get:Read a platform setting (all when KEY is omitted)'
                'set:Write a platform setting'
            )
            _describe -t commands "admin config commands" admin_config_cmds
        elif [[ "$words[2]" == "admin" && "$words[3]" == "zone" ]]; then
            local -a admin_zone_cmds
            admin_zone_cmds=(
                'list:List all catalog zones, enabled or not'
                'enable:Enable a zone for host creation'
                'disable:Disable a zone for host creation'
                'clear-cooldown:Lift a capacity cooldown steering placement away from a zone'
            )
            _describe -t commands "admin zone commands" admin_zone_cmds
        elif [[ "$words[2]" == "admin" && "$words[3]" == "machine-type" ]]; then
            local -a admin_mt_cmds
            admin_mt_cmds=(
                'update:Change enablement, the default, or display order'
            )
            _describe -t commands "admin machine-type commands" admin_mt_cmds
        elif [[ "$words[2]" == "admin" && "$words[3]" == "host-image" ]]; then
            local -a admin_hi_cmds
            admin_hi_cmds=(
                'list:List the image mappings'
                'get:Show one machine family'\''s mapping'
                'create:Register an image mapping'
                'update:Change an image mapping'
                'delete:Remove an image mapping'
            )
            _describe -t commands "admin host-image commands" admin_hi_cmds
        elif [[ "$words[2]" == "device" && "$words[3]" == "record" ]]; then
            local -a device_record_cmds
            device_record_cmds=(
                'start:Start a screen recording'
                'stop:Stop a screen recording'
                'list:List host-local screen recordings'
                'fetch:Archive a recording to storage and download it'
            )
            _describe -t commands "device record commands" device_record_cmds
        elif [[ "$words[2]" == "device" && "$words[3]" == "display" ]]; then
            local -a device_display_cmds
            device_display_cmds=(
                'list:List displays and available presets'
                'add:Add a display to a running device'
                'remove:Remove a display from a device'
            )
            _describe -t commands "device display commands" device_display_cmds
        elif [[ "$words[2]" == "device" && "$words[3]" == "snapshot" ]]; then
            local -a device_snapshot_cmds
            device_snapshot_cmds=(
                'take:Capture a running device onto its host'
                'list:List a device'\''s snapshots'
                'delete:Delete a snapshot'
                'restore:Start a stopped device from a snapshot'
            )
            _describe -t commands "device snapshot commands" device_snapshot_cmds
        elif [[ "$words[2]" == "config" && "$words[3]" == (use|delete|rename) ]]; then
            compadd -- $(cfctl completion values profile 2>/dev/null)
        elif [[ "$words[2]" == "host" && "$words[3]" == (get|delete|start|stop|retry|sync|update|extend|ssh|cvds) ]]; then
            compadd -- $(cfctl completion values host 2>/dev/null)
        elif [[ "$words[2]" == "device" && "$words[3]" == (get|delete|start|stop|restart|powerwash|powerbtn|extend|pin|unpin|lifecycle|screenshot|bugreport|connect|disconnect|webrtc) ]]; then
            compadd -- $(cfctl completion values device 2>/dev/null)
        elif [[ "$words[2]" == "machine-type" && "$words[3]" == "get" ]]; then
            compadd -- $(cfctl completion values machine-type 2>/dev/null)
        fi
    elif (( CURRENT == 5 )); then
        if [[ "$words[2]" == "admin" && "$words[3]" == "config" ]]; then
            compadd -- zone-pin quota-margin v1-zone-hint spot-auto-default default-gpu-machine-type default-upstream-build spot-min-uptime lifetimes docker-host artifact-buckets sweeps fleet
        fi
    fi
}

_cfctl "$@"
`

// Run prints the zsh completion script.
func (c *CompletionZshCmd) Run() error {
	fmt.Print(zshCompletionScript)
	return nil
}

const fishCompletionScript = `# cfctl fish completion
# Add to ~/.config/fish/completions/cfctl.fish: cfctl completion fish > ~/.config/fish/completions/cfctl.fish

function __fish_cfctl_needs_command
    set cmd (commandline -opc)
    if [ (count $cmd) -eq 1 ]
        return 0
    end
    return 1
end

function __fish_cfctl_using_command
    set cmd (commandline -opc)
    if [ (count $cmd) -ge 2 ]
        if [ $cmd[2] = $argv[1] ]
            return 0
        end
    end
    return 1
end

function __fish_cfctl_using_subcommand
    set cmd (commandline -opc)
    if [ (count $cmd) -ge 3 ]
        if [ $cmd[2] = $argv[1] ]; and [ $cmd[3] = $argv[2] ]
            return 0
        end
    end
    return 1
end

# True when completing the ID argument of "<noun> <verb>" for one of the
# given verbs: __fish_cfctl_id_position <noun> <verb>...
function __fish_cfctl_id_position
    set cmd (commandline -opc)
    if [ (count $cmd) -eq 3 ]; and [ $cmd[2] = $argv[1] ]; and contains $cmd[3] $argv[2..-1]
        return 0
    end
    return 1
end

# True when completing the KEY argument of "admin config get|set".
function __fish_cfctl_config_key_position
    set cmd (commandline -opc)
    if [ (count $cmd) -eq 4 ]; and [ $cmd[2] = admin ]; and [ $cmd[3] = config ]; and contains $cmd[4] get set
        return 0
    end
    return 1
end

# Main commands
complete -f -c cfctl -n '__fish_cfctl_needs_command' -a setup -d 'Interactive setup wizard'
complete -f -c cfctl -n '__fish_cfctl_needs_command' -a auth -d 'Authentication commands'
complete -f -c cfctl -n '__fish_cfctl_needs_command' -a config -d 'Manage CLI configuration'
complete -f -c cfctl -n '__fish_cfctl_needs_command' -a host -d 'Manage host VMs'
complete -f -c cfctl -n '__fish_cfctl_needs_command' -a device -d 'Manage Cuttlefish devices'
complete -f -c cfctl -n '__fish_cfctl_needs_command' -a build -d 'Manage the build registry'
complete -f -c cfctl -n '__fish_cfctl_needs_command' -a batch -d 'Manage batches of devices (CI/fleet)'
complete -f -c cfctl -n '__fish_cfctl_needs_command' -a operation -d 'Inspect long-running operations'
complete -f -c cfctl -n '__fish_cfctl_needs_command' -a machine-type -d 'Browse the machine-type catalog'
complete -f -c cfctl -n '__fish_cfctl_needs_command' -a zone -d 'Browse the zone catalog'
complete -f -c cfctl -n '__fish_cfctl_needs_command' -a quota -d 'Host-capacity quota'
complete -f -c cfctl -n '__fish_cfctl_needs_command' -a storage -d 'Browse the artifact buckets'
complete -f -c cfctl -n '__fish_cfctl_needs_command' -a admin -d 'Admin operations'
complete -f -c cfctl -n '__fish_cfctl_needs_command' -a status -d 'Check server health'
complete -f -c cfctl -n '__fish_cfctl_needs_command' -a doctor -d 'Diagnose connectivity, auth, quota and storage'
complete -f -c cfctl -n '__fish_cfctl_needs_command' -a completion -d 'Generate shell completion scripts'

# Auth commands
complete -f -c cfctl -n '__fish_cfctl_using_command auth' -a login -d 'Set up Application Default Credentials'
complete -f -c cfctl -n '__fish_cfctl_using_command auth' -a whoami -d 'Show current user information'
complete -f -c cfctl -n '__fish_cfctl_using_command auth' -a logout -d 'Delete cached tokens and revoke ADC'
complete -f -c cfctl -n '__fish_cfctl_using_command auth' -a status -d 'Show authentication status'

# Config commands
complete -f -c cfctl -n '__fish_cfctl_using_command config' -a show -d 'Show each effective setting and its source'
complete -f -c cfctl -n '__fish_cfctl_using_command config' -a profiles -d 'List profiles, marking the current one'
complete -f -c cfctl -n '__fish_cfctl_using_command config' -a use -d 'Make a profile the current one'
complete -f -c cfctl -n '__fish_cfctl_using_command config' -a create -d 'Create a profile without prompts'
complete -f -c cfctl -n '__fish_cfctl_using_command config' -a delete -d 'Delete a profile'
complete -f -c cfctl -n '__fish_cfctl_using_command config' -a rename -d 'Rename a profile'
complete -f -c cfctl -n '__fish_cfctl_using_command config' -a set-server -d 'Set the orchestrator API server URL'
complete -f -c cfctl -n '__fish_cfctl_using_command config' -a set-audience -d 'Set the IAP OAuth Client ID (ID token audience)'
complete -f -c cfctl -n '__fish_cfctl_using_command config' -a set-auth -d 'Set the client auth mode (auto or none)'
complete -f -c cfctl -n '__fish_cfctl_using_command config' -a set-credentials -d 'Set path to Google Service Account key JSON'
complete -f -c cfctl -n '__fish_cfctl_using_command config' -a clear-credentials -d 'Clear stored credentials path'
complete -f -c cfctl -n '__fish_cfctl_using_command config' -a set-impersonate -d 'Set service account to impersonate'
complete -f -c cfctl -n '__fish_cfctl_using_command config' -a clear-impersonate -d 'Clear service account impersonation'

# Host commands
complete -f -c cfctl -n '__fish_cfctl_using_command host' -a list -d 'List all hosts'
complete -f -c cfctl -n '__fish_cfctl_using_command host' -a get -d 'Get host details'
complete -f -c cfctl -n '__fish_cfctl_using_command host' -a create -d 'Create a new host VM'
complete -f -c cfctl -n '__fish_cfctl_using_command host' -a delete -d 'Delete a host VM'
complete -f -c cfctl -n '__fish_cfctl_using_command host' -a start -d 'Start a host VM'
complete -f -c cfctl -n '__fish_cfctl_using_command host' -a stop -d 'Stop a host VM'
complete -f -c cfctl -n '__fish_cfctl_using_command host' -a retry -d 'Re-provision a preempted or failed host'
complete -f -c cfctl -n '__fish_cfctl_using_command host' -a sync -d 'Reconcile a host against GCE now'
complete -f -c cfctl -n '__fish_cfctl_using_command host' -a update -d 'Change how many devices a host accepts'
complete -f -c cfctl -n '__fish_cfctl_using_command host' -a extend -d 'Extend a reserved host lease'
complete -f -c cfctl -n '__fish_cfctl_using_command host' -a ssh -d 'SSH into a host VM'
complete -f -c cfctl -n '__fish_cfctl_using_command host' -a cvds -d 'List every CVD on a host, including ones created by cvdr'

# Device commands
complete -f -c cfctl -n '__fish_cfctl_using_command device' -a list -d 'List all devices'
complete -f -c cfctl -n '__fish_cfctl_using_command device' -a get -d 'Get device details'
complete -f -c cfctl -n '__fish_cfctl_using_command device' -a create -d 'Create a new device'
complete -f -c cfctl -n '__fish_cfctl_using_command device' -a delete -d 'Delete a device'
complete -f -c cfctl -n '__fish_cfctl_using_command device' -a start -d 'Start a device'
complete -f -c cfctl -n '__fish_cfctl_using_command device' -a stop -d 'Stop a device'
complete -f -c cfctl -n '__fish_cfctl_using_command device' -a restart -d 'Restart a device in place'
complete -f -c cfctl -n '__fish_cfctl_using_command device' -a powerwash -d 'Reset a device to first-boot state'
complete -f -c cfctl -n '__fish_cfctl_using_command device' -a powerbtn -d 'Press the power button'
complete -f -c cfctl -n '__fish_cfctl_using_command device' -a extend -d 'Extend a device TTL'
complete -f -c cfctl -n '__fish_cfctl_using_command device' -a pin -d 'Fence a device from reclamation'
complete -f -c cfctl -n '__fish_cfctl_using_command device' -a unpin -d 'Return a device to normal reclamation'
complete -f -c cfctl -n '__fish_cfctl_using_command device' -a lifecycle -d 'Show TTL, expiry and pin state'
complete -f -c cfctl -n '__fish_cfctl_using_command device' -a screenshot -d 'Capture a display screenshot'
complete -f -c cfctl -n '__fish_cfctl_using_command device' -a bugreport -d 'Capture a bugreport to the artifact bucket'
complete -f -c cfctl -n '__fish_cfctl_using_command device' -a record -d 'Manage screen recordings'
complete -f -c cfctl -n '__fish_cfctl_using_command device' -a snapshot -d 'Manage device snapshots'
complete -f -c cfctl -n '__fish_cfctl_using_command device' -a display -d 'Manage a device displays'
complete -f -c cfctl -n '__fish_cfctl_using_command device' -a connect -d 'Connect to device via ADB'
complete -f -c cfctl -n '__fish_cfctl_using_command device' -a disconnect -d 'Stop background ADB connections'
complete -f -c cfctl -n '__fish_cfctl_using_command device' -a connections -d 'List background ADB connections'
complete -f -c cfctl -n '__fish_cfctl_using_command device' -a webrtc -d 'Open device WebRTC in browser'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand device record' -a start -d 'Start a screen recording'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand device record' -a stop -d 'Stop a screen recording'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand device record' -a list -d 'List host-local screen recordings'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand device record' -a fetch -d 'Archive a recording to storage and download it'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand device snapshot' -a take -d 'Capture a running device onto its host'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand device snapshot' -a list -d 'List a device'\''s snapshots'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand device snapshot' -a delete -d 'Delete a snapshot'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand device snapshot' -a restore -d 'Start a stopped device from a snapshot'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand device display' -a list -d 'List displays and available presets'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand device display' -a add -d 'Add a display to a running device'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand device display' -a remove -d 'Remove a display from a device'

# Batch commands
complete -f -c cfctl -n '__fish_cfctl_using_command build' -a push -d 'Push local build artifacts and register them'
complete -f -c cfctl -n '__fish_cfctl_using_command build' -a register -d 'Register an Android CI build by build ID and target'
complete -f -c cfctl -n '__fish_cfctl_using_command build' -a list -d 'List registered builds'
complete -f -c cfctl -n '__fish_cfctl_using_command build' -a get -d 'Get build details'
complete -f -c cfctl -n '__fish_cfctl_using_command build' -a update -d 'Change name, description, default or artifacts'
complete -f -c cfctl -n '__fish_cfctl_using_command build' -a delete -d 'Delete a build from the registry'
complete -f -c cfctl -n '__fish_cfctl_using_command batch' -a create -d 'Create a batch of devices'
complete -f -c cfctl -n '__fish_cfctl_using_command batch' -a list -d 'List batches'
complete -f -c cfctl -n '__fish_cfctl_using_command batch' -a get -d 'Get batch status'
complete -f -c cfctl -n '__fish_cfctl_using_command batch' -a devices -d 'List batch devices'
complete -f -c cfctl -n '__fish_cfctl_using_command batch' -a delete -d 'Delete a batch'

# Operation commands
complete -f -c cfctl -n '__fish_cfctl_using_command operation' -a get -d 'Show an operation status'
complete -f -c cfctl -n '__fish_cfctl_using_command operation' -a list -d 'List operations newest first'

# Machine-type commands
complete -f -c cfctl -n '__fish_cfctl_using_command machine-type' -a list -d 'List the machine-type catalog'
complete -f -c cfctl -n '__fish_cfctl_using_command machine-type' -a get -d 'Get machine type details'
complete -f -c cfctl -n '__fish_cfctl_using_command machine-type' -a default -d 'Show the default machine type'
complete -f -c cfctl -n '__fish_cfctl_using_command machine-type' -a families -d 'List machine families'

# Zone commands
complete -f -c cfctl -n '__fish_cfctl_using_command zone' -a list -d 'List zones and their availability'

# Quota commands
complete -f -c cfctl -n '__fish_cfctl_using_command quota' -a show -d 'Show host-capacity quota and current usage'
complete -f -c cfctl -n '__fish_cfctl_using_command quota' -a set -d 'Set the platform host quota'

# Storage commands
complete -f -c cfctl -n '__fish_cfctl_using_command storage' -a ls -d 'List buckets or a bucket'\''s objects'
complete -f -c cfctl -n '__fish_cfctl_using_command storage' -a rm -d 'Delete an object from a bucket'

# Admin commands
complete -f -c cfctl -n '__fish_cfctl_using_command admin' -a tasks -d 'Dead-letter queue operations'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand admin tasks' -a list -d 'List dead-lettered tasks'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand admin tasks' -a retry -d 'Retry a dead-lettered task'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand admin tasks' -a delete -d 'Delete a task from the queue'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand admin tasks' -a stats -d 'Show task-queue depth'
complete -f -c cfctl -n '__fish_cfctl_using_command admin' -a stats -d 'Platform statistics'
complete -f -c cfctl -n '__fish_cfctl_using_command admin' -a provisioning -d 'Recent provisioning outcomes'
complete -f -c cfctl -n '__fish_cfctl_using_command admin' -a reclaimable -d 'Resources an operator can reclaim'
complete -f -c cfctl -n '__fish_cfctl_using_command admin' -a audit -d 'Query the audit log'
complete -f -c cfctl -n '__fish_cfctl_using_command admin' -a capacity -d 'Where the fleet can grow, and the quota behind it'
complete -f -c cfctl -n '__fish_cfctl_using_command admin' -a control-plane -d 'Queue, live work and leadership snapshot'
complete -f -c cfctl -n '__fish_cfctl_using_command admin' -a zone -d 'Curate the placement zones: catalog enablement and capacity cooldowns'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand admin zone' -a list -d 'List all catalog zones, enabled or not'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand admin zone' -a enable -d 'Enable a zone for host creation'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand admin zone' -a disable -d 'Disable a zone for host creation'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand admin zone' -a clear-cooldown -d 'Lift a capacity cooldown steering placement away from a zone'
complete -f -c cfctl -n '__fish_cfctl_using_command admin' -a machine-type -d 'Curate the machine-type catalog'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand admin machine-type' -a update -d 'Change enablement, the default, or display order'
complete -f -c cfctl -n '__fish_cfctl_using_command admin' -a catalog-freshness -d 'Per-region catalog sync state'
complete -f -c cfctl -n '__fish_cfctl_using_command admin' -a host-image -d 'Curate machine-family image mappings'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand admin host-image' -a list -d 'List the image mappings'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand admin host-image' -a get -d 'Show one machine family'\''s mapping'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand admin host-image' -a create -d 'Register an image mapping'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand admin host-image' -a update -d 'Change an image mapping'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand admin host-image' -a delete -d 'Remove an image mapping'
complete -f -c cfctl -n '__fish_cfctl_using_command admin' -a config -d 'Platform settings (system-config)'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand admin config' -a get -d 'Read a platform setting (all when KEY is omitted)'
complete -f -c cfctl -n '__fish_cfctl_using_subcommand admin config' -a set -d 'Write a platform setting'
complete -f -c cfctl -n '__fish_cfctl_config_key_position' -a 'zone-pin quota-margin v1-zone-hint spot-auto-default default-gpu-machine-type default-upstream-build spot-min-uptime lifetimes docker-host artifact-buckets sweeps fleet'

# Completion commands
complete -f -c cfctl -n '__fish_cfctl_using_command completion' -a bash -d 'Generate bash completion script'
complete -f -c cfctl -n '__fish_cfctl_using_command completion' -a zsh -d 'Generate zsh completion script'
complete -f -c cfctl -n '__fish_cfctl_using_command completion' -a fish -d 'Generate fish completion script'

# Server-backed values, cached in the config directory by 'completion values'
complete -f -c cfctl -n '__fish_cfctl_id_position host get delete start stop retry sync update extend ssh cvds' -a '(cfctl completion values host 2>/dev/null)'
complete -f -c cfctl -n '__fish_cfctl_id_position device get delete start stop restart powerwash powerbtn extend pin unpin lifecycle screenshot bugreport connect disconnect webrtc' -a '(cfctl completion values device 2>/dev/null)'
complete -f -c cfctl -n '__fish_cfctl_id_position machine-type get' -a '(cfctl completion values machine-type 2>/dev/null)'
complete -c cfctl -n '__fish_cfctl_using_subcommand host create' -l machine-type -x -a '(cfctl completion values machine-type 2>/dev/null)'
complete -c cfctl -n '__fish_cfctl_using_subcommand host create' -l zone -x -a '(cfctl completion values zone 2>/dev/null)'
complete -c cfctl -n '__fish_cfctl_using_subcommand device create' -l host -x -a '(cfctl completion values host 2>/dev/null)'
complete -c cfctl -n '__fish_cfctl_using_subcommand device list' -l host -x -a '(cfctl completion values host 2>/dev/null)'
complete -f -c cfctl -n '__fish_cfctl_id_position config use delete rename' -a '(cfctl completion values profile 2>/dev/null)'
complete -c cfctl -l profile -x -a '(cfctl completion values profile 2>/dev/null)'
`

// Run prints the fish completion script.
func (c *CompletionFishCmd) Run() error {
	fmt.Print(fishCompletionScript)
	return nil
}
