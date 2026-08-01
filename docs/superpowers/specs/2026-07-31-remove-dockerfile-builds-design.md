# Remove custom Dockerfile builds (`build:`)

Date: 2026-07-31
Status: Approved design

## Problem

Toolpod supports a `build:` escape hatch: a profile can point at a Dockerfile
that is built on demand and tagged `toolpod/<name>:latest`, with optional
`depends_on` dependencies and a `--rebuild` flag. The feature has no users
and mise-based `tools:` covers tool provisioning. The `build:` machinery is
dead weight: an entire `internal/build` package, a topological dependency
resolver, drift-detection hints, and the `--rebuild` flag — roughly 400 lines
including tests.

This spec removes the feature altogether. `image:` stays.

## Scope

### Deleted

- `internal/build/` package (`build.go`, `build_test.go`) — build execution,
  depends_on resolution, local tagging, drift-detection hint.
- `--rebuild` flag and all `Rebuild` plumbing (CLI flag, `Launch` options,
  runtime struct field). It only ever forced Dockerfile rebuilds.
- The `Build` struct and `Build` field on `Profile`.

### Simplified

- `internal/profile/merge.go` — the image/build "single slot" logic reduces to
  "child's image wins".
- `internal/profile/validate.go` — validation requires `image:`; the
  exactly-one-of-image-or-build checks go away.
- `internal/profile/catalog.go` — fragment error message drops "build".
- `internal/runtime/docker_prepare.go` — image is pulled inlined (moved from
  the deleted `build` package), volumes and mise tools unchanged.
- `pkg/toolpod/spec.go` and `dryrun.go` — drop `build:` mapping and printing.

### Explicitly out of scope

- Historical docs under `docs/superpowers/` (design specs, implementation
  plans) stay untouched as archives.
- No special validation for profiles that still declare `build:` — the app has
  no users, and such profiles will simply fail validation with
  "image is required". No extra error message for `build:` itself.
- The mise volume/tools/`image:` pull path is unchanged.

## Docs

README only: remove the `build` row from the profile schema table.

## Tests

- `internal/build/build_test.go` — deleted with the package.
- `internal/profile/validate_test.go` — build-vs-image tests reworked to
  image-required semantics.
- Merge/extends/catalog/runtime tests — drop any `Build`/`Rebuild` usage.
- `go test ./...` and `go vet ./...` must pass.
