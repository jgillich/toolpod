# Runtime-declared packages via cached OCI derived images

Date: 2026-08-01
Status: Approved design

## Problem

The single base image `Dockerfile` (`ghcr.io/jgillich/tpod-mise`) has grown to
89 lines of `apt-get install` covering every fragment and profile in the
catalog — PHP build deps (`libxml2-dev`, `libicu-dev`, `libonig-dev`, `libzip-dev`,
`libxslt1-dev`, `libpq-dev`, `libmariadb-dev`, `bison`, `re2c`, …), the
GUI/GStreamer/X11/nss/alsa runtime stack for the Electron/Tauri AppImage
profiles (`buzz` extends `gui`, `t3code` extends `gui`), and the generic
mise-src-compiling C toolchain (`build-essential`, `cmake`, `ninja-build`,
`clang`, `autoconf`, `automake`, `libtool`, `libssl-dev`, …). Adding any new
agent or fragment that needs a system library means rebuilding and pushing the
base image for every user.

The `build:` escape hatch — point a profile at a custom Dockerfile and force a
rebuild with `--rebuild` — was removed on 2026-07-31 per
`2026-07-31-remove-dockerfile-builds-design.md`. The removal was motivated
not just by the feature being unused dead weight but by a deeper mismatch:
`build:` didn't align with tpod's vision and was a can of worms for users
and tpod itself. Today there is no per-profile path to system libraries.

## Goal

Stop growing the base Dockerfile to accommodate every fragment's C/C++
dependencies. Profiles and fragments declare the system packages they need;
toolpod materializes an OCI image containing the base image plus those packages
and reuses that derived image across runs. The mechanism is fully automatic and
uses the container runtime's native image cache — no relocatable-install hacks,
no foreign package manager, no user-visible "rebuild" step.

Single sentence: a profile/fragment declares `packages:` (a list of apt
package names); tpod's `Prepare` step lazily builds and tags a derived image
`tpod/packages:<hash>`, where `<hash>` is the first 16 hex chars of
`sha256(<base-image-id> + \u0000 + sorted(<packages>).join(\u0001))`,
caches it for reuse, and returns that ref to be used as the runtime image. The
base Dockerfile shrinks back to "the OS + `mise` + the things every profile
needs"; everything else moves into catalog-level `packages:` declarations and
the derived-image cache.

The strongest argument for this design is that it is *simpler* than the clever
alternatives while also being more robust: no `LD_LIBRARY_PATH`, no
`PKG_CONFIG_PATH` manipulation, no relocation, no patched `.pc` files, no
maintainer-script surprises. Everything lives exactly where Debian expects it
to live — `/usr/lib/x86_64-linux-gnu`, `/usr/include`, `/usr/share`. The
implementation simplicity *is* the architecture.

**Immutability invariant**: derived images are immutable implementation
details. Users should never tag, modify, or depend on them directly. Toolpod
is free to rebuild or garbage-collect them at any time, change the naming
scheme, switch to a different cache backend, or add registry support — users
never notice. The only contract is the input `packages:` list and the base
image; the derived image is a private cache.

The two-problems insight preserved from review: build-deps (`libxml2-dev` for
mise-php to compile against) and runtime GUI libs (`libgtk-3-0`, `libnss3`,
`libasound2`, `gstreamer1.0-plugins-bad` for Electron/Tauri to load at
runtime) are handled by the *same* mechanism — both become apt installs inside
the derived image, where they live in `/usr/lib/x86_64-linux-gnu` exactly as
Debian expects. The two-problems framing that earlier alternatives (relocated
dpkg, vcpkg, Homebrew) struggled with collapses: there's only one problem
(declare packages, get an image with those packages installed the normal
Debian way), and the OCI image cache is the naturally-suited store.

## Alternatives considered

Researched and rejected during brainstorming:

- **Nix / nix-portable**: covers every dep in the Dockerfile, but Nix's "store
  paths are everything" model fights tpod's "workspace mounted from host, mise
  shims on `PATH`, AppImage extracted into `/mise`" model. User ruled it out as
  too foreign.
- **vcpkg**: prototyped in an untracked `Dockerfile.vcpkg` and confirmed to
  work for PHP build-deps via `PKG_CONFIG_PATH=/opt/vcpkg/installed/x64-linux/lib/pkgconfig`.
  Insufficient catalog coverage for the GUI/GStreamer runtime libs (no
  WebKitGTK, no `gstreamer-plugins-bad`). Rejected as a standalone solution.
- **Spack**: covers both halves, but HPC-flavored, heavy Python bootstrap, slow
  concretization. Wrong ecosystem.
- **Homebrew/Linuxbrew**: covers both halves with bottles, but mandates the
  `/home/linuxbrew/.linuxbrew` prefix for bottle reuse, ships shellenv, brings
  Ruby toolchain. Adopting its ecosystem is a cost we don't need to pay.
- **Full `dpkg --instdir`**: officially supported but rarely exercised at scale
  for this use case; failure modes accumulate package-by-package over years
  (`ldconfig` expecting the real system, `update-alternatives` writing global
  state, `glib-compile-schemas`/`gtk-update-icon-cache` assuming system
  ownership, `pkg-config` `.pc` files containing absolute `/usr` paths, CMake
  configs embedding the install prefix, packages installing into `/etc` or
  `/var`). Rejected as too risky.
- **`apt download` + `dpkg -x` extraction** (no database, no scripts): a
  sharper variant of full `dpkg --instdir`. Promising for the build-deps half
  (the `.pc` files of `lib*-dev` packages typically use `${libdir}` and are
  RPATH-clean) but weaker for the GUI runtime half — postinst side-effects
  matter more, and runtime binaries expect them. Not chosen as the primary
  mechanism; left documented as a future option if the OCI-build path proves
  heavy for the build-deps-only case.

Chosen: **cached OCI derived images**, triggered by a new `packages:` field.
Brings back a controlled, automated form of the machinery that
`2026-07-31-remove-dockerfile-builds-design.md` removed — declarative from
catalog YAML, fully automatic (no manual rebuild flag, no `toolpod/<name>:latest`
drift tag), and using the container runtime's native image cache as the
deduplication store. The base Dockerfile stops growing; the mechanism works
identically for build and runtime deps because nothing is relocated.

## Scope

### Added

- A new `packages []string` field on `Profile` (in `internal/profile/types.go`)
  with YAML key `packages:`.
- A new additive-with-dedup merge rule for `packages`:
  parent's list is appended-to by the child's list, with duplicates removed
  while preserving order. `packages: null` in a child sets the merged list to
  empty. `command`'s existing "child replaces" rule is unchanged; `packages`
  follows the *additive* rule because packages from different fragments compose
  by nature (e.g. `php` contributes `libxml2-dev`, `gui` contributes `libgtk-3-0`);
  neither supersedes the other.
- A new `NullKeys` entry for `packages` so that `packages: null` reliably
  clears the inherited list at parse time, consistent with the existing
  null-to-delete mechanism for `mounts`/`environment`/`tools`/`caches`/`labels`/
  `ports`/`devices`/`dbus`. Implementation note: `packages` is a slice, not a
  map; the null-clear is whole-field, not per-element.
- Validation in `internal/profile/validate.go`: each entry in `packages` (after
  merge) must match Debian's package-name regex `^[a-z0-9][a-z0-9+.-]+$` (Debian
  Policy §5.6.7). Reject whitespace, shell metacharacters, `=` (no version
  pinning in v1). Validation runs at `ResolveProfile` time so inherited packages
  are also checked.
- A new derived-image build path in `internal/runtime/docker_prepare.go`
  (extended `Prepare`) and a new helper file
  `internal/runtime/docker_build.go` (image-id resolution, tag derivation,
  in-memory Dockerfile synthesis, `cli.ImageBuild` invocation).
- A new `tpod prune --images` flag in `cmd/tpod/cli.go` and a new branch in
  `internal/prune/prune.go` to remove `tpod/packages:<tag>` derived images,
  with `docker image prune` semantics (remove all `tpod/packages:*` images
  when the flag is invoked; no catalog-liveness inference, so a stale
  user-created profile that's temporarily absent from the catalog isn't
  surprised by having its derived image deleted out from under it).
- `tpod doctor` reports the count and total reclaimable size of
  `tpod/packages:*` derived images (best-effort via `cli.ImageList`).

### Changed

- `pkg/tpod/spec.go` — `Spec` gains a `Packages []string` field; `buildSpec`
  copies `cfg.Packages` into `spec.Packages`.
- `internal/runtime/docker_prepare.go` — `Prepare` resolves the base image's
  ID (`ImageInspectWithRaw(...).ID`, the content-addressed image-config SHA —
  not `RepoDigests[0]`, which is positional and may be a multi-arch manifest
  list digest rather than the per-platform filesystem identity); if
  `len(spec.Packages) > 0`, computes the derived tag, checks `imageExists`,
  and if absent calls `buildDerivedImage`; returns the derived ref (or the
  base ref when no packages).
- `internal/catalog/profiles/mise.yaml` — gains a `packages:` block carrying
     the general C toolchain (`build-essential`, `cmake`, `ninja-build`, `clang`,
     `pkg-config`, `autoconf`, `automake`, `libtool`, `bison`, `re2c`, `python3`,
     `libssl-dev`, `libcurl4-openssl-dev`, `zlib1g-dev`, `libreadline-dev`,
     `libffi-dev`, `libsqlite3-dev`, `libedit-dev`, `gettext`, `openssl`, `gdb`).
     Every profile that extends `mise` inherits these. Behavior-preserving in
     the sense that today's profiles see these packages in the base image; after
     migration they see the same set in a derived image built once per
     `(base-image-id, packages)` combo and reused on subsequent runs. First run
     of any profile after migration pays the one-time build cost; subsequent
     runs are instant. Trim the list later when evidence shows a tool works
     without a given dep.
- `internal/catalog/fragments/php.yaml` — gains a `packages:` block with
  `libxml2-dev libicu-dev libonig-dev libpq-dev libxslt1-dev libzip-dev
  libmariadb-dev libgd-dev libpng-dev libjpeg-dev bison re2c`.
- `internal/catalog/fragments/gui.yaml` — gains a `packages:` block with the
  full GUI/GTK/X11/nss/alsa/GStreamer set currently at Dockerfile L41-81.
- `Dockerfile` — shrinks to `FROM debian:13`, `extrepo enable mise`, install
  `mise`, install just the bare-minimum bootstrap (`extrepo`, `ca-certificates`,
  `curl`, `git` needed for `extrepo` and `mise`; `tini` for PID 1), and the
  `docker/xdg-open` copy. No more libs; everything else migrates to catalog
  `packages:`.
- `README.md` — `packages:` added to the profile schema reference; merge
  semantics documented; `tpod prune --images` documented; the "host lacks build
  permission" escape hatch (ship a custom `image:` yourself) documented.
- `AGENTS.md` — note added under "Conventions" that profiles declare
  per-profile system deps via `packages:` and that tpod auto-builds derived
  images keyed on `(base-image-id, packages)`.

### Out of scope

- Version pinning in `packages:` entries (`libxml2-dev=2.12.x`). v1 installs
  whatever Debian's archive gives the derived image.
- Pushing derived images to a registry. They live in the local Docker image
  store. Sharing across hosts would be a v2 follow-up (the build is
  deterministic so the tag is portable).
- Concurrency control on derived-image builds. Two simultaneous launches of
  profiles that resolve to the same `tpod/packages:<hash>` tag both call
  `imageExists`, both see false, both start a build. Docker handles concurrent
  builds fine. One wasted build is acceptable in v1; a host-level lock on
  build keys is a v2 follow-up.
- The synthesized Dockerfile does **not** use BuildKit cache mounts
  (`RUN --mount=type=cache,target=/var/cache/apt`). They would dramatically
  reduce rebuild cost after a base-image bump by sharing the apt cache across
  builds for the same base, but they require BuildKit (DOCKER_BUILDKIT=1 or
  rootless Podman equivalent) and add a runtime assumption. Worth revisiting
  in v2 once the basic mechanism is validated; deliberately simple in v1.

## Mechanism

### Image naming & cache key

Derived images are tagged `tpod/packages:<hash>` where `<hash>` is the first
16 hex chars of `sha256(<base-image-id> + \u0000 +
sorted(<packages>).join(\u0001))`. Deterministic for a given base image and
package set. The tag namespace `tpod/packages:<hash>` (not
`tpod/<profile>:<hash>`) deliberately excludes `spec.ProfileName` from the
tag: the cache key is `(base-image-id, package set)`, not `(profile,
base-image-id, package set)`. Two profiles that declare the same `packages:`
(or whose merged package sets match) share one derived image — one
filesystem, one build, one cache entry, one apt transaction. The mental
model is "exactly one derived image for a given base image and package set";
OCI's content-addressed layers dedupe the storage, and a single tag per
(base, packages) combo keeps tag metadata and pruning simple.

The cache key is `(base image, package names)` — not `(base image, exact
package contents)`. The same hash built months apart can produce different
filesystem contents because Debian mirrors drift over time. This is
intentional and acceptable: the contract is "the base image plus the named
packages, as apt resolves them at build time," not a bit-for-bit
reproducible artifact across years.

`<base-image-id>` is the content-addressed image-config SHA returned by
`ImageInspectWithRaw(ctx, baseRef).ID` — the actual filesystem identity of
the image we're building FROM. This is **not** `RepoDigests[0]`: `RepoDigests`
is a list whose ordering depends on push order and configured registries, and
for multi-arch images it may hold the manifest *list* digest (arch-agnostic)
rather than the per-platform image digest the build will actually use. The
image ID is always present, always unique to the filesystem, and unaffected by
registry state. Using it eliminates the "no digest available" fallback edge
case entirely.

The hash includes the base image ID, so when the base image is rebuilt
(`make image` bumps `ghcr.io/jgillich/tpod-mise`), every affected derived
image is automatically invalidated on the next run — a new base image gets a
new ID, the derived tag's hash changes, `imageExists` misses, and a fresh
derived image is built. No manual "pass `--rebuild`"; the cache naturally
rides the base image's lifecycle.

### Build Dockerfile (synthesized at runtime)

Generated in-memory by Go and piped to the Docker daemon via
`cli.ImageBuild`. No on-disk Dockerfile is written. Body:

```dockerfile
FROM <base-ref>
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        <sorted package list, shell-quoted> \
    && rm -rf /var/lib/apt/lists/*
```

`FROM` uses the original base-image **reference** (e.g.
`ghcr.io/jgillich/tpod-mise:latest`), not the image ID. `ensureImagePulled`
has already pulled the exact image, so Docker resolves `FROM <base-ref>` to
that local image — we build against the same filesystem we hashed. The image
ID is used only as a hash input, not as a `FROM` target, because image IDs
are local implementation details (not portable across registries or build
frontends, and not guaranteed to share semantics between Docker and Podman
long-term). Using `baseRef` in `FROM` keeps the synthesized Dockerfile
backend-agnostic; using `baseID` in the hash preserves automatic
invalidation when the local base image changes.

**Sort-and-emit invariant**: package list is sorted lexicographically before
hashing *and* before emitting in the `RUN` step. So `[git, curl]` and
`[curl, git]` produce byte-for-byte identical Dockerfiles. Order in catalog
YAML is irrelevant to cache hit and irrelevant to the apt invocation.

Each name is shell-quoted (Go's `strconv.Quote` or equivalent `shlex.quote`
semantics) before being interpolated into the `apt-get install` line so a
typo in a catalog entry can't inject into the RUN step. Validation already
rejects metacharacters, so quoting is defense-in-depth.

The build implementation is intentionally opaque. Future versions may
replace Dockerfile synthesis with direct BuildKit APIs or another OCI
builder without affecting the external behavior or the cache-key contract.

Build output is streamed via the same response-reader pattern `ImagePull`
already uses in `docker_prepare.go:42-49` (drain `cli.ImageBuild`'s reader,
write progress lines through `ProgressWriter`).

### Prepare flow

`Prepare(ctx, spec, w) (string, error)` is extended in place; the existing
`launch.go:115-118` ("if `imageRef != ""` then `runSpec.Image = imageRef`")
just works because `Prepare` returns the derived ref the same way it returns
the base ref today. None of the launch/spec plumbing changes.

```go
func (d *DockerRuntime) Prepare(ctx context.Context, spec Spec, w ProgressWriter) (string, error) {
    runtimeHome := spec.RuntimeHome

    baseRef := spec.Image
    if err := ensureImagePulled(ctx, d.cli, baseRef, w); err != nil {
        return "", fmt.Errorf("ensure base image: %w", err)
    }
    baseID, err := resolveImageID(ctx, d.cli, baseRef)
    if err != nil {
        return "", fmt.Errorf("resolve base image ID: %w", err)
    }

    imageRef := baseRef
    if len(spec.Packages) > 0 {
        derivedRef := derivedTag(baseID, spec.Packages)
        exists, err := imageExists(ctx, d.cli, derivedRef)
        if err != nil {
            return "", err
        }
        if !exists {
            if err := buildDerivedImage(ctx, d.cli, derivedRef, baseRef, spec.Packages, w); err != nil {
                return "", fmt.Errorf("build derived image: %w", err)
            }
        }
        imageRef = derivedRef
    }

    // Existing volume setup — unchanged
    miseVol := mise.MiseVolume(runtimeHome)
    if err := mise.EnsureVolume(ctx, d.cli, miseVol.Name); err != nil {
        return "", fmt.Errorf("mise volume: %w", err)
    }
    for _, cache := range spec.Caches {
        if err := mise.EnsureVolume(ctx, d.cli, cache.Name); err != nil {
            return "", fmt.Errorf("cache volume %s: %w", cache.Name, err)
        }
    }

    return imageRef, nil
}
```

### Build cost & caching behavior

- **First run** of a profile with a new `(base-image-id, packages)` combo:
  ~30-90s on a modern desktop — `apt-get update` + `install -y
  --no-install-recommends` of typically 20-80 packages. Happens once per cache
  key.
- **Subsequent runs**: instant. The `imageExists` check short-circuits the
  same way `ensureImagePulled` does today.
- **After a base image bump**: each affected derived image rebuilds once on
  first run.
- **Cache locality**: derived images live in the local Docker image store
  alongside the base image, cleaned up by `docker image prune` or
  `tpod prune --images` (which removes all `tpod/packages:<tag>` images
  with `docker image prune` semantics — no catalog-liveness inference).

### Failure modes & edge cases

1. **`packages:` empty after merge** — `Prepare` returns `baseRef` unchanged;
   the `if len(spec.Packages) > 0` block is skipped; no derived image, no
   build. Byte-for-byte the current behavior.
2. **`packages:` from multiple fragments** — lists merge additively with
   dedup at `ResolveProfile` time. One derived image per profile, one apt
   transaction, one cache key. This is why additive merge matters.
3. **A package fails to install** (bad name, missing in the base Debian
   release, mirror transient) — `cli.ImageBuild` returns a nonzero build
   error; `Prepare` bubbles it as `prepare: build derived image: <err>`. No
   half-built image is tagged (Docker's buildkit tags at the end). For
   actionable messages on unknown-package errors, we synthesize a cleaner
   error by grepping the streamed build log for
   `E: Unable to locate package <name>` and pointing at the bad entry.
4. **Base image ID unavailable** — `ensureImagePulled` runs first (unchanged);
   `ImageInspectWithRaw` then returns the image's `.ID` field directly. The
   image ID is always present after inspection (it's the content-addressed
   config SHA, not a registry-supplied digest), so this branch cannot be
   unreachable in the way a `RepoDigests[0]` lookup could.
5. **Concurrent launches of two profiles that resolve to the same derived
   tag** (same base, same merged package set) — both call `imageExists` on the
   same `tpod/packages:<hash>`, both see false (if first time), both start a
   build to the same tag. Docker's tag operation is last-writer-wins at the
   *tag* level but content-addressed at the layer level; both builds produce
   the same layers, so the loser's `ImageBuild` either succeeds silently or
   races the winner's tag write. Either way the resulting image is identical
   content-wise. Accepted in v1 (documented race).
6. **Host has no permission to build images** (read-only Docker socket,
   restricted Podman) — the build fails with a permission error from
   `cli.ImageBuild`, bubbled up by `Prepare`. Escape hatch: keep a custom
   `image:` (today's mechanism) and don't use `packages:`. Documented in
   README.
7. **Typo in catalog entry** (e.g. `libxml2` without `-dev`) — apt happily
   installs the runtime lib, mise's php build fails later because
   `libxml/HTMLparser.h` isn't there. Same failure mode a misnamed apt entry
   causes today. Validation catches syntactic bad names but not
   archive-existence; the missing header surfaces at compile time.
8. **Disk space accumulation** — `tpod prune --images` removes all
   `tpod/packages:<tag>` images in the local Docker store, with
   `docker image prune` semantics. The flag deliberately does **not** infer
   liveness from the catalog: recomputing "expected hashes" from currently
   resolvable profiles would surprise a user whose stale user-created profile
   temporarily isn't loadable. Explicit-only cleanup keeps the contract
   simple: `tpod prune --images` means "delete all cached derived images,
   rebuild them on next use." Users who want finer control can use
   `docker image prune` directly.

## Migration plan

Sequential; each step is independently shippable.

1. **Schema + merge + validation only.** Add `Packages []string` to `Profile`,
   the additive-dedup merge rule, the `NullKeys` entry for whole-field clear,
   and the package-name regex in `validate.go`. `Prepare` ignores the field
   for now; no behavior change. Tests in `internal/profile/merge_test.go` and
   `validate_test.go`.
2. **Derived-image build engine.** Implement
   `internal/runtime/docker_build.go` (`resolveImageID`, `derivedTag`,
   `buildDerivedImage`, in-memory Dockerfile synthesis, streamed build).
   Extend `Prepare`. `pkg/tpod/spec.go` adds `Spec.Packages` and `buildSpec`
   maps `cfg.Packages` to it. Catalog YAMLs still don't declare packages;
   behavior identical to today for built-in profiles. Tests with a fake
   `DockerRuntime` covering the new code paths: `derivedTag` determinism (same
   inputs ⇒ same tag, sort-normalisation, no `ProfileName` in tag), different
   package order ⇒ same tag, Dockerfile-body synthesis (contains sorted,
   shell-quoted packages, no shell metachar injection, FROM uses baseRef),
   empty-packages short-circuit.
3. **Catalog migration.** Move the apt list in `Dockerfile` to catalog:
   `lib*-dev` and PHP build deps → `php.yaml`; GUI/GStreamer/X11/nss/alsa
   (Dockerfile L41-81) → `gui.yaml`; general C toolchain (build-essential,
   cmake, ninja, clang, autoconf, automake, libtool, bison, re2c, python3,
   libssl-dev, libcurl4-openssl-dev, zlib1g-dev, libreadline-dev, libffi-dev,
   libsqlite3-dev, libedit-dev, gettext, openssl, gdb) →
   `internal/catalog/profiles/mise.yaml` so every profile that extends `mise`
   still sees them. Behavior-preserving in the sense that today's profiles see
   these in the base image; after migration they see the same set in a derived
   image built once per `(base-image-id, packages)` combo. First run of any
   profile after migration pays the one-time build cost; subsequent runs are
   instant. `Dockerfile` shrinks accordingly.
4. **Prune + doctor extensions.** `tpod prune --images` in `cmd/tpod/cli.go`
   and `internal/prune/prune.go` with `docker image prune` semantics: removes
   all `tpod/packages:<tag>` images in the local Docker store, no catalog
   liveness inference. `tpod doctor` reports `tpod/packages:*` image count
   and reclaimable space.
5. **Docs.** Update `README.md` schema reference, merge semantics,
   `tpod prune --images`, and the "host lacks build permission" escape hatch.
   Update `AGENTS.md`: profiles declare per-profile system deps via
   `packages:`, tpod auto-builds derived images keyed on `(base-image-id,
   packages)`.

## Tests

- `internal/profile/merge_test.go` — `packages` merge cases: additive dedup,
  ordering across `extends` chains, `null` whole-field clear, parent+child
  union.
- `internal/profile/validate_test.go` — packages-name regex (accepts
  `libxml2-dev`, `gstreamer1.0-plugins-bad`; rejects
  `lib xml2`, `libxml2-dev=2.12`, `libxml2;rm -rf /`).
- `internal/runtime/docker_build_test.go` — `derivedTag` determinism; no
  `ProfileName` in tag; sort-normalisation (different input order ⇒ same tag);
  identical package lists from different hypothetical profiles produce the
  same tag (cross-profile sharing invariant); Dockerfile-body synthesis
  contains the sorted, shell-quoted package list and the base-ref pinned
  `FROM`; empty-packages short-circuit; build invocation exercised against a
  fake `client.Client` like the existing `internal/runtime` fake.
- `cmd/tpod/cli_test.go` (or a new `internal/prune/prune_test.go`) —
  `tpod prune --images` removes `tpod/packages:<tag>` images, no
  catalog-liveness inference.
- `go test ./...` and `go vet ./...` must pass.

## Open sub-decisions (small; resolved by implementer)

- Whether to suppress apt's progress bar inside the synthesized Dockerfile
  (cleaner logs) vs. stream it through `ProgressWriter` (informative).
  Probably stream — matches `ImagePull` today.
- `--no-install-recommends` default: yes (matches today's Dockerfile).
- Extra apt knobs in v1 (`contrib`/`non-free` source components, `--no-install
  -recommends` toggles, version pinning, alternative suites): none — only the
  package list. v1 is simple on purpose.

## Relationship to the removed `build:` feature

`build:` was removed 2026-07-31 as dead weight and because it didn't align
with tpod's vision and was a can of worms (see the justification in
`2026-07-31-remove-dockerfile-builds-design.md`). This design brings back
the part that aligned — automated derived-image build keyed on a declarative
input — without the can of worms:

- No user-owned Dockerfile (tpod synthesises it from `packages:`).
- No `--rebuild` flag (content-addressed tag; base-image bumps invalidate
  automatically).
- No `depends_on` resolver (one derived image per `(base-image-id, packages)`
  combo; no inter-profile ordering).
- No drift detection (the tag is the hash; staleness is impossible by
  construction).
- No local-on-disk Dockerfiles or open-ended build context (synthesised
  in-memory; the only input is the package list).
