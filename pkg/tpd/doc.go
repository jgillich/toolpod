// Package tpd is the launch engine for the tpd CLI.
//
// It is in-module-only, not a public library API. The package exists to keep
// cmd/tpd thin and to make launch logic unit-testable; its exported names
// (Launch, LaunchOpts, Result, PortAllocator) are used by the CLI and by the
// package's own tests. The spec types it operates on (Spec,
// MountSpec, PortSpec, ...) live in internal/runtime and are deliberately not
// re-exported here — the type aliases that once surfaced them implied external
// consumers could construct them, so they were removed. The internal/ rule
// enforces the boundary: an external module cannot import internal/runtime,
// internal/approval, or internal/workspace, so it cannot name the spec types,
// implement the Runtime/Progress/approval interfaces, or satisfy
// LaunchOpts.Runtime/.Progress/.ApprovalStore/.ApprovalPrompt. LaunchOpts
// fields that carry only public types (ProfileName, Workspace, PortAllocator,
// ...) are the entire usable surface from outside the module. The fixture
// module under pkg/tpd/testdata/externalconsumer proves this by compiling an
// external consumer against the package and asserting the internal surface is
// unreachable.
package tpd
