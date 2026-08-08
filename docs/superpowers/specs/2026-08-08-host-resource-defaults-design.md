# Host resource defaults via templating

## Motivation

tpd launches disposable dev containers with no resource limits by default
(the original design is explicit: "profiles do NOT set resources, so the
container gets the host's full resources by default. Set these only to
constrain a profile."). The launch container runs arbitrary commands from a
trusted-ish host user, and a runaway process can exhaust host memory and
trip the kernel OOM killer on unrelated host processes. The goal is a
**default memory safety net** for the mise profile ecosystem without a
baked-in schema default or a silent behavior change for profiles that opt
out.

The chosen mechanism: expose the host's available resources to the `{{ }}`
template engine and express the limit as a built-in `defaults` fragment that
the `mise` profile extends.

## Context

- Template context (`internal/profile/paths.go`, `tmplData`) currently
  exposes `.Env`, `.UID`, `.Ports` and funcs `trimPrefix`, `uid`.
- `resources` is optional on profiles and fragments (types.go:125/161,
  `{memory, cpus}` strings parsed by `ParseMemoryBytes`/`ParseNanoCPUs`).
- Templates are rendered in `ResolveTildes` after `ResolveProfile`
  (merge + validate), so a template in `resources` must survive validation
  before it renders. Path validation already exempts `{{ }}` templates
  (validate.go:223, 459); resources validation has no such exemption yet.
- Profiles may `extends` fragments (bash extends `toolchain/bash`); fragments
  may only extend fragments (merge.go:120).
- 15 built-in profiles extend `mise`; each may still override `resources`
  because merge gives the leaf body precedence.
- `RAMInBytes` (docker/go-units) parses a bare integer as bytes, so a
  template rendering a raw byte count is parseable.

## Design

### 1. Template context additions

`tmplData` gains two fields, populated on the host at resolve time:

- `.MemBytes int64` — total host RAM in bytes, read from `/proc/meminfo`
  (`MemTotal`). If the file is unreadable or the key is missing, the value
  renders to empty (see §3 degradation).
- `.NumCPU int` — `runtime.NumCPU()` (logical CPUs).

The func map gains `div` (`func(a, b int64) int64`, integer division) so
fractions can be expressed in YAML. Go's `text/template` coerces the literal
`2` to `int64`, so `{{ div .MemBytes 2 }}` compiles.

### 2. Resource templating

`ResolveTildes` renders `resources.memory` and `resources.cpus` through the
same `renderTemplate` path as env/command values (each non-empty string).

Validation: when `resources.memory`/`resources.cpus` contain `{{`, skip
`ParseMemoryBytes`/`ParseNanoCPUs` in `validateResources` — mirroring the
existing path-template exemption. `spec.go` parses the rendered value and
ignores parse errors (existing behavior), so a rendered value that does not
parse degrades to unset (0 bytes / 0 CPUs → no limit), not an error.

### 3. Degradation when host data is missing

`renderTemplate` is unchanged; an empty `.MemBytes` renders the expression
to an empty string. `spec.go` already treats an empty rendered resource
value as unset (`if cfg.Resources.Memory != ""`), so the fragment degrades
to "no limit" rather than a hard error on hosts where `/proc/meminfo` is
unavailable. `div`'s contract: integer division when both operands are
non-empty numeric values, empty string output when either operand is empty
(missing host data).

### 4. The `defaults` fragment

`internal/catalog/fragments/defaults.yaml`:

```yaml
version: 1
meta:
  description: Cap the launch container to half the host's memory
resources:
  memory: "{{ div .MemBytes 2 }}"
```

The fragment is the mise ecosystem's home for default launch policies. It
currently carries only the memory cap; future defaults (env, tty, …) are
deliberate, documented additions.

### 5. mise integration

`mise` gains `extends: defaults`. The 15 built-in profiles extending `mise`
inherit the cap; any of them or a user profile can override by declaring
their own `resources` (leaf body wins in the merge). Non-mise profiles
(buzz, t3code, opencode-desktop, …) are unaffected.

### 6. Known gaps (documented, not in scope)

- Values are read on the client host; for a remote Docker engine they
  describe the client, not the daemon. Correct for tpd's primary
  rootless-podman-on-this-host target.
- The runtime sets only `Memory` (docker_run.go:117), not `MemorySwap`, so a
  capped container can still spill into host swap. A swap cap is a separate
  change.
- Derived-image builds (packages/repos) are not resource-limited; only the
  launch container is.

## Testing

- Template context: `.MemBytes`/`.NumCPU` resolve; `div` produces the
  expected integer half; missing `/proc/meminfo` degrades to empty.
- Resources rendering: a templated `resources.memory` renders and parses
  into `runtime.ResourceSpec` via `spec.go`.
- Validation: a templated value passes `validateResources`; a literal
  invalid value still fails.
- Merge: a profile extending `mise` inherits the memory cap; a profile
  declaring its own `resources` overrides it.
- Catalog: the fragment loads under `core/defaults`, carries a
  `meta.description` (`make docs` regenerates README + docs/catalog.md).
- Existing output that now reflects a limit: doctor's profile-validity
  message, `tpd show`, and dry-run for mise-derived profiles.

## Files

- `internal/profile/paths.go` — `tmplData` fields, `div` func, resource
  rendering in `ResolveTildes`.
- `internal/profile/validate.go` — template exemption in `validateResources`.
- `internal/catalog/fragments/defaults.yaml` — new fragment.
- `internal/catalog/profiles/mise.yaml` — `extends: defaults`.
- `internal/profile/paths_test.go`, `validate_test.go`, `merge_test.go`,
  `pkg/tpd/spec_test.go`, `internal/doctor/checks_test.go` — tests.
- README.md, docs/catalog.md — regenerated via `make docs`.
