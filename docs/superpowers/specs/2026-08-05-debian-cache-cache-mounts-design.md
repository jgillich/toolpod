# Engine-native Debian package cache via cache mounts

Date: 2026-08-05

## Motivation

The derived image (`tpd/packages:<hash>`) is the only cache today: identical
(base image, packages, repos) inputs skip the build entirely. But any
cache-miss rebuild — a moved base tag (`debian:13-slim` point release), an
edited `packages:` list, or a fresh host — runs `apt-get update && apt-get
install` inside a throwaway build container whose `/var/cache/apt` dies with
it. Every `.deb` is re-downloaded from Debian mirrors on each such rebuild.

The goal: keep `.deb` files (and the apt index) warm across rebuilds on a
per-base-image basis, so only genuinely new/changed packages re-download.

This is the v2 revisit of the cache-mount option deliberately deferred in
`2026-08-01-runtime-oci-deps-design.md` ("they require BuildKit
(DOCKER_BUILDKIT=1 or rootless Podman equivalent)"). That concern is resolved
here: podman/buildah executes `RUN --mount=type=cache` natively, and Docker
only needs the `version=2` API flag — no client-side BuildKit session.

## Design

Both primary targets execute cache mounts:

- **Podman**: the compat `/build` endpoint routes to buildah, which parses
  Dockerfiles itself and implements `RUN --mount=type=cache` natively
  (present since buildah v1.23, far below the podman ≥6 floor). Podman
  ignores the `version` query param, so no request change is needed.
- **Docker**: the daemon's `/build` handler defaults to the legacy builder
  and only dispatches to its embedded buildkit when the request carries
  `version=2` (`daemon/builder/backend/backend.go`). The buildkit backend
  handles a plain `/build` request with the context from the POST body — no
  client-side buildkit session is required (sessions are only for
  secret/ssh/agent mounts). So `options.Version = types.BuilderBuildKit`
  (="2", docker SDK v27.1 `api/types/client.go`) is sufficient.

### Synthesized Dockerfile

`internal/runtime/docker_build.go` `synthesizeDockerfile` changes. Every
mounted RUN carries the two cache mounts and removes `docker-clean` at the
top of the **first** mounted RUN:

```dockerfile
FROM <base>
RUN --mount=type=cache,id=tpd-<baseID>-apt,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,id=tpd-<baseID>-<repoDigest>-lists,target=/var/lib/apt,sharing=locked \
    rm -f /etc/apt/apt.conf.d/docker-clean \
    && apt-get update \
    && apt-get -o APT::Keep-Downloaded-Packages=true install -y --no-install-recommends <sorted shell-quoted packages>
```

- `/var/cache/apt` holds downloaded `.deb`s (the package cache) and apt's
  binary `pkgcache.bin`/`srcpkgcache.bin` (regenerated each build; safe to
  share across repo sets); `/var/lib/apt` holds the apt index (`lists/`).
- The trailing `rm -rf /var/lib/apt/lists/*` from the old install RUN is
  dropped: `/var/lib/apt` is now a cache *mount*, not a layer, so it cannot
  bloat the image, and removing the lists would discard the warm index.
- `docker-clean` (which Debian official images use to delete downloaded
  `.deb`s after install) is removed at the top of the first mounted RUN. If
  it were left in place, its `DPkg::Post-Invoke` hook would purge the
  `/var/cache/apt` mount after every install and the cache could never warm.
  The keep-downloaded setting is passed as `-o APT::Keep-Downloaded-
  Packages=true` on **install** — the key apt's package cleanup reads
  (`apt-private/private-install.cc`; `update` does not read it) — scoped per
  invocation rather than written to a config file, so **no configuration
  artifact persists into the derived image**. (`Binary::apt::APT::
  Keep-Downloaded-Packages`, as in the BuildKit docs, is scoped to the `apt`
  binary and ignored by `apt-get`; the option defaults to `true` anyway, but
  the explicit flag guards bases that set it false.) The only persistent
  change is `docker-clean`'s absence, which is intended and benign at
  runtime. A custom base image that ships an equivalent cleanup hook at a
  **nonstandard** path would still purge the mount (the flag does not
  override a dpkg hook); tpd handles the standard `docker-clean` path only.
- `sharing=locked` gives apt exclusive access to the caches, matching the
  official BuildKit apt example and serializing concurrent builds on the same
  base.

The repos (`extrepo`) case mounts its ca-certificates bootstrap RUN with the
**same cache ids**, so a cold repos build populates the base index in the
lists mount (reused by the install step when the mirror honors conditional
GETs, subject to the caveat below) and warms ca-certificates' `.deb`s; its
layer stays small because `/var/cache/apt` is a mount:

```dockerfile
FROM <base>
RUN --mount=type=cache,id=tpd-<baseID>-apt,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,id=tpd-<baseID>-<repoDigest>-lists,target=/var/lib/apt,sharing=locked \
    rm -f /etc/apt/apt.conf.d/docker-clean \
    && apt-get update \
    && apt-get -o APT::Keep-Downloaded-Packages=true install -y --no-install-recommends ca-certificates
COPY extrepo/<name>.sources ...
COPY extrepo/<name>.asc ...
RUN --mount=type=cache,id=tpd-<baseID>-apt,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,id=tpd-<baseID>-<repoDigest>-lists,target=/var/lib/apt,sharing=locked \
    apt-get update \
    && apt-get -o APT::Keep-Downloaded-Packages=true install -y --no-install-recommends <sorted shell-quoted packages>
```

### Lists freshness

**No explicit TTL.** apt's own freshness governs the index: `apt-get update`
re-downloads lists whose `Release` has expired (Debian `Valid-Until` default
~10 days) or whose content changed server-side (conditional `Last-Modified`/
ETag GETs). A warm index is reused without re-download **only when the mirror
honors conditional GETs** (deb.debian.org does; arbitrary mirrors may not) —
the `.deb` reuse is unconditional, the index reuse is conditional.

This only ever applies when the install step actually re-executes, which is
exactly the case where it matters. Derived builds do **not** set
`NoCache`; buildkit's step cache only reuses the install RUN when the full
instruction chain (base image, repo COPYs, package list) matches — and that is
precisely the case where the derived image already exists and no build runs at
all. Any genuine rebuild changes the base image ID, the package list, or the
canonical repo set — the inputs `DerivedTag` hashes — which invalidates the
install RUN and re-runs `apt-get update`. The tag is name-based, not
content-based: a repo's `.sources` content changing upstream (same canonical
descriptor) does not by itself trigger a rebuild.

`--pull` re-pulls the mutable base tag but **does not by itself force a
rebuild**: if the refreshed tag still resolves to the same local image ID,
`baseID` is unchanged, the existing derived tag matches, and the derived image
is reused — so repo-side package updates are not observed. A rebuild happens
only when an input changes (`baseID` via a genuinely new base image, or the
profile's `packages:`/`repos:`). Forcing a derived rebuild regardless of
inputs is an explicit refresh flag, out of scope here.

### Cache identity

Both cache ids embed the base image's local content hash (`baseID`, the
`sha256:`-prefixed `ImageInspect.ID`, with the prefix stripped — note
`DerivedTag` keeps the prefix, so the two namespaces are distinct), and the
lists id additionally embeds the canonical repo set:

- `.deb` cache (`-apt`): `tpd-<baseID>-apt` — **base-keyed only**. A cached
  `.deb` is only reused when the *current* profile's index references the same
  filename and its size matches (apt trusts size — `pkgAcqArchive` — and
  re-downloads on mismatch; checksums are verified only for freshly downloaded
  files), and the filename encodes name_version_arch, so another profile's
  repo can never inject a different package under the same name.
- lists cache (`-lists`): `tpd-<baseID>-<repoDigest>-lists`, where
  `<repoDigest>` is the first 16 hex chars of `sha256(canonicalRepos(repos))`
  (omitted when no repos). **Base- and repo-keyed.** Index files are
  source-specific: apt's `List-Cleanup` only removes indexes for vanished
  sources when an update *succeeds* (`apt-pkg/update.cc`), so a failed or
  partial update could leave another profile's repo indexes in the shared
  cache and let packages resolve from a repo the current profile never
  declared. Keying the lists cache by `(baseID, repos)` makes that
  cross-profile contamination impossible; profiles with different repos keep
  separate index caches. 16 hex chars (64 bits, matching `DerivedTag`) keeps
  a repo-set collision negligible. The `.deb` cache stays base-keyed so the
  dominant sharing win is preserved.

Consequences:

- **Same base image → one `.deb` cache shared across all profiles** — the main
  reuse win (profile A's `git` install warms profile B's build). Lists caches
  are shared only when the repo sets match.
- **Different bases → separate caches**; codename/arch cannot collide because
  `.deb` filenames encode version and arch and the ids partition by base.
- **Base bump → new ids → cold cache**, matching the derived-tag semantics;
  the old cache becomes orphaned (engine-GC-able on Docker).

The cache mount `id` is the stable contract the design relies on; buildah's
on-disk cache layout under its cache parent is internal and version-specific,
so this spec does not depend on it (see Pruning).

### Docker request

`buildDerivedImage` sets `options.Version = types.BuilderBuildKit`; podman
ignores the `version` query param (its compat handler does not read it).

### Cache location, ownership, and pruning

The caches are engine-managed, not tpd-labeled volumes:

- **Docker**: the daemon's buildkit content store (`/var/lib/docker/buildkit`,
  rootful; the user's data dir rootless). The daemon GC prunes automatically —
  after builds, when the build cache exceeds `builder.gc.keep-storage`
  (default ~10% of disk), least-recently-used records (including cache
  mounts) are evicted; orphaned per-base caches from base bumps become
  unreferenced and evictable. Manual: `docker builder prune`.
- **Podman**: buildah's cache parent (`/var/tmp/buildah-cache-<uid>/`, or
  `$TMPDIR`/`image_copy_tmp_dir`). **Not auto-pruned**: `podman system prune`
  does not touch it (it lives outside container storage), so it accumulates
  until OS tmp cleanup or manual `rm`. Accepted for v1 and documented.

`tpd prune` and `tpd doctor` are unchanged and do not manage these caches.
tpd-owned pruning of podman's buildah cache is out of scope: it would have to
replicate buildah's internal cache-layout scheme, which may change across
buildah versions — the prune rationale must not depend on it.

### BuildKit requirement (Docker)

The Docker path always sends `version=2` and requires a daemon whose embedded
buildkit is available. tpd's documented Docker floor is already ≥27.1 (AGENTS.md
subpath note), and moby initializes its buildkit backend unconditionally
(`daemon/command/daemon.go` `initBuildkit`), so `version=2` is accepted on all
supported daemons. No new detection code; if an exotic daemon rejects
`version=2`, the `ImageBuild` error surfaces verbatim as a prepare failure.
At implementation time, confirm on a live daemon that cache mounts persist
across `/build` calls (BuildKit's documented behavior) and appear in the
daemon GC set.

### Progress output

On Docker the buildkit backend emits no classic `stream` lines — build output
rides in `aux`/`moby.buildkit.trace` protobufs, which are not decoded (see Out
of scope). Replace the build log with a TTY spinner:

- TTY: an indeterminate spinner shows a status label; `WriteProgress` updates
  the label (truncated) when lines arrive. On Docker no lines arrive during
  the build, so the label is effectively **static** — it stays at the
  pre-build `build: tpd/packages:<hash>` line written by `buildDerivedImage`.
  On Podman the label tracks the latest apt line. `WriteProgress` is called
  from the Prepare goroutine while the spinner's ticker redraws, so both are
  serialized through a mutex on the underlying stderr writer (TTY detection is
  on stderr, where the default writer prints). The spinner wraps the **active**
  writer (`opts.Progress` or the `stderrProgress` default), not only the
  default.
- Non-TTY: lines pass through to stderr exactly as today (podman still emits
  them; docker emits none).
- The spinner wraps the `Prepare` → `StartServices` phase in
  `pkg/tpd/launch.go` (all build activity). It stops on every exit path of
  that phase; errors then print normally.

Build errors stay visible on both engines. On Docker the failure surfaces
either as a real `ImageBuild` HTTP error (moby's `postBuild` returns non-200
when nothing has flushed yet) or as a terminal `msg.Error`/`errorDetail` line
in the build stream after output has flushed; `drainBuildStream` catches the
streamed form and `ImageBuild` propagates the HTTP form. Either way a failed
build returns an error after the spinner clears.

**Docker known limitations (accepted):** no per-step build log, and the
friendly "apt could not locate package(s) X" synthesis (which scans `stream`
lines) degrades to buildkit's generic exit-code error. Podman keeps the
classic log parsing and the friendly message.

## Files

- `internal/runtime/docker_build.go`:
  - `synthesizeDockerfile(baseRef, repos, packages, aptCacheID,
    listsCacheID string)` — new id params; emit the mounted install RUN (and
    the mounted ca-certificates bootstrap RUN in the repos case) with the
    scoped `docker-clean` removal and `-o APT::Keep-Downloaded-Packages=true`
    on install; drop the lists `rm`.
  - `buildDerivedImage(ctx, cli, derivedRef, baseRef, baseID, repos, packages,
    w)` — new `baseID` param; set `options.Version = types.BuilderBuildKit`;
    derive both cache ids and pass to synthesis.
  - New helper `cacheMountIDs(baseID string, repos map[string]Repo) (aptID,
    listsID string)` → `tpd-<baseID>-apt` and
    `tpd-<baseID>-<repoDigest>-lists` (repoDigest = first 16 hex of
    `sha256(canonicalRepos(repos))`, omitted when no repos; base hash has the
    `sha256:` prefix stripped).
- `internal/runtime/docker_prepare.go`: pass `baseID` to `buildDerivedImage`.
- `internal/runtime/docker_services.go`: `createService` already computes
  `baseID` (`docker_services.go:178`); pass it to `buildDerivedImage` at the
  `docker_services.go:194` call site.
- `pkg/tpd/launch.go`: spinner progress writer wrapping the **active**
  progress writer; start before `rt.Prepare`, stop after the `StartServices`
  block (deferred so it runs on error paths).
- `pkg/tpd/spinner.go` (new): TTY-detecting `ProgressWriter` with `Start`/
  `Stop`; mutex-serialized label/ticker; non-TTY falls back to line
  pass-through; label truncated.
- Docs: `AGENTS.md` derived-image/caches note (cache-mount mechanism,
  engine-managed location, podman pruning caveat, Docker buildkit limitation,
  `docker-clean` assumption on Debian official bases).

## Tests

- `internal/runtime/docker_build_test.go`:
  - `TestSynthesizeDockerfile`: update — assert the cache-mount RUN with the
    `-apt`/`-lists` ids, the in-RUN `rm -f /etc/apt/apt.conf.d/docker-clean`,
    `-o APT::Keep-Downloaded-Packages=true` on install, the absence of
    `rm -rf /var/lib/apt/lists/*`, and that the repo-less case still has a
    single `apt-get update` and no ca-certificates bootstrap.
  - New `TestCacheMountIDs` (replaces the old cache-id test idea): `.deb` id
    embeds the base hash (prefix stripped) and varies with the base only; the
    lists id additionally varies with the repo set (two profiles, same base,
    different repos → different lists ids, same `.deb` id); no-repos lists id
    has no digest component.
  - `TestSynthesizeDockerfileRepos`: assert the mounted ca-certificates
    bootstrap RUN (with the `docker-clean` removal) precedes the repo COPYs
    and the mount RUN; assert the install RUN does not re-remove
    `docker-clean`.
  - `TestSynthesizeDockerfileOrderIndependent` and
    `TestSynthesizeDockerfileShellQuotesPackages`: update for the new
    `synthesizeDockerfile` signature (they currently call the 3-arg form).
  - `TestBuildDerivedImageLabelsImage` (extend the httptest fake): assert the
    request carries `version=2` and the Dockerfile in the tar body contains
    the cache-mount lines; update the `buildDerivedImage` call for `baseID`.
  - New: the service derived-build path (`createService`) produces a build
    request whose Dockerfile carries the `baseID`-derived cache ids (assert on
    the captured build body, proving the `docker_services.go:194` call-site
    change end-to-end).
- `pkg/tpd/spinner_test.go` (new): non-TTY passes lines through; TTY renders
  frames and clears the line on `Stop`; `WriteProgress` updates the label;
  concurrent `WriteProgress` + `Stop` does not garble or race.
- `pkg/tpd/launch_test.go`: existing order/progress assertions keep passing
  (non-TTY pass-through preserves behavior).
- Integration (`internal/runtime/docker_test.go`, gated on `DOCKER_HOST`,
  skipped in `-short`, **local engine only**): prove cache reuse by counting
  requests against a controlled apt source, not by inspecting engine cache
  dirs:
  - **Local-engine gate:** the whole test is skipped when `DOCKER_HOST` is not
    a local socket/localhost — a remote engine cannot reach the test's
    mirror, so the derived build would fail and nothing could be asserted.
    (This replaces any per-assertion skip.)
  - **Mirror reachability:** the in-test HTTP server binds `0.0.0.0`, and the
    test base's source line uses the host's primary non-loopback IPv4
    (discovered in-test, e.g. via the outbound route) rather than `127.0.0.1`
    — an `httptest` server on loopback is unreachable from Docker build
    containers (RUN steps run on the default bridge; `127.0.0.1` is the
    container itself). Podman builds run with host networking and Docker's
    default bridge NATs outbound traffic, so the same host address is
    reachable from both engines' build containers.
  - Build a hermetic test base image: `FROM debian:13-slim` with the default
    Debian sources neutralized (empty `/etc/apt/sources.list.d/debian.sources`
    and `/etc/apt/sources.list`) and a `deb [trusted=yes] http://<host-ip>:
    <port>/<suite> main` source — `trusted=yes` avoids signing (no GPG in the
    test env), and neutralizing the defaults removes the deb.debian.org
    network dependency so the test passes offline.
  - The in-test HTTP server serves the apt layout apt requires, with `<arch>`
    taken from the test base image's `Architecture`:
    `dists/<suite>/Release` (with `Date`, `Valid-Until`, `Components: main`,
    `Architectures: <arch>`) plus
    `dists/<suite>/main/binary-<arch>/Packages`, plus minimal valid `.deb`
    archives (ar format: `debian-binary` + control/data tarballs) for two
    packages `pkg1`, `pkg2`. Index entries carry `Package`, `Version`,
    `Architecture`, `Filename`, `Size`, `SHA256`. `InRelease`/`Release.gpg`
    return 404 so apt's signature fallback proceeds cleanly. All artifacts are
    generated **once** at test start and served statically, so sizes/checksums
    are stable across both builds. The server counts per-URL GETs.
  - Build derived image A (base + `[pkg1]`), then derived image B on the same
    base (`[pkg1, pkg2]`). Assert `pkg1`'s `.deb` URL was requested exactly
    once across both builds — the second build reused the cached `.deb` from
    the cache mount instead of re-downloading. Works on both engines and does
    not depend on buildah's or buildkit's on-disk layout.

## Out of scope

- Decoding `moby.buildkit.trace` protobufs to restore Docker's per-step build
  log and missing-package synthesis (adds the `moby/buildkit` module; the
  spinner + generic terminal error is accepted instead).
- tpd-owned pruning of podman's buildah cache (depends on buildah's internal
  cache-layout scheme; documented-only for now).
- An explicit lists TTL (apt's `Valid-Until` + conditional GETs suffice).
- A force-rebuild/refresh flag for derived images (`--pull` already re-pulls;
  rebuilding regardless of inputs is a separate feature).
