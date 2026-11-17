// Separate module on purpose: unexportcheck needs golang.org/x/tools, and a
// lint tool must not become a dependency of the orchestrator binary.
module cloud-cuttlefish-orchestrator/tools/unexportcheck

go 1.26

require golang.org/x/tools v0.48.0

require (
	golang.org/x/mod v0.38.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
)
