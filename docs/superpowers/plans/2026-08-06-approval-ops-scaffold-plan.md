# Approval, Prune, Doctor, Scaffold, and Mise Reliability Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` or `superpowers:executing-plans` to implement this plan task-by-task.
**Goal:** Prevent state corruption/leaks and make maintenance commands and initialization reliable under concurrency, active sessions, and malformed input.

**Architecture:** Treat approval files and generated profiles as transactional state, make prune/doctor fail closed with bounded contexts, and install mise plugins through atomic staged updates.

**Tech Stack:** Go, YAML, advisory file locks, Docker SDK, Bubble Tea/Huh, Lua plugin hooks, race-enabled tests.

**Scope:** I49–I52, I54–I56, the cache-volume portion of I57, I58–I61, I63–I73, M43–M46, M49, M51–M55, and worthwhile testing gaps. The derived-image portion of I57 is deferred because no offline mapping exists from an unavailable base image to a tag.

## Task 1: Make approval storage atomic and recoverable

**Files:** `internal/approval/store.go`, `internal/approval/approval.go`, `pkg/tpd/launch.go`, `internal/approval/prompt.go`, tests.

- Serialize the entire Load → Filter → prompt/choice merge → Save transaction in `pkg/tpd/launch.go` with an advisory lock on a stable sibling lock file. Do not lock the renamed state file.
- Write using `os.CreateTemp` in the approval directory, set mode 0600, `fsync`, and rename atomically; clean up temp files on all failures.
- Reject symlinked targets with `Lstat`/safe open semantics.
- On malformed YAML, return an error naming the profile/path and an explicit repair command; retain the fail-closed exit-2 behavior. Do not claim automatic recovery through `--yes`/`--no`, since filtering happens first.
- Preserve already approved choices and default only newly introduced sensitive items to unselected. Extend `PromptRequest` with prior approved keys so the UI can make that distinction; add mapping/state tests.

## Task 2: Make prune fail closed and protect active resources

**Files:** `internal/prune/prune.go`, prune tests.

- Track removal failures in `Result` and return an error/nonzero result when any requested removal fails.
- Inspect running, created, paused, and restarting containers. Protect resources referenced by any such container (regardless of labels when it mounts a tpd-named volume/image).
- Treat inspect/list races as a conservative global “cannot establish resource use” error: perform no removals and return nonzero. Never skip an uninspectable live candidate and continue pruning.
- Preserve cache volumes for profiles that fail to resolve or are skipped by tolerant loading. Defer derived-image retention when the base ID is unavailable: current tags deliberately incorporate that unavailable ID, so no correct offline association can be computed.
- Combine volume/image confirmation into one prompt when both scopes are selected.

## Task 3: Correct doctor checks and lifecycle cleanup

**Files:** `internal/doctor/doctor.go`, `internal/doctor/checks.go`, doctor tests.

- Use a bounded context/deadline and short-circuit dependent checks after runtime failure.
- Use the daemon-host parser introduced by the runtime plan; report unsupported remote schemes clearly.
- Do not attempt to infer “active session” from a bare labeled main container. Report running/created/paused containers as active resources, and report only exited/dead owned containers as leaks; use the configured engine name in remediation text.
- Label diagnostic resources consistently or guarantee cleanup with deferred removal; include failed probe cleanup in the report.
- Parse `mise.toml` errors as failures/warnings instead of silently reporting no tools.
- Distinguish active dbus proxy sockets from stale sockets by checking owning process/container metadata.

## Task 4: Make init/scaffold behavior deterministic

**Files:** `internal/scaffold/scaffold.go`, `internal/scaffold/merge.go`, `cmd/tpd/cli.go`, scaffold tests.

- Require both stdin and stdout to be TTYs before launching the huh/bubbletea UI.
- Enforce `--force`/`--merge` mutual exclusion at the start of `Run`, before dry-run or target existence branches, and distinguish permission errors from missing files.
- Fix incomplete-profile detection for missing image/command cases and avoid mutating the caller’s catalog when validating generated content.
- Write generated YAML atomically; validate scalar `extends`, tabs, and merged content explicitly.
- Add tests for redirected stdout, missing targets, permission failures, dry-run existing targets, and catalog non-mutation.

## Task 5: Make the mise AppImage plugin safe

**Files:** `internal/mise/plugins.go`, `internal/mise/plugins/appimage/hooks/backend_install.lua`, `backend_list_versions.lua`, Lua/plugin tests.

- Install embedded plugin files atomically into a versioned sibling directory, then update a single pointer/symlink only after a complete write; skip work when an embedded content marker matches. Quote `$HOME` in generated shell.
- Verify the actual mise/vfox Lua HTTP API before changing calls; use its documented non-throwing request variant (or wrap the throwing call with `pcall`) and check status codes explicitly.
- Validate `exe` and `name` before download/extraction/install-path mutation.
- Preserve the bundled `xdg-open` on swap failure and add Linux-only guards.
- Add Lua hook execution tests only if a compatible pure-Go Lua VM can run under CGO-disabled builds. Otherwise add fixture-driven static tests for generated plugin contents and a mise integration test behind an explicit tool/daemon gate; do not introduce CGO/mlua solely for tests.

## Task 6: Verify the operations track

Run:

```bash
GOCACHE=/tmp/tpd-gocache go test ./internal/approval ./internal/prune ./internal/doctor ./internal/scaffold ./internal/mise ./cmd/tpd ./pkg/tpd
GOCACHE=/tmp/tpd-gocache go vet ./internal/approval ./internal/prune ./internal/doctor ./internal/scaffold ./internal/mise ./cmd/tpd ./pkg/tpd
```

Add race-enabled approval tests with `go test -race ./internal/approval`; run engine-backed doctor/prune/service cleanup tests when the daemon is available.
