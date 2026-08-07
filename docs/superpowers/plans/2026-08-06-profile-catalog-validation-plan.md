# Profile and Catalog Correctness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` or `superpowers:executing-plans` to implement this plan task-by-task.
**Goal:** Ensure every built-in and user profile resolves predictably, validates strictly, and applies documented merge/template rules.

**Architecture:** Parse YAML strictly at the schema boundary while keeping tolerant loading as an explicit “warn and skip” policy for completion/edit. Validate path categories separately: ordinary bind mounts require source/target; service-socket mounts require target/service/socket but no source.

**Tech Stack:** Go, `gopkg.in/yaml.v3`, embedded YAML catalogs, profile merge/provenance code, Go tests.

**Scope:** C1, C2, I22–I29, T1, T7, and selected M22 gaps: labels/command control characters, cache/file/mount path safety, device source safety, TTY values, and file content size. I30 is intentionally excluded because profile extension and shadowing are documented behavior.

## Task 1: Repair and continuously validate built-ins

**Files:** `internal/catalog/profiles/bash.yaml`, `internal/catalog/fragments/vcs/gitlab.yaml`, generated catalog docs, `internal/profile/catalog_test.go`.

- Change extends references to `core/lang/bash` and `core/vcs/git`, preventing a user entry with the same local name from redirecting built-in inheritance.
- Regenerate catalog documentation with `make docs`.
- Add a test that loads the embedded catalog and resolves every built-in profile and fragment, asserting no missing extends, cycles, or validation errors.

## Task 2: Make YAML decoding and versions strict

**Files:** `internal/profile/catalog.go`, `internal/profile/validate.go`, profile tests.

- Decode profile and fragment documents with `yaml.Decoder.KnownFields(true)`. Audit custom `UnmarshalYAML` methods (`Mount`, `Tool`, cache paths, extends) and explicitly reject unknown mapping keys there, since `yaml.Node.Decode` bypasses the outer decoder setting.
- Require the *resolved leaf profile* version to equal 1, matching fragments, without requiring every extends-only raw entry to repeat a version declaration.
- Keep tolerant loading for completion/edit as “warn and skip malformed user entries”; account for those skipped entries conservatively in prune (operations plan).
- Add tests for top-level and nested unknown fields, version 0/version 2 resolved profiles, and tolerant-load recovery.

## Task 3: Implement scalar null-delete semantics

**Files:** `internal/profile/merge.go`, `internal/profile/catalog.go`, merge tests.

- Extend explicit-null collection to scalar/optional fields that actually exist (`network`, `image`, `command`, `tty`, and `resources`).
- In `MergeProfiles`, distinguish absent from explicit null and delete inherited values accordingly.
- Preserve existing map/list semantics and provenance after delete/re-add.
- Add table-driven tests for each scalar and a multi-level extends chain.

## Task 4: Validate services, mounts, and paths

**Files:** `internal/profile/validate.go`, `internal/profile/paths.go`, profile/runtime tests.

- Validate service image references and control characters using the same rules as the main image.
- Permit absolute or `~/` mount/cache/file targets as the existing built-ins require. Require bind-mount sources to be absolute or `~/`; exempt service-socket mounts from source validation and require their service/socket fields instead.
- Reject control characters and `..` components before expansion. After rendering, expand a resulting `~/`, then re-check non-empty, allowed prefix, absolute resolved path, and traversal for mounts, caches, files, and service mounts.
- Validate service expose syntax (non-root absolute parent/no traversal) here; runtime repeats the check immediately before host filesystem operations.

## Task 5: Fix template rendering and service hash framing

**Files:** `internal/profile/paths.go`, `internal/profile/hash.go`, tests.

- Render environment values and command arguments whenever they contain a template expression, not only when the value starts with `{{`.
- Replace newline-delimited service hashing with length-prefixed or unambiguous byte-delimited fields so arbitrary content cannot collide.
- Add tests for embedded env expressions, tilde results, newline-containing files/env/mounts, and structurally distinct services.

## Task 6: Verify profile/catalog behavior

Run:

```bash
GOCACHE=/tmp/tpd-gocache go test ./internal/profile ./internal/catalog ./internal/prune ./cmd/tpd ./pkg/tpd
GOCACHE=/tmp/tpd-gocache go vet ./internal/profile ./internal/catalog ./internal/prune ./cmd/tpd ./pkg/tpd
make docs
git diff --check
```
