# Runtime Safety and Engine Compatibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` or `superpowers:executing-plans` to implement this plan task-by-task.
**Goal:** Make launches, services, derived-image builds, and engine detection safe under normal failure and concurrency conditions.

**Architecture:** Keep safety checks at both profile-validation and runtime boundaries. Centralize daemon URL handling; serialize publication for the existing declared-input derived tags, and treat a change to extrepo-derived image identity as a separate design decision because it conflicts with offline prune and cache-hit behavior.

**Tech Stack:** Go, Docker SDK, Podman-compatible API, Unix sockets, extrepo/deb822, Go tests.

**Scope:** I1–I11, I14–I20, and the race portion of I21, plus focused regression tests. Excludes I12 (intentional `--pull` refresh semantics), I13 (already returns before run-dir removal), and the extrepo-content-identity portion of I21 pending a separate design.

## Task 1: Fix catalog-independent runtime validation and port binding

**Files:** `internal/runtime/docker_run.go`, `internal/runtime/docker_services.go`, `internal/profile/validate.go`, `pkg/tpd/dryrun.go`, corresponding runtime/profile/package tests.

- Default an omitted port host IP to loopback during spec construction; preserve explicit `0.0.0.0`, and render the resulting concrete value identically in dry-run and runtime messages.
- Accept service exposes only below a non-root absolute parent (for example `/run/app/db.sock`); reject `/db.sock`, `/`, and any `..` component before `serviceSocketPath`/`buildServiceMounts` run. This prevents a bind over the service container root as well as host traversal.
- Add tests for default/explicit host binding, `/db.sock`, `/`, and traversal. Do not add template tests: service expose values are not templated.

## Task 2: Enforce ownership when reusing or removing services

**Files:** `internal/runtime/docker_services.go`, `internal/runtime/labels.go`, fake service daemon tests.

- Require `tpd.managed=true` and the expected service label before treating a deterministic-name container as tpd-owned.
- Return a dedicated ownership-conflict error for a foreign container with the deterministic name. Never stop/remove it and never proceed to `ContainerCreate`, avoiding a later opaque 409. The error identifies the exact conflicting name and leaves remediation (rename/remove the foreign container) to its owner.
- Require ownership labels on service consumers before they protect a service from replacement/removal.
- Verify every reused expose socket with `os.Stat`/socket validation; recreate the service when the path is absent.

## Task 3: Harden engine host detection and HTTP requests

**Files:** `internal/runtime/docker.go`, `internal/runtime/extrepo.go`, `internal/doctor/checks.go`, `pkg/tpd/launch.go`, runtime/doctor/package tests.

- Extract daemon-host transport construction before making `/info` requests: Unix sockets use a Unix transport; `tcp://` becomes `http://`; `http://`/`https://` remain unchanged; `ssh://`/`npipe://` return a clear unsupported error.
- Treat bare `unix://` as the default Unix socket form.
- Set bounded transport/client timeouts before every `/info` and extrepo request while preserving caller cancellation. In a real launch, propagate `DetectMode` failure as a runtime error; do not silently select rootful mode.
- Add tests for Unix, bare Unix, TCP, unsupported schemes, timeout/cancellation, and doctor propagation.

## Task 4: Correct process suspension and generated container metadata

**Files:** `internal/runtime/docker_run.go`, runtime tests.

- Ask the container for the foreground app PGID (`ps -o pgid= -p <app-pid>`) before signalling it; use that PGID in `kill -- -<pgid>`, report failures, and add fake-daemon exec handlers for the query and signal paths.
- Retain the output-byte gate, which prevents foreground input racing ahead of SIGCONT, but bound it: on timeout emit a warning and reassert terminal state rather than hanging indefinitely.
- Parse `ContainerTop` columns by name, skip PID 1 only when a PID column exists, and recognise stopped state only when STAT's leading state rune is `T`/`t` (not a substring match such as `DT`).
- Replace PID-only random-ID fallback with a timestamp/crypto-independent uniqueness source and retry container-name collisions.
- Emit device cgroup rules only for actual character/block devices and preserve requested permissions.

## Task 5: Fix derived-image build correctness and races

**Files:** `internal/runtime/docker_build.go`, `internal/runtime/docker_prepare.go`, `internal/runtime/extrepo.go`, `internal/prune/prune.go`, runtime/prune tests.

- For a repos-only profile, emit repository/bootstrap steps but no package-install RUN; for packages, retain the install RUN with non-empty quoted operands.
- Resolve tar hardlinks when extracting image files for extrepo probing, or reject them explicitly instead of accepting empty content.
- Treat prerelease engine versions as unsupported for volume subpaths unless the release is known to have the feature; add release-candidate and stable-version tests.
- Serialize cache-miss builds by the existing derived tag using a per-tag lock, then re-check image existence after acquiring it. This removes concurrent last-writer races without changing tag semantics or adding network work on cache hits.
- Build from the resolved base image ID/reference rather than a mutable tag where the engine supports it.
- Preserve the declared-input, offline-recomputable tag contract used by prune. Before attempting content-derived tags, write a separate design covering a persistent resolved-repository index, tag migration, prune reachability, and cache-hit network policy.
- Add concurrent fake-daemon tests proving one build/published image per tag and prune tests proving current tags remain offline-recomputable.

## Task 6: Verify the runtime track

Run:

```bash
GOCACHE=/tmp/tpd-gocache go test ./internal/runtime ./internal/profile ./internal/prune ./internal/doctor ./pkg/tpd
GOCACHE=/tmp/tpd-gocache go vet ./internal/runtime ./internal/profile ./internal/prune ./internal/doctor ./pkg/tpd
```

Then run the engine-backed service/port tests when a Podman or Docker daemon is available.
