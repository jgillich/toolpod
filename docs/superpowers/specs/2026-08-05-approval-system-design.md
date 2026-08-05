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

`mounts`, `devices`, `environment`, `ports`, `dbus`, `network`.

`services:` are flattened: each service's sensitive sub-fields roll up
into the same approval set, addressed as `services.<name>.<field>`. The
service sub-fields that are gated are the same six as the top level
(`Mounts`, `Devices`, `Env`, `Ports`, `Dbus`, `Network`) **plus two
service-only fields**:

- `services.<name>.privileged` — the single most consequential service
  capability. The built-in `podman` sidecar is `privileged: true`
  (`internal/catalog/fragments/services/podman.yaml`); a privileged
  rootless sidecar gains `CAP_SYS_ADMIN` plus all devices inside its user
  namespace. Gating `privileged` forces an explicit approval before that
  capability is granted.
- `services.<name>.exposes` — the host-adjacent sockets a service
  publishes and the main container binds. A service that exposes a socket
  is a service that grants the main container access to something the
  service owns (e.g. a nested Podman engine socket). Gating `exposes`
  makes that grant visible at approval time.

Both are gated as scalars/maps under `services.<name>.*` and rendered in
the dialog alongside the service's other fields.

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
// Provenance records, for each sensitive key, the FullName of the catalog
// entry that last wrote it. Keys whose final value came from a user entry
// (Namespace == "") are not gated; keys from a core/remote entry are.
type Provenance struct {
    Mounts   map[string]string         // mount target   -> contributor FullName
    Devices  map[string]string         // device target  -> contributor FullName
    Env      map[string]string         // env var        -> contributor FullName
    Ports    map[string]string         // container port -> contributor FullName
    Dbus     DbusProvenance            // talk/own sub-maps keyed by bus name
    Network  string                    // "" if unset or user-owned; FullName if core-contributed
    Services map[string]ServiceProvenance // service name -> per-field provenance
}

type DbusProvenance struct {
    Talk map[string]string // bus name -> contributor FullName
    Own  map[string]string // bus name -> contributor FullName
}

// ServiceProvenance is collapsed to a single string: services merge
// shallowly and cannot extends, so a whole service always has exactly one
// contributor. See §2 "Services".
type ServiceProvenance = string
```

`Network` is a scalar (single slot, last-writer-wins like `image`), so its
provenance is a single string, not a map.

### Merge semantics

`MergeProfiles` already follows *child wins per key* for maps and *child
wins* for scalars. Provenance follows the same rule: when `mergeMap` /
`mergeStringMap` / `mergeMounts` / etc. copies a key from `child` into
`out`, the provenance map for that field records `child.FullName()` for
that key. Keys retained only from `parent` keep the parent's recorded
provenance. `null`-to-delete removes the key from both the value map and
the provenance map.

This requires `MergeProfiles` to receive the contributors' `FullName`s.
`resolveChain` already has the `RawProfile` for each link; it passes
`parent.FullName()` and `rc.FullName()` into the merge, and the merge
writes them into the parallel `Provenance` on the output `RawProfile`.

### No MergeProfiles signature change

`resolveChain` (merge.go:83-102) calls `MergeProfiles(merged, parent)`
then `MergeProfiles(merged, rc)`. In both calls the `child` argument is a
`RawProfile` with valid `Namespace`/`Name`, so `child.FullName()` is
available inside the merge without threading new parameters. Parent keys
already have their provenance recorded from prior merges (the accumulated
`merged` carries the provenance built up across the chain), so the merge
only needs to stamp `child.FullName()` onto keys copied from `child` and
leave parent-contributed provenance untouched. No signature change to
`MergeProfiles`.

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
helper allocates a fresh provenance map and copies entries from `parent`'s
provenance for retained keys, then writes `child.FullName()` for keys
copied from `child`. Provenance maps are never mutated in place on
`parent`, or the shallow copy would alias and corrupt the parent's view.

### User-wins-when-shadowing

Because provenance follows child-wins, a user profile that re-declares
`mounts: { ~/.ssh: { source: ... } }` over a built-in `ssh` fragment's
`~/.ssh` key stamps that key's provenance as the **user** FullName (`""`
namespace). The approval filter skips any key whose provenance is a user
entry. This falls out of the merge rule for free and matches the trust
model (the user explicitly opted into this value).

### Services

Services merge shallowly via `mergeMap(parent.Services, ...)`
(merge.go:158) and a service may not `extends` (validate.go:309), so a
whole service value always comes from exactly one contributor — per-field
provenance inside a service can never diverge. `Provenance.Services` is
therefore collapsed to `map[string]string` (service name → contributor
FullName). The approval filter treats a service as gated if its
contributor is non-user, and gates the service's sensitive sub-fields
(`Mounts`, `Devices`, `Env`, `Ports`, `Dbus`, `Network`, `Privileged`,
`Exposes` — see §1) as a unit under that one contributor.

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
// fields. Returned by ResolveProfileWithProv. ResolveProfile is kept as a
// thin wrapper that discards provenance, for callers that don't gate
// (tpd show --resolved, etc.).
type Resolved struct {
    Profile
    Prov Provenance
}

func ResolveProfileWithProv(cat Catalog, name string) (Resolved, error)
```

`ResolveProfile` stays for `tpd show --resolved` and other non-launch
callers; it calls `ResolveProfileWithProv` and returns just the `Profile`.
`ResolveFragment` gets the same treatment
(`ResolveFragmentWithProv`) for symmetry, though fragments aren't launched.

### Approval filter

New `internal/approval` package with the gate logic (no UI here — that's
injected). The filter takes a `Resolved` and produces the **gated
`Profile`** (denied keys dropped) plus a **prompt request** describing
what still needs a decision:

```go
// internal/approval/filter.go

// SensitiveItem is one gated key the user must decide on.
type SensitiveItem struct {
    Field    string // "mounts", "devices", "env", "ports",
                    // "dbus.talk", "dbus.own", "network",
                    // "services.<name>.mounts", ...
    Key      string // the map key ("" for scalar network)
    Value    string // human-readable rendering (template-unexpanded)
    Source   string // contributor FullName, e.g. "core/creds/ssh"
}

// PromptRequest is what the dialog renders. Empty Items = no prompt needed.
type PromptRequest struct {
    ProfileName  string
    Hash         string
    Items        []SensitiveItem
    // PriorChoices carries the stored allowed-set keyed by Field/Key, so the
    // dialog can pre-check toggles that were approved before the hash changed.
    PriorChoices map[string]map[string]bool // field -> set of allowed keys
}

// Filter returns the profile with denied/dropped fields removed and a
// PromptRequest describing any still-unapproved sensitive fields. If
// PromptRequest.Items is empty, no prompt is needed and the filtered
// profile is ready to launch.
func Filter(res profile.Resolved, store Store) (profile.Profile, PromptRequest, error)
```

### Filter algorithm

1. Walk `Prov` and `Profile` together over the gated fields.
2. Compute the hash (§4) from the non-user sensitive fields of the
   resolved profile (pre-filter).
3. Load the stored `State` for the resolved `FullName` (not the display
   name — see §4).
4. If the stored hash == current hash: reconcile stored choices against
   the current non-user sensitive key set. Keys present in the stored set
   but **missing from the new key set** are dropped from the in-memory
   state used by this filter run. The on-disk file is only rewritten on
   the next `Save` (which happens when a prompt occurs and choices are
   persisted); until then, stale keys linger in the file harmlessly. Keys
   still present keep their prior allowed/denied choice and feed
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
7. Return the filtered `Profile` and the `PromptRequest`.

### Dialog return shape

The dialog returns the final `map[field]set[key]bool` of allowed keys for
this hash (the union of prior choices and new toggles, with new toggles
overriding on conflict). `--yes` sets every item's key to true; `--no`
sets every item's key to false. `Launch` then:

1. Writes the combined choices to the store (`store.Save`) under the
   current hash.
2. Re-runs `Filter` with the now-populated store. Because every item now
   has a stored choice for the current hash, `PromptRequest.Items` is
   empty and `Filter` returns the filtered profile directly.

This is a single re-filter call, not a double pass: the first `Filter`
discovers what needs a decision; the dialog resolves it; the second
`Filter` applies it. The filtered `Profile` from the second call is what
flows into `buildSpec`.

### Empty prompt short-circuit

If `PromptRequest.Items` is empty after reconciliation (every non-user
sensitive key already has a stored choice for the current hash), no prompt
is shown. This is your "If empty, permission prompt is skipped" rule. It
also covers the case where the profile has no non-user sensitive fields
at all.

### Where Filter is called

Inside `pkg/tpd.LaunchWithWriter`, immediately after
`ResolveProfileWithProv` and **after `ResolveTildes`**, so the dialog can
render template-expanded values (e.g. the actual
`/run/user/1000/wayland-0` instead of the literal
`{{ .Env.XDG_RUNTIME_DIR }}/{{ .Env.WAYLAND_DISPLAY }}` — the trust
decision is meaningless against an opaque template string). The filtered
`Profile` flows into `buildSpec`; the original `Resolved` is discarded.
The DryRun path uses the same filter so `--dry-run` reflects the approved
set.

`ResolveTildes` currently runs inside `buildSpec` (spec.go:30) after port
allocation. To make expanded values available to the filter, the launch
flow is reordered: port allocation and `ResolveTildes` move out of
`buildSpec` into `LaunchWithWriter`, before the filter. `buildSpec`
receives an already-expanded `Profile`. This is a refactor of existing
code, not new logic — `ResolveTildes` is already a pure function
(`internal/profile/paths.go:69`).

## 4. Hash, state file, and Store interface

### Hash

New `internal/approval/hash.go` (parallel to
`internal/profile/hash.go`'s `computeServiceHash`). The hash covers
**non-user sensitive fields only**, post-template-expansion, of the
resolved profile (the same expanded form the filter and dialog see — see
§3 "Where Filter is called"):

```go
func ComputeApprovalHash(res profile.Resolved) string
```

- Walk each gated field; for each key whose `Provenance` is a non-user
  entry, emit `<field>\n<key>\n<value-canonical>\n` into a sha256 hasher,
  sorted by (field, key).
- `value-canonical` is the explicit-field write form used by
  `computeServiceHash` (e.g. for a Mount: `mount %s %s %s %s %v %v %v\n`
  with target, source, service, socket, readOnly, optional, create).
  Values are **expanded** — the key and canonical value are the
  post-`ResolveTildes` form, so a mount whose template expanded to
  `/run/user/1000/wayland-0` hashes under that key, not under the literal
  template. This keeps the hash, the dialog display, and the approval
  keys all aligned on the same expanded identity.
- `network` is a scalar: emit `network\n<value>\n` if non-user-contributed
  and non-empty.
- Services: emit `services.<name>.<field>\n<key>\n<value>\n` per sensitive
  sub-field (including `privileged` and `exposes` — see §1).
- Return `hex.EncodeToString(sum[:])[:12]` (12 chars, same as service
  hash).

A user-only change to a non-sensitive field never touches this hash. A
user re-declaration of a core key changes that key's provenance to user,
removing it from the hash input → hash changes → reprompt, but the new
item set has fewer items (that key is no longer gated). Prior choices for
that key are dropped via the reconciliation rule.

**Trade-off accepted:** the hash now depends on the host environment
(e.g. `$DISPLAY`, `$XDG_RUNTIME_DIR`). Two machines with different
runtime dirs will hash the same profile differently and reprompt
independently. This is correct: the expanded path *is* the thing being
approved, and `/run/user/1000/wayland-0` is a different trust decision
than `/run/user/1001/wayland-0`. The state file is per-machine anyway
(it lives under `~/.local/share`).

> **Note (design change from initial Q&A):** the brainstorming Q&A
> originally chose pre-template hashing. Post-review, this was flipped
> to post-template because the approval dialog must show the actual host
> path being approved (issue 8: "the trust decision is made on an opaque
> string"). The hash, the dialog display, and the approval keys must all
> align on the same expanded identity, so the hash moved to
> post-expansion with them.

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
(`lang/go` → `approvals/lang/go.yaml`); `..` is already rejected by
`ValidateName`, so no path-escape risk.

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
  services:
    podman:
      privileged: true
      exposes: [registry]
      mounts: [/var/run/podman.sock]
      env:            # absent key = sub-field present, all denied
```

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
3. Nests `services.<name>.<field>` under a `services:` map in the YAML
   (the Go struct is flat — `Approved["services.podman.mounts"]` — but the
   on-disk shape is nested for human readability, as the example shows).

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
                     // dbus.talk, dbus.own, services.<name>.<field>
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

## 5. Launch integration

### LaunchOpts additions

```go
type LaunchOpts struct {
    // ... existing fields ...

    // ApprovalStore persists per-profile approval choices. If nil, a
    // default file-system store rooted at ~/.local/share/tpd/approvals/
    // is used. Injectable for tests.
    ApprovalStore approval.Store

    // ApprovalPrompt renders the interactive approval dialog and returns
    // the user's choices as a map[field]set[key]bool. If nil, a default
    // huh-based TUI is used. If the launch is non-interactive (not a TTY)
    // and a prompt is required, Launch returns an error unless --yes or
    // --no was passed.
    ApprovalPrompt approval.Prompt

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

The CLI wires the real huh-based prompt (§6); tests wire a fake.

### LaunchWithWriter flow

### LaunchWithWriter flow

```
1. LoadProfiles
2. ResolveProfileWithProv  -> Resolved{Profile, Prov}
3. allocate ports; ResolveTildes(expandedProfile, ...) -> expanded Resolved
4. filteredProfile, promptReq, err := approval.Filter(expandedResolved, store)
5. if promptReq.Items empty:
       proceed to buildSpec with filteredProfile
   else if AssumeYes:
       choices = every item's key = true, merged with prior
       if !DryRun: store.Save(choices)
       filteredProfile = re-filter
   else if AssumeNo:
       choices = every item's key = false, merged with prior
       if !DryRun: store.Save(choices)
       filteredProfile = re-filter
   else if DryRun || !isTTY(stdin):
       return Result{ExitCode: 2, Err: "unapproved sensitive fields require --yes or --no: <list>"}
   else:
       choices := ApprovalPrompt(promptReq, stdin, stdout)
       if aborted: return Result{ExitCode: 2, Err: "approval declined"}
       store.Save(choices); filteredProfile = re-filter
6. buildSpec(filteredProfile, ...)
7. ... existing Prepare/Run pipeline ...
```

"re-filter" in step 5 means a second `approval.Filter(expandedResolved, store)`
call. Because the store now has a choice for every item under the current
hash, the second call returns `promptReq.Items` empty and the filtered
profile ready for `buildSpec`. This mirrors the dialog-return shape in
§3.

The `--yes` and `--no` flags persist their choices to the state file, so
subsequent non-interactive launches reuse them. A later launch with a
changed hash re-prompts (or re-errors in non-interactive mode).

### DryRun

`--dry-run` is a read-only inspection command: it must not write approval
state or block on input. If a prompt would be required (there are
unapproved sensitive items and neither `--yes` nor `--no` was passed),
DryRun errors with exit code 2 and the same "requires --yes or --no"
message as non-interactive mode — it does not invoke the dialog and does
not persist state. With `--yes` or `--no`, DryRun applies the choice
in-memory only (no `store.Save`) and prints the resulting spec. This
keeps `--dry-run` side-effect-free while still showing what would launch.

## 6. Dialog (huh-based)

### Library

The interactive dialog uses [charmbracelet/huh](https://github.com/charmbracelet/huh)
v1.0.0, already a dependency and already used by `internal/scaffold/scaffold.go`
(`huh.NewMultiSelect`, `huh.NewConfirm`).

### Rendering

The dialog groups sensitive items by contributor fragment, then by field.
Each item is a row in a `huh.NewMultiSelect[string]` (or several, if the
item set is large — huh handles paging). Toggle on/off with space; Enter
confirms. Default state: **all off** (per your spec), unless a key was
previously approved and the hash changed (then `PriorChoices` pre-checks
it).

Values are rendered **post-template-expansion** because the filter runs
after `ResolveTildes` (§3). The user sees the actual host path that will
mount, not the opaque `{{ .Env.XDG_RUNTIME_DIR }}/{{ .Env.WAYLAND_DISPLAY }}`
template. If expansion produced an empty string (an optional mount whose
host variable is unset), the item is still shown but marked `(unresolved —
host variable not set)` so the user knows the mount will be skipped at
launch.

```
tpd: myagent wants the following from core/creds/ssh
  [x] ~/.ssh                    (mount, read-only)
  [ ] ~/.ssh/known_hosts        (mount, read-write)
  [ ] DOCKER_HOST=unix:///var/run/docker.sock   (env, from core/services/docker-host)

tpd: myagent wants the following from core/gui
  [ ] /dev/dri                  (device)
  [ ] /tmp/.X11-unix            (mount, optional)
  [ ] /run/user/1000/wayland-0  (mount, optional)

tpd: myagent wants the following from core/services/podman
  [ ] privileged: true          (service capability)
  [ ] /var/run/podman.sock      (service expose: registry)

  <Approve>  <Abort>
```

(Exact rendering TBD at implementation; the contract is: per-item toggle,
default off, prior-approved keys pre-checked, expanded values shown, an
explicit abort path.)

### Abort

If the user aborts the dialog (Esc / Ctrl+C), Launch returns
`Result{ExitCode: 2, Err: "approval declined"}`. No state is written.

### Non-interactive

`approval.Prompt` is only invoked when `isTTY(stdin)`. If stdin is not a
TTY and `AssumeYes`/`AssumeNo` are both false and `promptReq.Items` is
non-empty, Launch fails with exit code 2 and a message listing the
unapproved fields and the `--yes`/`--no` hint. This is your
"non-interactive shell with missing approvals is an error" rule.

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

`--yes` and `--no` only decide **currently-unapproved items** (keys in
`promptReq.Items` with no stored choice for the current hash). A key that
was previously **denied** (stored, hash matches, key absent from the
approved list) stays denied — `--yes` does not override prior denials, and
`--no` does not re-deny keys that are already approved. This makes the
flags idempotent and script-safe: a pipeline running `tpd --yes <profile>`
does not silently re-approve a key the user explicitly denied on an
earlier interactive launch. To un-deny a previously-denied key, the user
edits the state file (or deletes it to reset). A future
`tpd approval reset` command is out of scope here.

## 8. Advisory removal

Delete:

- `internal/catalog/advisories.go` (the whole file).
- The `catalog.Advisory(...)` call sites in:
  - `cmd/tpd/cli.go` (`runShow`, `runEdit` — the `if msg :=
    catalog.Advisory(advisoryName(key)); msg != "" { ... }` blocks).
  - `internal/scaffold/scaffold.go` (any advisory prints during `init`).
- The `advisoryName` helper in `cmd/tpd/cli.go`.
- Any tests for `catalog.Advisory` in `internal/catalog/catalog_test.go`.

Update `docs/2026-08-03-security-model.md`:

- The "Credential-fragment advisories" section is rewritten to point at
  the approval dialog as the single source of sensitivity information.
- The "Trust model: profiles are trusted configuration" section gains a
  note that user-vs-core contributions are now distinguished by the
  approval system; the "only run profiles you trust" guidance still
  applies to the profile's own command/files/packages.

## 9. Testing

### Unit tests

- `internal/profile/merge_test.go`: assert provenance on merged results
  for each gated field; cover user-wins-when-shadowing, null-to-delete,
  and the services flatten.
- `internal/profile/extends_test.go`: assert provenance across a
  multi-hop extends chain.
- `internal/approval/filter_test.go`: table-driven cases for the filter:
  - no sensitive fields → no prompt.
  - all-user sensitive fields → no prompt.
  - mixed user/core → only core keys in prompt items.
  - hash unchanged, all choices stored → no prompt.
  - hash changed, key dropped from profile → key dropped from state.
  - hash changed, key retained → prior choice preserved.
  - denied key dropped from filtered profile.
  - network scalar handling.
  - services flatten.
- `internal/approval/hash_test.go`: hash stability (same input → same
  hash), hash changes on core-field edit, hash unchanged on user-only or
  non-sensitive edit, template literal preserved.
- `internal/approval/store_test.go`: round-trip State through YAML; empty
  file vs missing file; atomic save (temp + rename).
- `pkg/tpd/launch_test.go`: end-to-end with a fake Store and fake Prompt:
  - interactive approve → launch proceeds with approved fields.
  - interactive deny → launch proceeds without denied fields.
  - non-interactive without flags and unapproved items → exit 2.
  - `--yes` persists and proceeds.
  - `--no` persists and proceeds without denied fields.
  - DryRun filters consistently.

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
- **Hash granularity:** post-template-expansion, to align with the dialog
  display. See §4 "Hash" and the note there on the design change.
- **State file key:** the resolved catalog `FullName`, not the display
  name. See §4 "State file."

No open questions remain at design time.