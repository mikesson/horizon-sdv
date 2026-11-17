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

package config

import "time"

// DeviceIngress selects where WebRTC device media meets the internet.
type DeviceIngress string

const (
	// DeviceIngressPublic gives host VMs external IPs so device media flows
	// directly from the host to clients (TURN optional).
	DeviceIngressPublic DeviceIngress = "public"

	// DeviceIngressPrivate keeps host VMs internal-only; clients reach device
	// media over a VPN or private network (TURN unnecessary).
	DeviceIngressPrivate DeviceIngress = "private"

	// DeviceIngressRelay keeps host VMs internal-only and relays device media
	// through a TURN server (TURN required).
	DeviceIngressRelay DeviceIngress = "relay"
)

// Database type constants.
const (
	// DatabaseTypeSpanner indicates Cloud Spanner database.
	DatabaseTypeSpanner = "spanner"
)

// AuthMode selects the orchestrator front-door authentication mechanism.
type AuthMode string

const (
	// AuthModeIAP authenticates via built-in Cloud Run IAP (the default).
	AuthModeIAP AuthMode = "iap"

	// AuthModeProxy trusts identity headers injected by an upstream
	// authenticating reverse proxy (such as oauth2-proxy on GKE Gateway).
	AuthModeProxy AuthMode = "proxy"

	// AuthModeNetwork performs no authentication: every caller is attributed
	// to one fixed synthetic principal, and network reachability (private
	// ingress and VPC membership, both Terraform-enforced) is the entire access
	// model. For legacy clients with no credential (OmniLab ATS).
	AuthModeNetwork AuthMode = "network"
)

// IsValid reports whether the device-ingress value is recognized.
func (m DeviceIngress) IsValid() bool {
	switch m {
	case DeviceIngressPublic, DeviceIngressPrivate, DeviceIngressRelay:
		return true
	default:
		return false
	}
}

// UsesExternalIP reports whether host VMs get external IPs. It is true only
// when device media meets the internet at the host (public).
func (m DeviceIngress) UsesExternalIP() bool {
	return m == DeviceIngressPublic
}

// IsValid reports whether the auth mode is recognized.
func (m AuthMode) IsValid() bool {
	switch m {
	case AuthModeIAP, AuthModeProxy, AuthModeNetwork:
		return true
	default:
		return false
	}
}

// Duration is a time.Duration under a distinct name, so timeout and interval
// fields read as durations rather than as bare integers. Every value is a code
// default; nothing parses one from text.
type Duration time.Duration

// Config holds all configuration for the orchestrator service.
type Config struct {
	// DeviceIngress selects where WebRTC device media meets the internet
	// (public/private/relay). It does not affect authentication or CCO↔host
	// traffic, which always uses internal IPs. Must be set explicitly.
	DeviceIngress DeviceIngress

	// Deployment names this install within its project (CCO_DEPLOYMENT). It
	// is stamped on every host VM and boot disk as the cco-deployment label,
	// so two installs in one project never treat each other's hosts as
	// orphans. Required.
	Deployment string

	// Server configuration
	Server ServerConfig

	// Auth configuration
	Auth AuthConfig

	// Database configuration
	Database DatabaseConfig

	// GCP configuration
	GCP GCPConfig

	// TURN configuration
	TURN TURNConfig

	// ICE configuration
	ICE ICEConfig

	// Reaper configuration for automatic resource cleanup
	Reaper ReaperConfig

	// CatalogSync configuration for GCE resource catalog synchronization
	CatalogSync CatalogSyncConfig

	// TaskWorker configuration for the durable task-queue worker pool
	TaskWorker TaskWorkerConfig

	// TaskReclaimer configuration for expired task-lease reclamation
	TaskReclaimer TaskReclaimerConfig

	// HostReconciler configuration for the GCE state reconciliation job
	HostReconciler HostReconcilerConfig

	// DeviceController configuration for the device health polling job
	DeviceController DeviceControllerConfig
}

// HostReconcilerConfig tunes GCE state reconciliation. Zones defaults to the distinct
// zones of live hosts (queried per cycle), so it normally never needs setting.
type HostReconcilerConfig struct {
	// Interval between sync cycles.
	Interval Duration

	// Zones overrides the synced zone list; empty means derive from hosts.
	Zones []string
}

// DeviceControllerConfig tunes device health polling.
type DeviceControllerConfig struct {
	// Interval between health check cycles.
	Interval Duration
}

// TaskWorkerConfig tunes the task-queue worker pool. Zero values (except
// Enabled) fall back to the defaults in jobs/taskworker. NumShards is the
// single source for the queue's shard fan-out: the enqueue side and the
// claiming workers both derive from it, so they cannot diverge (a mismatch
// would leave high shards enqueued but never claimed).
type TaskWorkerConfig struct {
	Enabled       bool
	MinWorkers    int
	MaxWorkers    int
	NumShards     int
	ClaimBatch    int
	LeaseDuration Duration
	HandleTimeout Duration
	PollMin       Duration
	PollMax       Duration
	RetryBase     Duration
	RetryMax      Duration
}

// TaskReclaimerConfig tunes expired task-lease reclamation (crash recovery).
type TaskReclaimerConfig struct {
	Enabled  bool
	Interval Duration
}

// ReaperConfig holds settings for automatic resource termination
type ReaperConfig struct {
	// Enabled controls whether the reaper service runs
	Enabled bool

	// Interval between reaper cycles
	Interval Duration

	// BatchSize is the maximum number of resources to process per cycle
	BatchSize int

	// DryRun logs what would be reaped without actually deleting
	DryRun bool
}

// CatalogSyncConfig holds settings for the Spot capacity-advice probe, the
// one catalog source that still writes rows. What Compute Engine offers is
// cached in memory and never synced.
type CatalogSyncConfig struct {
	// SpotAdvice enables the Spot capacity-advice sync (the beta GCE
	// advice.capacity API, probed per enabled zone × machine type). Default
	// off: advice is advisory, and placement degrades to quota/health signals
	// without it.
	SpotAdvice bool

	// SpotAdviceSyncInterval is how often to probe Spot capacity advice.
	SpotAdviceSyncInterval Duration
}

// ServerConfig holds HTTP server settings.
//
// ReadTimeout/WriteTimeout are enforced per request by middleware, with
// streaming and long-poll routes (legacy /v1, storage, signaling) exempt. A
// server-wide write timeout would kill the legacy `:wait` long-poll (55s)
// and large storage transfers. Only ReadHeaderTimeout and IdleTimeout are set
// on the http.Server itself.
type ServerConfig struct {
	// Address to listen on (e.g., ":8080")
	Address string

	// ReadHeaderTimeout bounds reading a request's headers (slowloris guard).
	ReadHeaderTimeout Duration

	// ReadTimeout is the per-request read deadline for non-streaming routes.
	ReadTimeout Duration

	// WriteTimeout is the per-request write deadline for ordinary API routes.
	WriteTimeout Duration

	// SlowRequestTimeout is the per-request deadline, BOTH read and write,
	// for routes whose handler waits out a Host Orchestrator operation (a CVD
	// relaunch or powerwash boots Android again). It is the effective bound on
	// those routes: the platform's own request timeout is an hour, set that
	// high so the streaming routes exempt from these deadlines are not cut off
	// mid-session, which leaves this the only thing ending an overrun.
	//
	// The read half is not optional: net/http reads the connection in the
	// background to notice a client going away, and an expired READ deadline
	// looks exactly like that, so it cancels the request context out from
	// under a handler that is only slow. Extending the write deadline alone
	// turns the connection break into a 500 and changes nothing else.
	SlowRequestTimeout Duration

	// IdleTimeout for keep-alive connections
	IdleTimeout Duration
}

// AuthConfig holds authentication settings. Authentication is the only
// access gate, and there are no roles.
type AuthConfig struct {
	// Mode selects the front-door mechanism: "iap" (the default), "proxy"
	// (identity headers from an upstream authenticating reverse proxy), or
	// "network" (no authentication; one synthetic principal).
	Mode AuthMode

	// IAPAudience is the expected audience of the Cloud Run IAP assertion:
	// /projects/PROJECT_NUMBER/locations/REGION/services/SERVICE_NAME
	IAPAudience string

	// TrustedProxies lists peer IPs or CIDRs allowed to supply proxy
	// identity headers when Mode is "proxy".
	TrustedProxies []string

	// ProxyDefaultUser is an optional fallback identity in proxy mode when
	// a trusted proxy omits identity headers. Empty (the default) fails closed.
	ProxyDefaultUser string

	// DevMode trusts plain identity headers for local development.
	// WARNING: never enable in production.
	DevMode bool

	// DevFallbackEmail is the identity assumed in DevMode when the request
	// carries no identity header (e.g., the local Angular dev server).
	DevFallbackEmail string
}

// ICEConfig holds the ICE settings that are not about TURN.
type ICEConfig struct {
	// StunServers are offered to whichever leg of a connection can reach the
	// public internet. Setting the variable replaces the default pair; an
	// empty value reads as unset, as it does for every list setting here.
	StunServers []string
}

// TURNConfig holds TURN server settings
type TURNConfig struct {
	// SharedSecret for HMAC authentication
	SharedSecret string

	// TTL for generated credentials. It is a session ceiling rather than a
	// refresh interval: the expiry is baked into the username, so renewing
	// means a new allocation, and no client CCO serves can re-gather without
	// rebuilding its peer connection. Long enough that a workday session never
	// reaches it. The revocation window is unaffected, because a relay's agent
	// re-asks the orchestrator on a much shorter ceiling, so a deleted device
	// stops relaying long before its credential lapses.
	TTL Duration

	// Servers is a list of TURN server URIs for the client leg, the browser
	// or CLI end of a connection. External addresses, and TURNS where a
	// certificate exists.
	Servers []string

	// DeviceServers is a list of TURN server URIs for the device leg, on
	// internal addresses. A relay-mode host has no external address and the
	// VPC has no NAT, so it can reach a relay only this way; the two lists
	// name the same instances by their two addresses.
	DeviceServers []string

	// Realm the TURN servers authenticate under. It is part of the long-term
	// key, so a value that disagrees with the servers' own realm rejects every
	// credential. Terraform sets both sides from one variable.
	Realm string
}

// DatabaseConfig holds database connection settings
type DatabaseConfig struct {
	// Type is the database type. Cloud Spanner ("spanner") is the only
	// supported backend; Validate rejects anything else.
	Type string

	// Spanner-specific settings
	Spanner SpannerConfig
}

// SpannerConfig holds Cloud Spanner connection settings
type SpannerConfig struct {
	// Project is the GCP project ID
	Project string

	// Instance is the Spanner instance name
	Instance string

	// Database is the Spanner database name
	Database string

	// FullDatabasePath returns the full database resource path
	FullDatabasePath string
}

// GCPConfig holds GCP-specific settings
type GCPConfig struct {
	// ProjectID is the GCP project ID
	ProjectID string

	// HostOrchestratorPort is the port the host orchestrator listens on
	HostOrchestratorPort int

	// Network is the VPC network for host VMs
	Network string

	// Subnetwork is the subnetwork for host VMs
	Subnetwork string

	// HostServiceAccount is the service account email for host VMs.
	// This SA should have access to GCS buckets containing build artifacts.
	// If empty, VMs are created without a service account (limited GCS access).
	HostServiceAccount string

	// HostImageProject is the GCP project host image families resolve in
	// (CCO_HOST_IMAGE_PROJECT), unless a host image config row names its own
	// source_project. Required: there is no default image project.
	HostImageProject string

	// ArtifactsBucket is the deployment-default GCS bucket for device
	// artifacts (bugreports, screen recordings). The per-type system_config
	// keys override it at runtime; with neither set, artifact capture is
	// rejected as unconfigured.
	ArtifactsBucket string

	// AllowSelfSignedHostSSLCert allows self-signed certs on the Host
	// Orchestrator's HTTPS endpoint (the cco-host image serves a self-signed
	// cert on the operator TLS port).
	AllowSelfSignedHostSSLCert bool
}
