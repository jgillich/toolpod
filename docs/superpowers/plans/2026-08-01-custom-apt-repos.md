# Custom apt repos + mise self-install

Date: 2026-08-01
Spec: `2026-08-01-runtime-oci-deps-design.md`
Builds on: commits `8c529e2` (derived images) + `4e88338` (prune redesign)

## Goal

Add a `repos:` field to profiles/fragments so the derived-image build can
enable extra apt sources before `apt-get install`. Move `mise` itself out of
the base image and into the `mise` profile's `packages:` (enabled via
`repos: { mise: { extrepo: mise } }`). The base Dockerfile shrinks to
`debian:13` + `ca-certificates` + `extrepo` + the xdg-open helper.

## Schema

```yaml
repos:
  mise:                     # key is the merge identity (logical repo name)
    extrepo: mise           # the extrepo catalog name to enable
  my-custom-repo:           # fully inline custom repo (schema-ready, v1 errors at build)
    url: https://example.com/deb
    key_url: https://example.com/key.pub
    suites: stable
    components: main
```

`Repo` struct (`internal/profile/types.go`):

```go
type Repo struct {
    ExtRepo    string `yaml:"extrepo,omitempty"`
    URL        string `yaml:"url,omitempty"`
    KeyURL     string `yaml:"key_url,omitempty"`
    Suites     string `yaml:"suites,omitempty"`
    Components string `yaml:"components,omitempty"`
}
```

`ExtRepo` is the extrepo catalog name (`extrepo enable <ExtRepo>`), e.g.
`mise`. It may differ from the map key, though in practice they're the same.

`Repos map[string]Repo` field on `Profile` (YAML `repos:`). Merge: map
key-by-key with null-to-delete (identical to `mounts` via existing `mergeMap`).
Add `"repos"` to `collectNullKeys`.

Validation (`validateRepos`):
- Map key and `ExtRepo` (when set) each match `^[a-z0-9][a-z0-9+.-]+$`
  (reuse `packageNameRe`).
- `ExtRepo != ""` and any of `URL`/`KeyURL`/`Suites`/`Components` set → error
  ("extrepo repos must not set url/key_url/suites/components").
- `ExtRepo == ""` and `URL==""` → error ("repo requires extrepo: <name> or a url").
- `ExtRepo == ""` and `KeyURL==""` → error ("custom repo requires key_url").

## Derived image

Tag: `tpd/packages:<hash>` where hash now includes repos:
```
no repos:     sha256(baseID \x00 sorted(packages).join(\x01))
with repos:   sha256(baseID \x00 sorted(packages).join(\x01) \x00 sorted(canonical-repos).join(\x02))
```
`canonical-repos` entry = `name \x01 extrepo \x00 url \x00 key_url \x00 suites \x00 components`
(sorted by map key; empty fields serialize as empty strings). Entries are
joined with `\x02` (distinct from the `\x01` inside an entry) so the
serialization is injective even with arbitrary URL characters. The repos
segment is **appended only when non-empty** — a packages-only profile keeps
the byte-identical pre-repos hash, so existing derived-image cache entries
survive the upgrade for profiles that don't declare repos.
`DerivedTag` signature changes to accept repos; update call sites (prepare, prune, tests).

Synthesized Dockerfile (`synthesizeDockerfile` extended):
```dockerfile
FROM <base-ref>
RUN <for each repo, sorted by map key> \
    <if extrepo set:> extrepo enable <ExtRepo> && \
    <else: TODO v2 custom repo setup; v1 errors in Prepare not here> \
    apt-get update \
    && apt-get install -y --no-install-recommends <sorted shell-quoted packages> \
    && rm -rf /var/lib/apt/lists/*
```

The emitted `extrepo enable` uses the `ExtRepo` value (the catalog name), not
the map key — they're independently validated but may coincide.

`Prepare` errors early if any repo has `ExtRepo==""` ("custom apt repos not
yet supported; use extrepo: <name>"). This defer-statement keeps the schema
ready while v1 synthesis only handles extrepo — additive future change, no
schema migration.

Build trigger: `len(spec.Packages) > 0 || len(spec.Repos) > 0`.

Runtime `Repo` mirror struct + `Repos` field in
`internal/runtime/runtime.go` `Spec`. `buildSpec` converts
`profile.Repo` → `runtime.Repo` (same fields).

## Catalog

`internal/catalog/profiles/mise.yaml`:
```yaml
version: 1
image: ghcr.io/jgillich/tpd-mise:latest
command: ["/usr/bin/mise"]
repos:
  mise:
    extrepo: mise
packages:
  - mise
  - curl
  - git
  - build-essential
  - cmake
  - ninja-build
  - clang
  - pkg-config
  - autoconf
  - automake
  - libtool
  - bison
  - re2c
  - python3
  - libssl-dev
  - libcurl4-openssl-dev
  - zlib1g-dev
  - libreadline-dev
  - libffi-dev
  - libsqlite3-dev
  - libedit-dev
  - gettext
  - openssl
  - gdb
mounts:
  /etc/mise:
    source: ~/.config/mise
    optional: true
tools:
  bat: latest
  fd: latest
  fzf: latest
  jq: latest
  ripgrep: latest
  yq: latest
```

`php.yaml` and `gui.yaml` unchanged (their `packages:` lists stay as
committed in `8c529e2`).

## Dockerfile

```dockerfile
FROM debian:13

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        extrepo \
    && rm -rf /var/lib/apt/lists/*

COPY docker/xdg-open /usr/local/bin/xdg-open
RUN ln -sf /usr/bin/xdg-open /usr/local/bin/xdg-open.real \
    && chmod +x /usr/local/bin/xdg-open
```

No mise in the base. `ca-certificates` stays (needed by `extrepo enable mise`
to fetch the repo over https; verified the mise repo is https). `extrepo`
pulls perl + libwww-perl transitively (~30MB) — acceptable for the base.

The xdg-open symlink `/usr/local/bin/xdg-open.real → /usr/bin/xdg-open` is
dangling in the base (xdg-utils moved to `gui.yaml`); it resolves in GUI
derived images where gui.yaml's packages install xdg-utils. Harmless for
non-GUI profiles (the helper only runs inside AppImage tools).

## No entrypoint change

The container bootstrap (`internal/runtime/docker_run.go`) remains
unconditional `mise install` + `eval "$(mise hook-env)"`. Every built-in
profile extends `mise` (which declares `packages: [mise, …]`) so they all get
a derived image with `/usr/bin/mise`. A custom-image profile with no packages
+ no repos uses the bare base (no mise) → `mise install` fails loud as today.
Decision: keep failing loud.

## Ordered steps

### Step 1 — Schema + merge + validate

Files:
- `internal/profile/types.go` — add `Repo` struct + `Repos` field on `Profile`.
- `internal/profile/catalog.go` — add `"repos"` to `collectNullKeys`.
- `internal/profile/merge.go` — `out.Repos = mergeMap(parent.Repos, child.Repos, child.NullKeys["repos"])`.
- `internal/profile/validate.go` — `validateRepos`: name regex, extrepo/url mutual exclusivity, key_url required when not extrepo. Call from `validate()`.
- `internal/profile/merge_test.go` — repos merge (override by name), null-to-delete, additive across extends.
- `internal/profile/validate_test.go` — extrepo+url=error, no-url-no-extrepo=error, custom-without-key=error, valid-extrepo-only=ok, valid-custom=ok.

Verify: `go test ./internal/profile/ && go vet ./internal/profile/`.

### Step 2 — Derived image engine

Files:
- `internal/runtime/runtime.go` — add `Repo` struct + `Repos map[string]Repo` on `Spec`.
- `internal/runtime/docker_build.go`:
  - `DerivedTag(baseID string, packages []string, repos map[string]Repo) string` — extend hash.
  - `synthesizeDockerfile` — extend to take `repos`, emit `extrepo enable <ExtRepo>` per sorted repo before apt-get update.
  - `Prepare` path — pass repos; error early on non-extrepo repos.
- `internal/runtime/docker_prepare.go` — `Prepare` triggers on repos; passes repos to `buildDerivedImage` + `DerivedTag`.
- `pkg/tpd/spec.go` — `buildSpec` converts `profile.Repo` → `runtime.Repo`.
- `internal/prune/prune.go` — `computeUsed` passes `cfg.Repos` to `DerivedTag`.
- Tests:
  - `docker_build_test.go` — `DerivedTag` varies with repos; `synthesizeDockerfile` contains `extrepo enable mise`; empty repos tag == packages-only tag (back-compat for packages-only profiles).
  - merge/validate tests for repos.

Verify: `go test ./internal/runtime/ ./internal/profile/ ./pkg/tpd/ ./internal/prune/ && go vet ./...`.

### Step 3 — Catalog + Dockerfile

Files:
- `internal/catalog/profiles/mise.yaml` — add `repos: { mise: { extrepo: mise } }` + `packages: [mise, curl, git, …]`.
- `Dockerfile` — shrink to ca-certificates + extrepo + xdg-open.
- `internal/profile/catalog_test.go` — assert mise profile resolves `repos` with `mise: {extrepo: mise}` and `packages` contains `mise`.

Verify: `go test ./... && go vet ./... && tpd doctor` (all 12 profiles still valid).

### Step 4 — Integration test

`internal/runtime/docker_test.go`:
- Integration test: build derived image with `repos: {mise: {extrepo: mise}}` + `packages: [mise]`, verify `/usr/bin/mise` exists and `mise --version` runs.
  - Skip in `-short`.
  - Skip if `DOCKER_HOST` unset.
  - Cleanup: remove derived image after.

### Step 5 — Docs

Files:
- `docs/superpowers/specs/2026-08-01-runtime-oci-deps-design.md` — section on `repos:`, base shrink, extrepo mechanism.
- `README.md` — `repos:` schema row + merge semantics note.
- `AGENTS.md` — repos field note under Conventions.

Final verify: `go test ./... && go vet ./...`.

## Verify at each milestone

- After step 1: `go test ./internal/profile/` — schema/merge/validate green.
- After step 2: `go test ./internal/runtime/ ./internal/profile/ ./pkg/tpd/ ./internal/prune/` — all green, existing integration test (TestIntegrationPrepareBuildsDerivedImage against the still-unshrunk base) passes.
- After step 3: `go test ./...` + `tpd doctor` (12 profiles valid) + `tpd shell -c 'mise --version'` (builds the new mise-derived image, runs mise).
- After step 4: integration test green (`-count=1`).
- After step 5: docs+tests pass.