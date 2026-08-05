# tpd sensitive-field approval system

Status: design (2026-08-05). Companion to `docs/2026-08-03-security-model.md`,
which this design partially supersedes (see §1, Trust model shift).

## 1. Goals and trust model shift

### Goal

Add a per-field permission gate for sensitive profile fields contributed by
non-user catalog entries. The gate runs once per `(profile, sensitive-content-hash)`
tuple: first launch prompts interactively; later launches reuse the stored
choices until the hash changes, then re-prompt with prior choices preserved
(keys missing from the new item set are dropped from state).

### Trust model shift

`docs/2026-08-03-security-model.md` currently says "profiles are trusted
configuration." This design narrows that:

- A profile's **own user-authored fields remain trusted** (Namespace == "").
- Fields a **built-in** (Namespace == "core") or future **remote-namespace**
  entry contributes onto a user profile are **no longer auto-trusted** — they
  require explicit per-key approval before they reach the container.
- Launching a built-in profile directly (`tpd opencode`) is the fully-gated
  case. A pure user profile with no built-in extends is fully unguarded.

The security-model doc must be updated to record this shift.

### Sensitive fields (gated)

Top-level: `mounts`, `devices`, `environment`, `ports`, `dbus`,
`network`.

`services:` are gated **coarsely, one item per service**, addressed as
`services.<name>`. The decision is whether *this profile uses the
service at all*, not which sub-field of the shared daemon is trimmed:
a service is a global singleton (`tpd-svc-<name>`, shared across all
launches per `2026-08-04-services-design.md:127-180`), so filtering the
daemon's definition per-profile would recreate it on every disagreement
and churn every consumer. Instead, the approval gate controls *use*:
the item's value renders the service's sensitive definition so the user
sees what "use podman" entails.

The rendered definition covers the schema-valid sensitive sub-fields —
`privileged`, `exposes`, the service's own `mounts`, and `env` (the only
fields `validateServices` permits beyond `image`/`packages`/`command`/
`files`/`caches`; it rejects `network`, `dbus`, `ports`, `devices` on
services at validate.go:281-301). The render is informational — the
approval decision is per-service (approve uses the daemon as-is; deny
does not use it). A definition change (e.g. `privileged` flips, a new
`exposes` socket) changes the hash and re-prompts with the prior choice
pre-checked.

`services.<name>.{devices,ports,dbus,network}` never occur (the schema
rejects them); they are not rendered or gated.

### Whole-service deny (intentional outcome)

Deny = this launch **does not use the service**: the service is dropped
from `cfg.Services` and every top-level mount referencing it
(`service: <name>`) is cascaded off. The shared daemon is **never
filtered** — its `tpd.service-hash` never changes as a result of an
approval decision, so no recreation churn, ever. If the daemon is
already running for other consumers it keeps running; this profile
simply doesn't bind its socket. A service's `image`/`packages`/
`command` are not gated (consistent with the top-level exclusion of
those fields), so a profile that overrides a service (replacement
merge) makes the whole service user-trusted and ungated — see §2
"User-wins-when-shadowing."

### Non-gated (intentionally excluded)

`packages`, `tools`, `image`, `command`, `labels`, `resources`, `caches`,
`files`, `repos`.

Rationale: they affect the container or the derived-image build, not the
host directly. `files` and `repos` are the closest calls:

- `files` writes into the container fs (not the host).
- `repos` feeds the derived-image build; its trust anchor is already the
  existing `extrepo` TLS path documented in the security model.

Gating them would expand the prompt without adding meaningful host
protection. If the threat model later shifts, they can be added to the
sensitive set without changing the architecture.

### Advisories removed

The curated `catalog.Advisory` table
(`internal/catalog/advisories.go`) and all its call sites in
`tpd show` / `tpd edit` / `tpd init` are deleted. The approval dialog at
launch is the sole surface for sensitivity.

Trade-off (accepted): `tpd show core/creds/ssh` and `tpd init` no longer
print any pre-launch hint; the user discovers sensitivity by running the
profile. Mitigations: the dialog is shown on first launch, the stored
choices are human-readable YAML in `~/.local/share/tpd/approvals/`, and the
sensitive-field set is documented.

## 2. Provenance tracking in merge

### Data structure

Record, for each sensitive key in the resolved profile, the catalog entry
(`FullName`) that contributed it. The approval filter uses this to decide
which keys to gate.

```go
// Contributor identifies a catalog entry that contributed a sensitive
// value. Stored in provenance so the filter can decide trust without
// access to the catalog: a user entry (Namespace == "") is trusted and
// not gated; a core or remote-namespace entry is gated.
type Contributor struct {
    FullName  string // canonical catalog key: "core/creds/ssh", "ssh", "github.com/foo/bar"
    Namespace string // "" for user, "core" for built-ins, or a remote namespace
}

// Trusted reports whether this contributor is user-owned and therefore
// not subject to the approval gate.
func (c Contributor) Trusted() bool { return c.Namespace == "" }

// Provenance records, for each sensitive key, the Contributor that last
// wrote it. Keys whose final value came from a user entry are not gated;
// keys from a core/remote entry are.
type Provenance struct {
    Mounts   map[string]Contributor     // mount target   -> contributor
    Devices  map[string]Contributor     // device target  -> contributor
    Env      map[string]Contributor     // env var        -> contributor
    Ports    map[string]Contributor     // container port -> contributor
    Dbus     DbusProvenance             // talk/own sub-maps keyed by bus name
    Network  Contributor                // zero-value Trusted() == true if unset or user-owned
    Services map[string]Contributor     // service name -> contributor (services can't extends)
}

type DbusProvenance struct {
    Talk map[string]Contributor // bus name -> contributor
    Own  map[string]Contributor // bus name -> contributor
}

// ServiceProvenance is collapsed to Contributor: services merge
// shallowly and cannot extends, so a whole service always has exactly one
// contributor. See §2 "Services".
type ServiceProvenance = Contributor
```

`Network` is a scalar (single slot, last-writer-wins like `image`), so its
provenance is a single `Contributor` (zero-value `Contributor{}` has
`Namespace == ""` and is trusted, matching "unset or user-owned").

### Merge semantics

`MergeProfiles` already follows *child wins per key* for maps and *child
wins* for scalars. Provenance follows the same rule: when `mergeMap` /
`mergeStringMap` / `mergeMounts` / etc. copies a key from `child` into
`out`, the provenance map for that field records `Contributor{FullName:
child.FullName(), Namespace: child.Namespace}` for that key. Keys
retained only from `parent` keep the parent's recorded provenance.
`null`-to-delete removes the key from both the value map and the
provenance map.

`resolveChain` (merge.go:83-102) calls `MergeProfiles(merged, parent)`
then `MergeProfiles(merged, rc)`. In both calls the `child` argument is a
`RawProfile` with valid `Namespace`/`Name`, so `child.FullName()` and
`child.Namespace` are available inside the merge without threading new
parameters. Parent keys already have their provenance recorded from prior
merges (the accumulated `merged` carries the provenance built up across
the chain), so the merge only needs to stamp the child `Contributor` onto
keys copied from `child` and leave parent-contributed provenance
untouched. No signature change to `MergeProfiles`.

### Leaf initialization (no-extends case)

`resolveChain` (merge.go:62-63) returns the leaf `RawProfile` directly
when a profile has no `extends`, bypassing any merge. A built-in leaf
profile (e.g. `core/bash`) would therefore have **empty provenance** and
bypass all gates — a bug. The fix: `resolveChain` initializes the leaf's
`Provenance` by stamping `Contributor{FullName: rc.FullName(), Namespace:
rc.Namespace}` onto every sensitive key the leaf itself declares before
returning. Equivalently, `resolveChain` can always merge the leaf against
an empty `RawProfile` (which stamps the leaf's own contributions) — but
the direct init is cheaper and avoids a no-op merge. The
`ResolveProfileWithProv` wrapper ensures this runs for both the leaf and
non-leaf paths so no built-in profile escapes the gate.

### Storage on RawProfile

Add `Provenance Provenance `yaml:"-"`` to `RawProfile` (non-YAML, like
`Namespace` / `Name` / `Path`). The existing `Profile` type does **not**
carry provenance (it's a YAML-shaped struct); `ResolveProfile` returns a
new `(Profile, Provenance)` pair via a wrapper — see §3.

### Map aliasing

`MergeProfiles` starts with `out := parent` (merge.go:121), which copies
the struct header. The value maps are rebuilt fresh by `mergeMap` /
`mergeStringMap` (`make(...)` at merge.go:183, 242), so value maps are
safe. The provenance maps must follow the same pattern: every merge
helper allocates a fresh `map[string]Contributor` and copies entries from
`parent`'s provenance for retained keys, then writes the child
`Contributor` for keys copied from `child`. Provenance maps are never
mutated in place on `parent`, or the shallow copy would alias and
corrupt the parent's view.

### User-wins-when-shadowing

Because provenance follows child-wins, a user profile that re-declares
`mounts: { ~/.ssh: { source: ... } }` over a built-in `ssh` fragment's
`~/.ssh` key stamps that key's provenance as a **user** `Contributor`
(`Namespace == ""`, `Trusted() == true`). The approval filter skips any
key whose contributor is trusted. This falls out of the merge rule for
free and matches the trust model (the user explicitly opted into this
value).

### Services

Services merge shallowly via `mergeMap(parent.Services, ...)`
(merge.go:158): a `Service` is a single map value, so the child's entire
service struct replaces the parent's — there is no deep sub-field merge,
and existing tests assert this. A service may not `extends`
(validate.go:309), so a whole service always has exactly one contributor.
`Provenance.Services` is therefore `map[string]Contributor` (service name
→ contributor). The approval filter treats a service as gated if its
contributor is non-user, and gates the service's **schema-valid
sensitive sub-fields** (`Mounts`, `Env`, `Privileged`, `Exposes` — see
§1) as a unit under that one contributor. `Devices`, `Ports`, `Dbus`,
and `Network` are rejected by `validateServices` and are never gated for
services. The merge contract for services is unchanged —
replacement-based, as today.

### Cost

~6 small map operations added to `MergeProfiles`, all following the
existing merge pattern. No signature change. Tests in
`internal/profile/merge_test.go` and `internal/profile/extends_test.go`
extend to assert provenance on the merged result.

## 3. Resolve API and the approval filter

### Resolve API change

Today: `ResolveProfile(cat, name) (Profile, error)`. To expose provenance
without polluting the YAML-shaped `Profile`, introduce a wrapper:

```go
// Resolved is a fully merged profile plus the provenance of its sensitive
// fields and the catalog identity of the resolved entry. Returned by
// ResolveProfileWithProv. ResolveProfile is kept as a thin wrapper that
// discards provenance, for callers that don't gate (tpd show --resolved,
// etc.).
//
// FullName is the resolved catalog key (e.g. "core/opencode", "lang/go",
// "myagent") — the stable identity used to key the approval state file
// (§4). DisplayName is the unqualified name for human-facing output in
// the dialog.
type Resolved struct {
    Profile
    Prov         Provenance
    FullName     string
    DisplayName  string
}

func ResolveProfileWithProv(cat Catalog, name string) (Resolved, error)
```

`ResolveProfile` stays for `tpd show --resolved` and other non-launch
callers; it calls `ResolveProfileWithProv` and returns just the `Profile`.
`ResolveFragment` gets the same treatment
(`ResolveFragmentWithProv`) for symmetry, though fragments aren't launched.

### Approval filter

New `internal/approval` package with the gate logic (no UI here — that's
injected). The filter takes a `Resolved` (which carries `FullName`,
`DisplayName`, `Prov`, and the `Profile`) and produces the **gated
`Profile`** (denied keys dropped) plus a **prompt request** describing
what still needs a decision:

```go
// internal/approval/filter.go

// SensitiveItem is one gated key the user must decide on.
type SensitiveItem struct {
    Field    string // "mounts", "devices", "env", "ports",
                    // "dbus.talk", "dbus.own", "network",
                    // "services" (one item per service, Key = service name)
    Key      string // the map key ("" for scalar network/privileged);
                    // pre-expansion — this is the stable identity used by
                    // the hash and the store
    Value    string // canonical pre-expansion rendering of the field's
                    // value, for dialog display. Literal "{{ ... }}" if
                    // the profile templated the value. The dialog shows
                    // this as-is; see §6 "Rendering" for why display-time
                    // expansion is out of scope for v1.
    Source   Contributor // the contributor that wrote this key
}

// PromptRequest is what the dialog renders. Empty Items = no prompt needed.
type PromptRequest struct {
    ProfileName  string // Resolved.DisplayName
    FullName     string // Resolved.FullName (for diagnostics)
    Hash         string
    Items        []SensitiveItem
    // PriorChoices carries the stored allowed-set keyed by Field/Key, so the
    // dialog can pre-check toggles that were approved before the hash changed.
    PriorChoices map[string]map[string]bool // field -> set of allowed keys
}

// Filter returns the profile with denied/dropped fields removed and a
// PromptRequest describing any still-unapproved sensitive fields. If
// PromptRequest.Items is empty, no prompt is needed and the filtered
// profile is ready to launch. The store is keyed by res.FullName.
func Filter(res profile.Resolved, store Store) (profile.Profile, PromptRequest, error)
```

### Filter algorithm

1. Walk `Prov` and `Profile` together over the gated fields.
2. Compute the hash (§4) from the non-user sensitive fields of the
   resolved profile (pre-filter).
3. Load the stored `State` for `res.FullName` (the resolved catalog key,
   not the display name — see §4).
4. If the stored hash == current hash: reconcile stored choices against
   the current non-user sensitive key set. Keys present in the stored set
   but **missing from the new key set** are dropped from the state. If
   the reconciled state differs from the loaded state, `Filter` writes it
   back via `store.Save` **before returning**, regardless of whether a
   prompt follows. This is the carry-forward cleanup: after a hash
   change, the dialog/`--yes`/`--no` merge carried the old state into the
   new hash, and the re-filter's reconcile drops keys that no longer
   exist in the profile; it also cleans hand-edited state files. Note the
   reconcile does **not** prevent a removed-then-re-added key from
   restoring its approval: removing a key changes the hash, so the
   `st.Hash == current hash` guard skips reconciliation for that change,
   and re-adding identical content matches the old hash and restores the
   choice — benign, since the user approved identical content. Keys still
   present keep their prior allowed/denied choice and feed
   `PriorChoices`.
5. For each key whose provenance is a **non-user** entry:
   - **Approved** (stored, hash matches) → keep in the profile.
   - **Denied** (field present in stored state, key absent from its
     approved list, hash matches) → drop from the profile
     (drop-and-continue).
   - **No stored choice for this hash** (field absent from stored state,
     or hash differs and the key is new) → add to `PromptRequest.Items`.
     The key's current value stays in the profile *for now*; the dialog
     returns the final allowed-set and the filter is re-run.
6. For each key whose provenance is a **user** entry → keep, no gate.
 7. **Dependent-mount cascade (coarse services):** if a service is
    denied (removed from `cfg.Services`), every top-level `mounts` entry
    with `service: <name>` is also dropped — the main container cannot
    bind a socket the service isn't exposing to it, and
    `validateMountServices` (validate.go:374-382) would otherwise fail at
    service binding (the mount requires the service to be declared). The
    cascaded mounts are not themselves gated keys; they are removed as a
    consequence of the service denial. A dialog that denies a service
    should surface the dependent mount(s) so the user sees the cascade.
 8. Return the filtered `Profile` and the `PromptRequest`.

### Dialog return shape

The dialog returns the final `map[field]set[key]bool` of allowed keys for
this hash. For each **decided field** the set is the complete allowed-set
for that field — prior-approved keys arrive pre-checked (from
`PriorChoices`) and are included unless the user un-toggles them, so the
returned set **replaces** the stored choices for that field, it does not
union with them. `--yes` sets every item's key to true; `--no` sets
every item's key to false. `Launch` then:

1. Merges the choices **per-field** into the loaded prior state via
   `mergeChoicesIntoState`: decided fields replace their stored keys;
   fields not present in the current item set keep their stored choices
   untouched. Writes the merged state to the store (`store.Save`) under
   the current hash.
2. Re-runs `Filter` with the now-populated store. Because every item now
   has a stored choice for the current hash, `PromptRequest.Items` is
   empty and `Filter` returns the filtered profile directly.

This is a single re-filter call, not a double pass: the first `Filter`
discovers what needs a decision; the dialog resolves it; the second
`Filter` applies it. The filtered `Profile` from the second call is what
flows into `buildSpec`.

**Partial results fail closed.** If the dialog returns a map that leaves
any `PromptRequest.Item` without a choice (the user submitted without
deciding every row), `Launch` does **not** save and returns
`Result{ExitCode: 2, Err: "approval incomplete: <undecided fields>"}`.
This is distinct from an explicit abort (Ctrl+C → "approval declined"):
an incomplete submission is a user error to surface, not a choice to
persist. `--yes`/`--no` cannot produce a partial result (they decide
every item).

### Empty prompt short-circuit

If `PromptRequest.Items` is empty after reconciliation (every non-user
sensitive key already has a stored choice for the current hash), no prompt
is shown. This is your "If empty, permission prompt is skipped" rule. It
also covers the case where the profile has no non-user sensitive fields
at all.

### Where Filter is called

Inside `pkg/tpd.LaunchWithWriter`, immediately after
`ResolveProfileWithProv` and **before** `buildSpec`. The filter runs on
the **unexpanded** `Resolved` — template values stay literal, because the
hash (§4) and approval keys are pre-expansion (a changed host env var
must not invalidate approvals). The filtered `Profile` flows into
`buildSpec`, which calls `ResolveTildes` as it does today (spec.go:30) —
no refactor of `buildSpec` or reordering of port allocation.

The dialog (§6) shows the **canonical pre-expansion** value from
`SensitiveItem.Value` — it does not expand templates. Display-time
expansion would require mode, host/runtime home, and allocated `.Ports`
values that are only available inside `buildSpec` (spec.go:24-33); threading
them into the prompt API would couple the gate to the spec builder and
complicate injection. v1 shows the literal profile-declared value (e.g.
`{{ .Env.XDG_RUNTIME_DIR }}/{{ .Env.WAYLAND_DISPLAY }}`); a future
revision may add a typed renderer with an expansion context if users find
the opaque templates hard to trust. The choices the dialog returns are
keyed by `SensitiveItem.Key` (the unexpanded key), so they map directly
onto the hash and the store without translation.

The DryRun path uses the same filter so `--dry-run` reflects the approved
set.

## 4. Hash, state file, and Store interface

### Hash

New `internal/approval/hash.go` (parallel to
`internal/profile/hash.go`'s `computeServiceHash`). The hash covers
**non-user sensitive fields only**, **pre-template-expansion**, of the
resolved profile — i.e. the profile-declared values exactly as they
appear after `extends` merge, with `{{ ... }}` templates left literal:

```go
func ComputeApprovalHash(res profile.Resolved) string
```

- Walk each gated field; for each key whose `Provenance` contributor is
  non-user (`!contributor.Trusted()`), emit
  `<field>\n<key>\n<contributor FullName>\n<contributor Namespace>\n<value-canonical>\n`
  into a sha256 hasher, sorted by (field, key, contributor).
- **Contributor identity is part of the hash.** The dialog asks the user
  to trust a *named* contributor ("core/creds/ssh wants to mount
  ~/.ssh"), so the approval must bind to that contributor. If
  `core/creds/ssh` is later replaced by a remote-namespace contributor
  with identical mount values, the hash changes and the stored approval
  does not silently transfer. The `Namespace` is included alongside the
  `FullName` so a user shadow (`ssh`, Namespace `""`) and a built-in
  (`core/ssh`) with the same FullName-leaf are also distinct — though in
  practice the user shadow is trusted and never hashed.
- `value-canonical` is the explicit-field write form used by
  `computeServiceHash` (e.g. for a Mount: `mount %s %s %s %s %v %v %v\n`
  with target, source, service, socket, readOnly, optional, create).
  **Templates stay as literal `{{ ... }}` strings** — the hash captures
  what the profile *declares*, not what the host environment resolves it
  to. A changed `$DISPLAY` or `$XDG_RUNTIME_DIR` must not invalidate
  approvals; the approval is for the profile's intent, and the expansion
  is a per-launch runtime fact.
- `network` is a scalar: emit `network\n<contributor>\n<value>\n` if
  non-user-contributed and non-empty.
- Services: emit one `services\n<name>\n<contributor FullName>\n<contributor Namespace>\n<rendered-definition>\n`
  entry per non-user-contributed service. `<rendered-definition>` is the
  canonical pre-expansion form of the service's schema-valid sensitive
  sub-fields (`privileged`, `exposes`, the service's own `mounts`, `env`)
  — e.g. `privileged=true; exposes={podman:/run/podman/podman.sock};
  mounts={...}; env={...}`. A definition change (a `privileged` flip, a
  new `exposes` socket, a new service mount/env key) changes the
  rendered form and therefore the hash, re-prompting with the prior
  choice pre-checked. The validator-rejected fields (`devices`, `ports`,
  `dbus`, `network`) never occur on services and are not rendered.
- Return `hex.EncodeToString(sum[:])[:12]` (12 chars, same as service
  hash).

A user-only change to a non-sensitive field never touches this hash. A
user re-declaration of a core key changes that key's provenance to user,
removing it from the hash input → hash changes → reprompt, but the new
item set has fewer items (that key is no longer gated). Prior choices for
that key are dropped via the reconciliation rule. A host environment
change (`$DISPLAY`, `$XDG_RUNTIME_DIR`, etc.) does **not** touch the
hash — the same profile on the same machine (or a different machine)
hashes identically and approvals persist. Expansion is a display-time
concern only (§6).

### State file

`~/.local/share/tpd/approvals/<profile-key>.yaml`, one file per profile,
nested by `/`. The `<profile-key>` is the **resolved catalog `FullName`**
(e.g. `core/opencode`, `lang/go`, `myagent`), **not** the display name the
user typed. `tpd core/opencode` and `tpd opencode` (when no user shadow
exists) both resolve to `core/opencode` and must share one approval file;
keying by `opts.ProfileName` would split state across invocations of the
same resolvable entry. The `FullName` is what `Catalog.ResolveRef` returns
(ref.go:59-69) and is the stable identity of the resolved entry.

Parent dirs are created with `0o700`; the file is `0o600` (it records
trust decisions). `FullName`s containing `/` map to a nested path
(`lang/go` → `approvals/lang/go.yaml`). The store **validates each
`FullName` component** before joining it into a filesystem path:
`profileNameRe` (`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`) is applied to every
`/`-separated segment, rejecting `..`, empty segments, and anything
outside the safe charset. This matters for future remote namespaces
(e.g. `github.com/foo/bar`), whose components are not today passed
through `ValidateName` on the load path. The store does not rely on the
loader's validation alone.

```yaml
# ~/.local/share/tpd/approvals/core/creds/ssh.yaml
profile: ssh          # display name, for human readers
hash: a1b2c3d4e5f6    # current approved hash
approved:
  mounts:
    - ~/.ssh
    - ~/.ssh/known_hosts
  devices:            # absent key = field present in profile, all denied
  env:
    - DOCKER_HOST
  ports:              # absent key = field present in profile, all denied
  dbus:
    talk: [org.freedesktop.portal.Desktop]
    own:              # absent key = sub-field present, all denied
  network: true       # scalar: bool, approved or not
  services:           # coarse: approved service names
    - podman
```

`services` is a plain map field (like `mounts`): `Approved["services"].Keys`
is the list of approved service names. A service present in the profile
but absent from the list is denied (dropped + cascaded). No per-sub-field
service keys; the service is approved or denied as a whole.

### State semantics (three-state, round-trippable)

Each gated field has three states that must survive a YAML round-trip:
**approved** (key listed), **denied** (field present in profile but key
absent from the list), and **never decided** (field absent from the
profile, so the key is not in the list and not in the profile either —
the filter treats this as "not applicable, no prompt"). The distinction
that matters for the filter is:

- A key **in the approved list** for a field → kept.
- A key **absent from the approved list** while the field key is present
  in the profile → denied (dropped).
- A field **absent from the `approved:` map entirely** while the field is
  present in the profile → no stored choice for any of its keys → all its
  keys prompt.

`gopkg.in/yaml.v3` marshals nil and empty slices identically (both as
`[]`), so a flat `map[string][]string` cannot distinguish "field present,
all denied" from "field absent, never decided." The design therefore uses
a custom `MarshalYAML`/`UnmarshalYAML` on `State` that:

1. Emits a field key with an empty value (or `[]`) when the field is
   present in the `approved` map with an empty `Keys` slice (denied).
2. Omits the field key entirely when the field is absent from the
   `approved` map (never decided).
3. Nests `dbus` as `dbus: {talk: [...], own: [...]}` for readability (the
   Go struct is flat — `Approved["dbus.talk"]` — but the on-disk shape
   is nested). All other fields, including `services`, are flat lists of
   approved keys.

The `Network` scalar uses `*bool`: `nil` omits the key (never decided),
`true`/`false` emit the bool.

### Store interface

Injected via `LaunchOpts.ApprovalStore` (default impl reads/writes the
state dir):

```go
// internal/approval/store.go
type Store interface {
    // Load returns the stored approval state for profileName, or a zero
    // State (empty approved sets, empty hash) if no file exists yet.
    Load(profileName string) (State, error)
    // Save writes the state atomically (temp file + rename) to the state dir.
    Save(profileName string, s State) error
}

type State struct {
    Profile  string
    Hash     string
    Approved map[string]ApprovedField // field -> approved keys (or network scalar)
}

// ApprovedField represents one field's approved set. Map fields use Keys;
// the scalar `network` uses Network: a pointer to distinguish three states
// (nil = never decided → prompts; true = approved; false = denied).
//
// The three-state distinction is preserved on disk via custom
// MarshalYAML/UnmarshalYAML on State — see "State semantics" above.
type ApprovedField struct {
    Keys    []string // for map fields: mounts, devices, env, ports,
                     // dbus.talk, dbus.own, services (service names)
    Network *bool    // for the `network` scalar only; nil for map fields
}
```

The `State` struct marshals to the YAML in §State file: map fields
become lists; `network` becomes a bool (omitted from YAML when `Network`
is nil, so the field is absent rather than `false`). Tests inject a fake
`Store` (in-memory map).

### Prune

`tpd prune` is **not** taught about `~/.local/share/tpd/approvals/` —
the files are tiny, human-editable, and not engine resources; a stray
approval file for a deleted profile is harmless. A future
`tpd approval list` / `reset` command is out of scope here.

### Concurrent-launch store race (known limitation)

The store is a load-modify-write file with no cross-process lock. Two
simultaneous launches of the same unapproved profile will both prompt
and both `Save`; the second write wins and the first user's choices are
lost. This is acceptable for a single-user CLI (concurrent launches of
the same profile are rare and the consequence is a re-prompt, not data
loss). A file lock or atomic compare-and-swap would close it; out of
scope for v1.

## 5. Launch integration

### LaunchOpts additions

```go
type LaunchOpts struct {
    // ... existing fields ...

    // In is the input reader for the interactive approval dialog. If nil,
    // os.Stdin is used. The TTY check (IsTerminal) runs against this reader
    // when it is an *os.File. Injectable so tests can drive the prompt with
    // a pipe and exercise the non-interactive error path deterministically.
    In io.Reader

    // ApprovalStore persists per-profile approval choices. If nil, a
    // default file-system store rooted at ~/.local/share/tpd/approvals/
    // is used. Injectable for tests.
    ApprovalStore approval.Store

    // ApprovalPrompt renders the interactive approval dialog and returns
    // the user's choices as a map[field]set[key]bool. If nil, a default
    // huh-based TUI is used. The prompt is only invoked when In is a TTY;
    // if In is not a TTY and a prompt is required, Launch returns an error
    // unless AssumeYes or AssumeNo was passed.
    ApprovalPrompt approval.Prompt

    // IsTTY reports whether In is an interactive terminal. If nil, a
    // default is used. The existing internal/ui.IsTTY takes an io.Writer
    // and type-asserts *os.File (ui.go:18-23), so it cannot be delegated
    // to directly for an io.Reader. The default here is a small wrapper
    // that type-asserts In to *os.File and calls term.IsTerminal on its
    // Fd (mirroring ui.IsTTY's logic but on the reader side). Injectable
    // so tests can force the non-TTY path without piping.
    IsTTY func(io.Reader) bool

    // AssumeYes auto-approves all currently-unapproved sensitive fields
    // and persists the choice (equivalent to --yes). No prompt is shown.
    AssumeYes bool
    // AssumeNo auto-denies all currently-unapproved sensitive fields and
    // persists the choice (equivalent to --no). No prompt is shown.
    AssumeNo bool
}
```

`approval.Prompt` signature:

```go
type Prompt func(req PromptRequest, stdin io.Reader, stdout io.Writer) (map[string]map[string]bool, error)
```

The CLI wires `In: os.Stdin`, the real `IsTTY` (delegating to
`internal/ui.IsTTY`), and the huh-based prompt (§6); tests wire a fake
`Prompt`, a fake `Store`, and either a pipe for `In` (non-TTY) or a fake
`IsTTY` that returns true (interactive path without needing a real
terminal). `LaunchWithWriter` keeps its existing `w io.Writer` for
progress/spec output; approval dialog I/O goes through `opts.In` and `w`.

### LaunchWithWriter flow

```
1. LoadProfiles
2. ResolveProfileWithProv  -> Resolved{Profile, Prov, FullName, DisplayName}
3. filteredProfile, promptReq, err := approval.Filter(resolved, store)
4. if promptReq.Items empty:
       proceed to buildSpec with filteredProfile
   else if AssumeYes or AssumeNo:
       choices = every item's key = (AssumeYes ? true : false), merged with prior
       effectiveStore = store
       if DryRun:
           effectiveStore = ephemeralStore(store)   // wraps store with choices in-memory, no Save
       else:
           store.Save(choices)
       filteredProfile = re-filter(resolved, effectiveStore)
   else if DryRun || !opts.IsTTY(opts.In):
       return Result{ExitCode: 2, Err: "unapproved sensitive fields require --yes or --no: <list>"}
   else:
       choices := ApprovalPrompt(promptReq, opts.In, w)
       if aborted: return Result{ExitCode: 2, Err: "approval declined"}
       if incomplete: return Result{ExitCode: 2, Err: "approval incomplete: <undecided>"}
       store.Save(choices); filteredProfile = re-filter(resolved, store)
5. buildSpec(filteredProfile, ...)   // ResolveTildes runs inside buildSpec as today
6. ... existing Prepare/Run pipeline ...
```

"re-filter" in step 4 means a second `approval.Filter(resolved, effectiveStore)`
call. `ephemeralStore` is an in-memory `Store` wrapper seeded with the
loaded state plus the just-made `choices`, so the second filter sees a
fully-populated state without touching disk — this is what makes
`--dry-run --yes`/`--no` print a spec with denied fields actually
removed. For the non-DryRun `--yes`/`--no` and interactive paths,
`effectiveStore` is the real store after `Save`. The `resolved` passed
to both filter calls is the **unexpanded** profile; `buildSpec` expands
it after the gate.

The `--yes` and `--no` flags persist their choices to the state file (when
not DryRun), so subsequent non-interactive launches reuse them. A later
launch with a changed hash re-prompts (or re-errors in non-interactive
mode).

### DryRun

`--dry-run` is a read-only inspection command: it must not write approval
state or block on input. The whole dry-run flow runs against a
**`ReadOnlyStore`** (Load delegates to the real store, Save is a no-op),
so the initial `Filter` can read stored approvals — an already-approved
profile must not prompt — while its reconciliation write-back never
touches disk. If a prompt would be required (there are unapproved
sensitive items and neither `--yes` nor `--no` was passed), DryRun errors
with exit code 2 and the same "requires --yes or --no" message as
non-interactive mode — it does not invoke the dialog and does not persist
state. With `--yes` or `--no`, DryRun applies the choice through an
**ephemeral in-memory overlay store** (see step 4 of the flow): the
re-filter sees the choices without a `Save`, so the printed spec reflects
denied fields actually removed. No state file is written. This keeps
`--dry-run` side-effect-free while showing what would launch.

## 6. Dialog (huh-based)

### Library

The interactive dialog uses [charmbracelet/huh](https://github.com/charmbracelet/huh)
v1.0.0, already a dependency and already used by `internal/scaffold/scaffold.go`
(`huh.NewMultiSelect`, `huh.NewConfirm`).

### Rendering

The dialog groups sensitive items by **contributing leaf** (the catalog
entry that last wrote the key, per provenance), then by field. Each item
is a row in a `huh.NewMultiSelect[string]` (or several, if the item set
is large — huh handles paging). Toggle on/off with space; Enter
confirms. Default state: **all off** (per your spec), unless a key was
previously approved and the hash changed (then `PriorChoices` pre-checks
it).

**Values are shown pre-expansion.** `SensitiveItem.Value` carries the
canonical profile-declared form, literal templates included. v1 does not
expand templates in the dialog (see §3 "Where Filter is called" for why).
The user sees `{{ .Env.XDG_RUNTIME_DIR }}/{{ .Env.WAYLAND_DISPLAY }}`
rather than the resolved `/run/user/1000/wayland-0`; the choice is keyed
by the unexpanded `Key`, so a changed host env var does not invalidate
the approval. A future revision may add display-time expansion.

**Contributor granularity:** the group header is the *contributing leaf*,
not the profile the user named on the command line. A profile extending
`core/lang/typescript` (which itself extends `core/lang/javascript`,
typescript.yaml:2) will prompt with headers like "from
core/lang/javascript" for keys that `javascript` contributed. This is
correct — provenance attributes to the writing entry — but the dialog
should make clear that the header is the contributor, not the named
extends target.

```
tpd: myagent wants the following from core/creds/ssh
  [x] ~/.ssh                    (mount, read-only)
  [ ] ~/.ssh/known_hosts        (mount, read-write)

tpd: myagent wants the following from core/services/docker-host
  [ ] DOCKER_HOST=unix:///var/run/docker.sock   (env)

tpd: myagent wants the following from core/gui
  [ ] /dev/dri                  (device)
  [ ] /tmp/.X11-unix            (mount, optional)
  [ ] {{ .Env.XDG_RUNTIME_DIR }}/{{ .Env.WAYLAND_DISPLAY }}  (mount, optional, templated)

tpd: myagent wants the following from core/services/podman
  [ ] podman                    (service: privileged sidecar; exposes podman → /run/podman/podman.sock)

  <Approve>  <Abort>
```

The service item is a single toggle per service. Its rendered value
shows the service's sensitive definition (privileged, exposes, service
mounts/env) so the user knows what "use podman" entails. Denying means
this profile won't use the service (the socket mounts cascade off); the
shared daemon is never filtered.

(Exact rendering TBD at implementation; the contract is: per-item toggle,
default off, prior-approved keys pre-checked, **pre-expansion values
shown**, group header is the contributing leaf, an explicit abort path,
and a fail-closed check for incomplete submissions.)

### Abort

If the user aborts the dialog (Esc / Ctrl+C), Launch returns
`Result{ExitCode: 2, Err: "approval declined"}`. No state is written.

### Non-interactive

`approval.Prompt` is only invoked when `opts.IsTTY(opts.In)` returns true.
If `In` is not a TTY and `AssumeYes`/`AssumeNo` are both false and
`promptReq.Items` is non-empty, Launch fails with exit code 2 and a
message listing the unapproved fields and the `--yes`/`--no` hint. This
is your "non-interactive shell with missing approvals is an error" rule.
The `IsTTY` injection lets tests force either path without a real
terminal.

## 7. CLI flags

`--yes` and `--no` are added to the shared launch flags
(`addLaunchFlags` in `cmd/tpd/cli.go`), so they apply to both
`tpd <profile>` and `tpd run <profile>`:

```go
cmd.Flags().BoolVar(&o.AssumeYes, "yes", false, "Auto-approve all unapproved sensitive fields and persist the choice.")
cmd.Flags().BoolVar(&o.AssumeNo, "no", false, "Auto-deny all unapproved sensitive fields and persist the choice.")
```

`--yes` and `--no` are mutually exclusive (cobra `MarkFlagsMutuallyExclusive`).
They are **not** given a short form, to avoid clashing with any future
short flags and to keep the trust decision deliberate.

The flags are passed through `runLaunch` → `tpd.LaunchOpts.AssumeYes` /
`AssumeNo`.

### Semantics: prior denials persist

**When the stored hash matches the current hash**, `--yes` and `--no`
only decide **currently-unapproved items** (keys in `promptReq.Items`
with no stored choice for this hash). A key previously **denied**
(stored, hash matches, key absent from the approved list) stays denied —
`--yes` does not override prior denials, and `--no` does not re-deny
keys that are already approved. This makes the flags idempotent and
script-safe: a pipeline running `tpd --yes <profile>` does not silently
re-approve a key the user explicitly denied on an earlier interactive
launch.

**When the hash changes**, every key is treated as undecided (the design
re-prompts with prior choices pre-checked in `PriorChoices`), so `--yes`
also approves keys that were denied under the old hash — the denial was
made against the old definition and does not carry to the new one. The
dialog path has the same shape: prior-approved keys are re-decided as
pre-checked items, and the user's final per-field selection **replaces**
the stored choices for that field (pre-checked items the user un-toggles
become denied). Fields not present in the current item set keep their
stored choices untouched (see §3 "Dialog return shape" — the merge is
per-field, preserving fields the dialog didn't decide).

To un-deny a previously-denied key without a hash change, the user edits
the state file (or deletes it to reset). A future `tpd approval reset`
command is out of scope here.

## 8. Advisory removal

Delete:

- `internal/catalog/advisories.go` (the whole file).
- The `catalog.Advisory(...)` call sites and their helpers:
  - `cmd/tpd/cli.go`: the `if msg := catalog.Advisory(advisoryName(key)); msg != "" { ... }`
    blocks in `runShow` (lines ~160, ~174) and `runEdit` (lines ~185,
    ~228), and the `advisoryName` helper (lines ~551-554).
  - `internal/scaffold/scaffold.go`: the advisory-print block (lines
    ~205-206) and the `advisoryLeaf` helper (lines ~396-398).
- The advisory tests:
  - `internal/catalog/catalog_test.go`: any `catalog.Advisory` tests.
  - `cmd/tpd/cli_test.go`: `TestShowDockerPrintsSensitiveAdvisory`
    (line ~138) and `TestEditDockerPrintsSensitiveAdvisory` (line ~151).
  - `internal/scaffold/scaffold_test.go`:
    `TestScaffoldPrintsAdvisoryForSensitiveFragments` (line ~593).

Update `docs/2026-08-03-security-model.md`:

- The intro line "as of 2026-08-03" (line ~8) gains the approval-system
  date so the doc records both hardening snapshots.
- The "Credential-fragment advisories" section (lines ~41-45 and the
  closing paragraph ~194-201) is rewritten to point at the approval
  dialog as the single source of sensitivity information.
- The "Trust model: profiles are trusted configuration" section gains a
  note that user-vs-core contributions are now distinguished by the
  approval system; the "only run profiles you trust" guidance still
  applies to the profile's own command/files/packages.

## 9. Testing

### Unit tests

- `internal/profile/merge_test.go`: assert provenance on merged results
  for each gated field; cover user-wins-when-shadowing, null-to-delete,
  and the coarse `Services` provenance (one contributor per service).
- `internal/profile/extends_test.go`: assert provenance across a
  multi-hop extends chain.
- `internal/approval/filter_test.go`: table-driven cases for the filter:
  - no sensitive fields → no prompt.
  - all-user sensitive fields → no prompt.
  - mixed user/core → only core keys in prompt items.
  - hash unchanged, all choices stored → no prompt.
  - hash changed, key dropped from profile → key dropped from state **and
    state persisted on this filter run** (reconciliation writes back even
    when no prompt follows).
  - hash changed, key retained → prior choice preserved.
  - denied key dropped from filtered profile.
  - network scalar handling.
  - **coarse services:** approve keeps the whole service; deny drops the
    service and cascades every top-level `service: <name>` mount off;
    the filtered profile passes `validateMountServices`; the daemon's
    definition is never modified by the decision.
  - **partial prompt result fails closed:** a `Prompt` returning a map
    missing an item's choice → exit 2, no `Save`.
- `internal/approval/hash_test.go`: hash stability (same input → same
  hash), hash changes on core-field edit, hash unchanged on user-only or
  non-sensitive edit, template literal preserved, hash changes when the
  contributor identity changes (same value, different Namespace), hash
  changes when a service's sensitive definition changes (privileged flip,
  new expose socket, new service mount/env key).
- `internal/approval/store_test.go`: round-trip State through YAML; empty
  file vs missing file; atomic save (temp + rename); **per-component
  validation of FullName** rejects `..`, empty segments, and unsafe chars
  (remote-namespace future-proofing).
- `pkg/tpd/launch_test.go`: end-to-end with a fake Store and fake Prompt:
  - interactive approve → launch proceeds with approved fields.
  - interactive deny → launch proceeds without denied fields.
  - non-interactive without flags and unapproved items → exit 2.
  - `--yes` persists and proceeds.
  - `--no` persists and proceeds without denied fields.
  - **`--dry-run --yes`/`--no`** applies the choice via the ephemeral
    store (denied fields removed from the printed spec, no file written).
  - **`--dry-run` without `--yes`/`--no`** and unapproved items → exit 2.
  - partial prompt → exit 2, no `Save`.
  - coarse-service cascade end-to-end (deny service → socket mounts off).

### CLI tests

- `cmd/tpd/cli_test.go`: `--yes`/`--no` mutually exclusive; both passed
  to `LaunchOpts`; help text.

### No e2e TUI test

The huh dialog itself is not exercised by an automated test (it needs a
TTY). The fake `Prompt` in `pkg/tpd/launch_test.go` stands in for it.
Manual smoke test: `tpd <sensitive-profile>` on a fresh state dir.

## 10. Out of scope

- A `tpd approval list` / `tpd approval reset` command (future; the state
  files are human-editable in the meantime).
- Gating `files`, `repos`, `packages`, `tools`, `image`, `command`,
  `labels`, `resources`, `caches`.
- Per-mount risk grading (the existing review already declined it for
  advisories; same reasoning applies here).
- Remote-namespace support beyond leaving the door open in the
  provenance model (Namespace != "" and != "core" is treated the same as
  core for gating).
- A TUI test harness (fake Prompt covers the contract).

## 11. Resolved design questions

- **`MergeProfiles` signature:** no change needed. `child.FullName()` is
  already available inside the merge; parent keys keep their previously
  recorded provenance. See §2 "No MergeProfiles signature change."
- **Hash granularity:** pre-template-expansion. The hash and store key
  on the profile-declared (literal template) form, so a changed host env
  var does not invalidate approvals. The dialog shows pre-expansion
  values in v1 (display-time expansion is out of scope). See §4 "Hash"
  and §6 "Rendering."
- **State file key:** the resolved catalog `FullName`, not the display
  name. See §4 "State file."

No open questions remain at design time.