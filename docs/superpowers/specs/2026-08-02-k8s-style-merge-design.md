# Kubernetes-style strategic merge for profile fields

Date: 2026-08-02
Builds on: `2026-07-30-toolpod-design.md` (§4 merge semantics),
`2026-08-01-runtime-oci-deps-design.md` (`packages:`/`repos:` merge).

Status: **Design only.** Implementation deliberately left open and not scheduled.

## Goal

Give profile fields a single, uniform merge language so *every* field composes
the same way — the way Kubernetes Strategic Merge Patch does — instead of
tpod's current ad-hoc per-field rules. The concrete driver is per-item
removal from inherited lists: today `packages` is append-with-dedup or
`packages: null` (clear-all), with no way to drop a single inherited package
(e.g. replacing `docker` with `podman-docker`).

## Current merge rules (the status quo)

| Field | Type | Merge today |
|---|---|---|
| `command` | `[]string` | replace |
| `packages` | `[]string` | append + dedup; `null` clears |
| `repos`, `mounts`, `files`, `env`, `tools`, `caches`, `labels`, `ports`, `devices` | map | key-by-key; `null` deletes a key |
| `image`, `network`, `tty`, `resources`, `dbus` | scalar/object | replace |

This is already a subset of K8s strategic merge:
- scalar list without a merge key → concat + dedup of scalars (exactly `mergePackages`),
- map with a key → merge-by-key with null-delete,
- atomic field → replace.

The gap: **no per-item operation on scalar lists.** K8s fills it by giving list
items a merge key, then supporting `$patch: delete` / `$patch: replace`.

## Proposed semantics (K8s vocabulary)

Fields keep their current merge *defaults* (so nothing breaks), and gain two
uniform directives:

### `$patch: delete` on a list item

```yaml
packages:
  - docker
    $patch: delete      # drop inherited `docker` from this profile
  - podman-docker       # append stays the default
```

Because `packages` has no natural key (items are bare strings), the item is
matched **by value**. For keyed lists of maps the match would be by the field's
merge key, exactly like K8s.

### `$patch: replace` on a whole field

```yaml
packages:
  $patch: replace
  - docker-cli
  - docker-compose
```

Replaces the entire inherited list rather than appending. For maps, `replace`
replaces the whole map (today's equivalent is nulling every key, which is
unwieldy).

### `null` retains its current meaning

`null` on a map value still deletes that key; `null` on a list still clears it
(alias for `$patch: replace` with an empty list). Backward compatible with
every existing profile.

## Open design questions (left open by intent)

1. **Shape.** Two candidate encodings:
   - (a) K8s-true: `- {package: docker, $patch: delete}` — items stay data,
     directives ride alongside.
   - (b) `$patch` as a sibling map key of the whole field (as sketched above) —
     closer to how K8s expresses whole-field `replace`.
   Both can coexist; the spec intentionally doesn't pick.
2. **Which fields adopt directives.** `packages` and `tools` (both string
   lists) are the clear first targets. Whether `command` should gain `$patch`
   semantics is unclear — it already replaces wholesale.
3. **Tools keyed by name.** `tools` is `map[string]string` today, so
   `tools: { docker-cli: null }` already removes an inherited tool. The real
   need is on `packages` (unkeyed list) — and, speculatively, unkeyed values
   like `command` and `mounts` (keyed already) need nothing.
4. **Ordering.** Whether `$patch: delete` is order-sensitive relative to
   appends (delete-then-append vs. append-then-delete) — K8s resolves by key
   set, but a scalar list needs an explicit decision.
5. **Validation.** `$patch` keys should be rejected outside their legal
   positions, and `$patch: delete` on a non-existent item should be either an
   error or a no-op (pick one).

## Library feasibility (investigated)

`k8s.io/apimachinery/pkg/util/strategicpatch` is the reference implementation
and is standalone. `StrategicMergePatch(original, patch []byte, dataStruct
interface{})` derives merge rules from Go struct tags
(`x-kubernetes-patch-merge-key`, `x-kubernetes-patch-strategy`) read via a
forked `encoding/json`. Its scalar-list behavior (concat + dedup, no merge
key) is byte-for-byte the current `mergePackages` semantics, so adopting the
library would not change existing behavior.

**Recommended against**, for three reasons:
1. **Weight.** `apimachinery` is ~5.6MB and pulls protobuf, gogo, and
   OpenTelemetry into a CLI that currently has a lean dependency tree.
2. **Fit.** strategicpatch is JSON-bytes + struct-tag driven; tpod's merge runs
   on `yaml.Node` with custom decoders (`ExtendsList`, `Mount`). Adopting it
   means re-architecting the merge to a JSON round-trip, not a drop-in call.
3. **Need.** The semantics are ~100 lines to hand-roll on `yaml.Node`, and the
   driving use case (docker→podman replacement) is arguably better served by
   fragment choice than list surgery.

If the vocabulary is adopted, hand-rolling `$patch` on the existing
`yaml.Node` merge is the preferred path — zero new dependencies, and the
semantics are already understood (they're a subset of K8s).

## Compatibility

- All existing profiles keep working: current defaults (append+dedup, map
  merge, replace) are the no-directive behavior.
- `packages: null` and `$patch: replace` with an empty list are equivalent.
- Map `null`-deletion is unchanged.
- The derived-image hash (`DerivedTag`) already feeds on the *resolved* sorted
  package list, so removal/replacement is automatically reflected in the cache
  key with no changes on the runtime side.

## Out of scope

- Applying `$patch` to `files`/`mounts`/`repos` (already keyed; `null` suffices).
- JSON Merge Patch (RFC 7386) or JSON Patch (RFC 6902) as additional syntax.
- Any change to the `extends` resolution or fragment rules.
