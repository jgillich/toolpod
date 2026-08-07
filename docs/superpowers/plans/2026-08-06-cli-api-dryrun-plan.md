# CLI, Public API, and Dry-Run Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` or `superpowers:executing-plans` to implement this plan task-by-task.
**Goal:** Make dry-run output faithful, CLI errors deterministic, and the public launch API usable and writer-safe.

**Architecture:** Keep dry-run engine-free as specified: construct a deterministic preview with explicit “mode unknown” semantics rather than querying a daemon. Share pure spec construction where possible, keep runtime-only mutations visible as omitted, and separate stdout container output from an injectable diagnostics writer.

**Tech Stack:** Go, Cobra, Docker runtime interfaces, `pkg/tpd`, subprocess and unit tests.

**Scope:** I31–I39, I41–I44, I46–I47, plus worthwhile API/test gaps. Excludes I40 (exit-code convention preference), I45 (library-only `ExtraTools`), and I48: dry-run must remain fail-closed for unapproved sensitive fields.

## Task 1: Make engine-free dry-run deterministic and render the full static spec

**Files:** `pkg/tpd/launch.go`, `pkg/tpd/dryrun.go`, `pkg/tpd/spec.go`, tests.

- Preserve the no-engine dry-run contract. Add an explicit preview mode (`unknown`) and do not claim rootful paths, rootful runtime home, or a rootful workspace target when no daemon is queried.
- Factor only pure profile-to-spec construction into shared helpers. Render static fields (repos, files, TTY, declared ports/devices/services); explicitly omit runtime-only values such as derived image ID, DBUS proxy address, service socket host paths, and engine mode.
- Require a deterministic injected `PortAllocator` in dry-run tests; do not gold-test randomly allocated host ports.
- Align port rendering with the runtime plan: an omitted host IP renders as the concrete loopback default, while explicit `0.0.0.0` remains wildcard.

## Task 2: Fix CLI argument and workspace errors

**Files:** `cmd/tpd/cli.go`, CLI tests.

- Return a nonzero usage error when bare `tpd`/`tpd run` has no profile.
- Validate `os.Getwd()` errors and ensure `--workspace` exists and is a directory before launch.
- Define and test `--command` plus passthrough-argument behavior: reject the ambiguous combination with a usage error, or support it as `sh -c <command> -- <args>`; select one after reviewing Cobra’s argument boundary and document it.
- Add command-name collision handling without silently breaking existing `run`/`show`/`edit`/`list` profiles: either make explicit subcommands win and retain bare profile access through `tpd run <profile>`, or introduce a migration warning before reserving names.

## Task 3: Honor injected writers and make completion consistent

**Files:** `pkg/tpd/launch.go`, `pkg/tpd/dbusproxy.go`, `pkg/tpd/types.go`, `cmd/tpd/completion.go`, tests.

- Add an explicit diagnostics/stderr writer to `LaunchOpts`; preserve `Launch` container stdout on `os.Stdout` and send spinner/progress/warnings there. `LaunchWithWriter` continues to use its writer for rendered preview output without corrupting container stdout.
- Keep file completion after the profile positional: those arguments belong to the contained command, and file completion is useful rather than a contract violation. Correct tests/documentation that claim “no completions.”
- Add writer-capture and completion tests.

## Task 4: Harden mode detection and helpers

**Files:** `pkg/tpd/launch.go`, `pkg/tpd/types.go`, tests.

- Add a small optional `ModeDetector` interface for custom runtimes. In real launches, propagate a detector failure instead of silently falling back to rootful; in dry-run, do not instantiate/detect a runtime.
- Make `defaultApprovalDir` absolute with a documented fallback; validate tool flag syntax and reject malformed `name=version` values rather than silently inventing `latest`.
- Document that auto-port allocation is necessarily best-effort because the engine cannot consume the caller’s reserved socket. Keep the existing bind-then-close allocator and add a retry/error-message path only if the engine reports an auto-port bind collision; do not promise TOCTOU elimination.

## Task 5: Make built-in editing transactional

**Files:** `cmd/tpd/cli.go`, CLI tests.

- When `runEdit` seeds a built-in shadow, register cleanup immediately after the write. If the editor returns an error, remove the unchanged seed before returning that editor error.
- Preserve a seed that differs from its original bytes, including when an editor writes then returns a nonzero status.
- Add tests for editor failure without a write, editor failure after a write, and normal no-edit cleanup.

## Task 6: Decide public API boundaries

**Files:** `pkg/tpd/types.go`, exported adapter types, package docs/tests.

- Replace aliases to `internal` types with public structs/interfaces, or explicitly mark the package in-module-only and remove the implication of external construction.
- Keep dependency injection for runtime, progress, approval, and port allocation usable by external callers if the API remains public.
- Add a temporary separate Go module in a test fixture and run `go test` there with a replace directive to this repository; this proves consumers cannot rely on `internal/` types.

## Task 7: Verify the CLI track

Run:

```bash
GOCACHE=/tmp/tpd-gocache go test ./pkg/tpd ./cmd/tpd ./internal/runtime
GOCACHE=/tmp/tpd-gocache go vet ./pkg/tpd ./cmd/tpd ./internal/runtime
```

Exercise engine-free `tpd --dry-run`, `tpd run`, completion scripts, and deleted/non-directory workspaces in subprocess tests.
