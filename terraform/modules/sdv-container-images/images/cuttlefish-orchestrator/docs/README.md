# Documentation

Welcome to the Cloud Cuttlefish Orchestrator documentation.

## Getting Started

New to CCO? Start here:

- [Quick Start](getting-started/quick-start.md) - Deploy CCO from scratch
- [Deployment Modes](getting-started/deployment-modes.md) - the three topology variables: ingress × auth × device_ingress
- [Replacing Cloud Orchestrator](getting-started/replacing-cloud-orchestrator.md) - drop-in migration from the upstream service, clients unchanged
- [Deployment Checklist](getting-started/deployment-checklist.md) - Pre-deployment verification

## Concepts

Understand how CCO works:

- [Architecture](concepts/architecture.md) - System architecture and components
- [Authentication](concepts/authentication.md) - The two front doors, `iap` and `network`; browser and programmatic (cfctl/CI) auth
- [WebRTC Streaming](concepts/webrtc-streaming.md) - Device streaming and TURN relay

## Guides

Step-by-step instructions for common tasks:

- [Creating Devices](guides/creating-devices.md) - Create and manage virtual devices
- [Managing Hosts](guides/managing-hosts.md) - Host VM lifecycle
- [Building Host Images](guides/building-host-images.md) - Create custom host images
- [Using the CLI](guides/using-the-cli.md) - Command-line workflows
- [cfctl for cvdr Users](guides/cvdr-to-cfctl.md) - Every cvdr command and its cfctl equivalent
- [Testing Local Builds](guides/local-builds.md) - Push workstation builds, boot cloud devices from them
- [CI/CD Integration](guides/cicd-integration.md) - Workload Identity Federation setup

## Reference

Detailed technical reference:

- [API Reference](reference/api.md) - REST API documentation
- [Legacy API compatibility](reference/legacy-api.md) - The `/v1` byte-exact legacy CO/HO surface
- [CLI Reference](reference/cli.md) - Complete cfctl command reference

## Development

For contributors:

- [Code Architecture](development/code-architecture.md) - Codebase structure and patterns
- [Backend Guide](development/backend.md) - Go backend development
- [Control Plane](development/control-plane.md) - The durable task queue, workers, and reconciliation loops
- [Architecture Decisions](development/architecture-decisions.md) - The invariants the rest of the system depends on (isolation, ownership time)
- [Frontend Guide](development/frontend.md) - Angular frontend development
- [Testing Guide](development/testing.md) - Test layers, the Spanner emulator, and the fault-injection harnesses
- [Linting](development/linting.md) - The Go and TypeScript lint gates and how to run them
- [Legacy Compatibility Layer](development/legacy-compat-layer.md) - How the `/v1` legacy CO/HO surface is built internally

## Operations

For platform operators:

- [Fleet Autoscaler](operations/autoscaler.md) - Standing capacity: the target, the pools, and the one switch that stops it
- [Quotas](operations/quotas.md) - Resource quota management

## Quick Links

| Topic | Document |
|-------|----------|
| Deploy CCO | [Quick Start](getting-started/quick-start.md) |
| Create a device | [Creating Devices](guides/creating-devices.md) |
| Test your local build | [Testing Local Builds](guides/local-builds.md) |
| Set up CLI | [Using the CLI](guides/using-the-cli.md) |
| Understand auth | [Authentication](concepts/authentication.md) |
| API reference | [API Reference](reference/api.md) |
| Run the test harnesses | [Testing Guide](development/testing.md) |
| Turn on standing capacity | [Fleet Autoscaler](operations/autoscaler.md) |
