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

Beyond dead weight, `build:` did not align with tpod's vision and was a
potential can of worms for users and tpod itself:

- **Vision mismatch.** tpod's pitch is "one base image serves every profile;
  no per-language image, no rebuild when a tool version bumps." Per-profile
  Dockerfiles invert that — every profile carries its own build recipe that
  drifts independently of the curated base image. The escape hatch rewards
  the wrong instinct (hand-roll a Dockerfile) instead of the right one
  (declare what you need and let tpod provision it).
- **User can of worms.** A free-form Dockerfile is an open-ended
  responsibility: the user owns base-image choice, layer ordering, cache
  hygiene, `apt-get update` correctness, secret handling, multi-arch
  support, and tagging. Every user-built profile reinvents the same wheel
  badly, with no shared infrastructure for caching, pruning, or upgrade.
- **tpod can of worms.** Owning a Dockerfile-build feature means owning its
  failure modes: build-context path resolution inside the workspace mount,
  `depends_on` cycle handling, drift detection between the user's Dockerfile
  and the tagged image, `--rebuild` semantics across host boundaries,
  concurrent-build races, and the implicit promise that tpod understands
  arbitrary Dockerfiles. Each is a small maintenance burden in isolation;
  together they're a permanent surface area that the core team didn't want
  to support for a feature with no users.

This spec removes the feature altogether. `image:` stays. The later design
at `2026-08-01-runtime-oci-deps-design.md` reintroduces an *automated,
declarative* form of "toolpod builds a derived image on demand" keyed on a
profile's `packages:` list — without bringing back any of the can-of-worms
surface area (no user-owned Dockerfile, no `depends_on`, no `--rebuild`, no
drift detection, no open-ended build context).

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
