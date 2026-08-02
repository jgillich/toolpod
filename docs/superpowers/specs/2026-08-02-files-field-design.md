# Launch-time files via a `files:` field

Date: 2026-08-02
Builds on: `2026-07-30-toolpod-design.md` (dropped `files:` in v1) and the
`packages:`/`repos:` derived-image machinery (`2026-08-01-runtime-oci-deps-design.md`).

## Goal

Add a `files:` map to profiles/fragments so a profile can write small
config/state files into the container at launch. Content is embedded inline in
the profile YAML, with `{{ }}` template resolution. Files are owned by the
execution user (the host user), live only for the launch, and vanish with the
ephemeral container.

## Why launch-time write, not image baking

`files:` is launch-time configuration. It may depend on runtime template
expansion (e.g. an auto-allocated host port from `.Ports`), which does not
exist at image build time; it must not force a derived-image rebuild when a
profile only adds a config file; and it must behave identically whether or not
the profile uses `packages:`/`repos:`. Writing files after `ContainerCreate`
satisfies all three: content is rendered at resolve time, no image machinery is
involved, and every profile gets the same behavior.

A derived-image `COPY` would couple file content to the build cache key and
couldn't express runtime-resolved values at all. Launch-time write also lets
toolpod set the correct runtime ownership directly via tar headers — a
consequence of the mechanism, not its justification.

## Schema

```yaml
files:
  ~/.config/mise/config.toml:        # target path in container; ~ → runtimeHome
    content: |                       # inline YAML block, {{ }} templates resolved
      [settings]
      task_output: parses
    mode: 0644                       # optional; default 0644
```

`internal/profile/types.go`:

```go
type File struct {
	Content string `yaml:"content"`
	Mode    uint32 `yaml:"mode,omitempty"` // default 0644; raw permission bits
}
```

`Profile` gains `Files map[string]File` with YAML key `files:`.

## Merge, validation, templates

- **Merge:** key-by-key with null-to-delete, identical to `mounts` via the
  existing `mergeMap` (`File` is a plain struct; no custom merge needed). Add
  `"files"` to `collectNullKeys`. An entry is overridden wholesale by key;
  `null` deletes an inherited entry.
- **Validation** (`validateFiles`, called from `validate()`):
  - Target key must be an absolute path (leading `/`) or `~`-prefixed; `~`
    expands to runtimeHome at resolve time, so a bare relative target is
    rejected.
  - Target must resolve to a clean path within the container filesystem:
    reject `..` path segments (before or after `~` expansion). The tar is
    rooted at `/`, so a `..` target would otherwise traverse outside the
    intended location.
  - `content` required.
  - `mode`, when set, must be a valid permission value: `0 <= mode <= 07777`.
    yaml.v3 parses a leading-zero integer literal as octal, so `mode: 0644`
    yields int 420 == `0o644` — the raw permission bits we pass straight to the
    tar header. Authors should write octal (`0644`, `0755`); `mode: 644` would
    mean `0o1204` (a legal but surprising mode). Document the octal-literal
    convention.
- **Templates + `~`:** extend `ResolveTildes` (internal/profile/paths.go) so
  the `~` in a `files:` target expands to runtimeHome (like mount targets),
  and `content` is rendered through the same `{{ }}` machinery
  (`.Env`, `uid`, `.Ports`, helpers) that `environment`/`mounts` use.
  Template rendering follows the same semantics as existing profile fields.

## Runtime

`internal/runtime/docker_run.go`, in `Run` between `ContainerCreate`
(line 117) and `ContainerStart` (line 179):

```go
if len(spec.Files) > 0 {
	if err := writeContainerFiles(ctx, d.cli, resp.ID, spec.Files, hostUID, hostGID); err != nil {
		return 3, fmt.Errorf("write profile files: %w", err)
	}
}
```

`writeContainerFiles` builds an in-memory tar (reusing the `archive/tar`
pattern already used for the derived-image Dockerfile in
`docker_build.go:tarDockerfile`) and calls:

```go
cli.CopyToContainer(ctx, containerID, "/", tarReader, container.CopyToContainerOptions{})
```

**Ownership:** files are owned by the container execution user. Today that
user is the host user (`os.Getuid()/os.Getgid()` — `containerIdentity` returns
the same value in both modes), and the bootstrap chowns `$HOME`/volumes to it
(docker_run.go:66). So every tar entry header carries `Uid: hostUID,
Gid: hostGID`. If a future mode changes the execution user, the files should
follow it — the invariant is "execution user," not "host user." Mode B's
`setpriv` fallback runs as root, which can read/write anything, so the
host-user ownership is harmless there. No Mode A/B branching in the files
path.

**Missing parent directories:** the daemon's untar (`pkg/archive.Unpack`)
auto-creates implied parent dirs at mode 0755 when a file entry's parent
doesn't exist. toolpod relies on this rather than emitting dir entries. For
targets under `$HOME`, toolpod additionally extends the bootstrap chown set
(the existing `homeParents` call, docker_run.go:65-66) with each file target's
parent dir, so the execution user can later modify the file — without that, a
root-owned 0755 parent would allow reads but block writes. Targets outside
`$HOME` (e.g. `/etc/...`) keep the daemon's root-owned 0755 parents.

**Tar paths:** entries are written with relative paths (no leading `/`), e.g.
`home/user/.config/foo`, since `CopyToContainer` untars at `dstPath "/"` and
leading slashes in tar entries have historically been a source of surprises.
`tarFiles` produces the relative path by stripping the leading `/` from the
resolved target.

`tarFiles` (the tar-construction step) is a pure function taking
`(files, uid, gid)` → `[]byte` and is unit-tested directly.

## Spec plumbing

- `internal/runtime/runtime.go` — `Spec` gains
  `Files []FileSpec{Target, Content string, Mode uint32}`.
- `pkg/tpod/spec.go` — `buildSpec` maps `cfg.Files` → `spec.Files` *after*
  `ResolveTildes` has already expanded `~` and rendered templates (so the
  runtime sees concrete targets/content and does no templating).

## Tests

- `internal/profile/merge_test.go` — files merge: override by key, null-to-delete,
  additive across extends.
- `internal/profile/validate_test.go` — bad target, missing content, bad mode.
- `internal/profile/paths_test.go` (or catalog_test.go) — `~` expansion on
  target, `{{ }}` resolution in content.
- `internal/runtime/docker_run_test.go` — `tarFiles` pure-function tests:
  entry per file, mode/uid/gid in headers, correct paths.
- `internal/runtime/docker_test.go` — integration: profile with `files:`
  writes `~/.config/tpod-test.conf` inside the container, verified by the
  launched command (`cat`), skipped in `-short`/no `DOCKER_HOST`. Include a
  case where the target's parent dir (`~/.config/...`) does not exist, to
  exercise implied-directory creation.
- `go test ./...` and `go vet ./...` must pass.

## Out of scope

- `source: <host-path>` copy (inline content only).
- Directory trees, recursion, deletion/sync semantics — files are written once
  per launch into a fresh container.
- Files persisting across runs — persistent state stays in mounts/caches.
- Writing into the image or the host; `files:` only ever touches the
  ephemeral container filesystem.
