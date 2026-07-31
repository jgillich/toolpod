# Ports + Devices + `.Ports` Templating Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `ports` and `devices` to the profile schema (container-port-keyed with optional `host`/`host_ip`/`protocol`; container-device-keyed with `source`/`permissions`/`cgroup`), auto-allocate host ports, and expose allocated ports to the existing `{{ }}` template machinery as `.Ports` so commands/env can launch on them.

**Architecture:** Three layers, each with a clean seam. (1) `internal/profile` owns the schema: new `PortBind`/`DeviceBind` structs, load-time validation, map merge with null-to-delete, and template rendering extended with a `.Ports` context. (2) `pkg/toolpod` owns spec assembly: `buildSpec` allocates auto ports (injectable allocator), builds sorted `PortSpec`/`DeviceSpec` slices, and renders templates with the port values. (3) `internal/runtime` translates to Docker Engine API fields (`ExposedPorts`/`PortBindings`/`Devices`/`DeviceCgroupRules`) and prints `listening on <proto>://<ip>:<port>` lines after start.

**Tech Stack:** Go 1.25, `gopkg.in/yaml.v3`, Docker Engine SDK (`github.com/docker/docker/api/types/container`, `nat`), `golang.org/x/sys/unix` (already transitive via `golang.org/x/term`).

## Global Constraints

- Schema (from spec `docs/superpowers/specs/2026-07-31-toolpod-ports-devices-spec.md`):
  - `ports` is `map[string]PortBind`, key = container port (YAML int or string, normalized to string, 1–65535). Template key is the bare container port, **no** protocol suffix.
  - `PortBind`: `host` (missing/`0`/`""` = random; string or int normalized; range 1–65535), `host_ip` (default `""` = all interfaces), `protocol` (`tcp` default, `udp`, `sctp`; `sctp` **requires** explicit `host` — validation error otherwise).
  - `devices` is `map[string]DeviceBind`, key = container device path. `source` default = key, `permissions` ∈ {`r`,`rw`,`rwm`} default `rwm`, `cgroup` bool default `false`.
  - `null` value deletes an inherited entry (null-to-delete, like `mounts`); whole-field `null` deletes the entire inherited map (`"*"` sentinel).
- Template rule: `environment` values and `command`/`args_if_none` args are templates **iff they start with `{{`**. Mounts/caches keep the existing contains-`{{` rule — do not change it.
- Runtime: `HostConfig.DeviceCgroupRules` must never emit the blanket `["c *:*"]`; scope to `c <major>:<minor> rwm` where derivable, else `c <major>:* rwm` per device **with a warning**.
- No new fields on built-in profiles (`shell`/`opencode`/`codex` untouched).
- `network: host` + non-empty `ports` → warning to stderr (not error).
- `PortSpecs` sorted by (container, protocol, host port); `DeviceSpecs` sorted by container path. Deterministic output.
- Spec types live in `internal/runtime/runtime.go`; `pkg/toolpod/types.go` re-exports aliases only (mirrors `MountSpec` pattern).
- Tests: `go test ./...` (Go 1.25; the dev shell uses mise — if `go` shim errors, run `mise use -g go@1.26.5` first). Gated integration tests: `testing.Short()` + `DOCKER_HOST` check.

---

### Task 1: Schema types and validation

**Files:**
- Modify: `internal/profile/types.go` (after `Mount` struct, line ~59)
- Modify: `internal/profile/validate.go`
- Test: `internal/profile/validate_test.go`

**Interfaces:**
- Produces: `PortBind{Host, HostIP, Protocol string}` (yaml tags `host,omitempty`, `host_ip,omitempty`, `protocol,omitempty`), `DeviceBind{Source, Permissions string; Cgroup bool}` (yaml tags `source,omitempty`, `permissions,omitempty`, `cgroup,omitempty`), `Profile.Ports map[string]PortBind` (`yaml:"ports,omitempty"`), `Profile.Devices map[string]DeviceBind` (`yaml:"devices,omitempty"`).

- [ ] **Step 1: Add the types**

In `internal/profile/types.go`, add after the `Mount` struct:

```go
// PortBind publishes a container port to the host. Empty Host means the
// host port is auto-allocated at launch.
type PortBind struct {
	Host     string `yaml:"host,omitempty"`
	HostIP   string `yaml:"host_ip,omitempty"`
	Protocol string `yaml:"protocol,omitempty"`
}

// DeviceBind attaches a host device node into the container.
type DeviceBind struct {
	Source      string `yaml:"source,omitempty"`
	Permissions string `yaml:"permissions,omitempty"`
	Cgroup      bool   `yaml:"cgroup,omitempty"`
}
```

Add to the `Profile` struct (after `Tools`):

```go
	Ports   map[string]PortBind   `yaml:"ports,omitempty"`
	Devices map[string]DeviceBind `yaml:"devices,omitempty"`
```

- [ ] **Step 2: Write the failing validation tests**

In `internal/profile/validate_test.go`, add:

```go
func TestValidatePorts(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		host    string
		proto   string
		wantErr bool
	}{
		{"valid auto", "8080", "", "", false},
		{"valid fixed", "80", "5173", "", false},
		{"valid zero host means auto", "8080", "0", "", false},
		{"valid udp", "53", "", "udp", false},
		{"valid sctp with host", "5000", "9000", "sctp", false},
		{"zero container port", "0", "", "", true},
		{"container port over range", "65536", "", "", true},
		{"non-numeric key", "abc", "", "", true},
		{"negative host", "8080", "-1", "", true},
		{"host over range", "8080", "70000", "", true},
		{"non-numeric host", "8080", "abc", "", true},
		{"bogus protocol", "8080", "", "icmp", true},
		{"sctp without host", "5000", "", "sctp", true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			rc := RawProfile{Profile: Profile{
				Version: 1, Image: "x", Command: []string{"sh"},
				Ports: map[string]PortBind{tt.key: {Host: tt.host, Protocol: tt.proto}},
			}}
			err := validate(rc)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateDevices(t *testing.T) {
	valid := RawProfile{Profile: Profile{
		Version: 1, Image: "x", Command: []string{"sh"},
		Devices: map[string]DeviceBind{"/dev/fuse": {}},
	}}
	if err := validate(valid); err != nil {
		t.Fatalf("default device should validate: %v", err)
	}
	bad := RawProfile{Profile: Profile{
		Version: 1, Image: "x", Command: []string{"sh"},
		Devices: map[string]DeviceBind{"/dev/foo": {Permissions: "rxw"}},
	}}
	if err := validate(bad); err == nil {
		t.Fatal("expected error for invalid permissions")
	}
}

func TestValidateIntKeysNormalizedToStrings(t *testing.T) {
	rc, err := parseRaw([]byte("version: 1\nimage: x\ncommand: [sh]\nports:\n  8080: {}\n"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rc.Ports["8080"]; !ok {
		t.Errorf("int YAML key 8080 should decode to string key \"8080\", got %v", rc.Ports)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/profile/ -run 'TestValidatePorts|TestValidateDevices|TestValidateIntKeys' -v`
Expected: FAIL (field `Ports` doesn't exist / tests don't compile).

- [ ] **Step 4: Implement validation**

In `internal/profile/validate.go`, add imports `fmt`, `os`, `strconv`, and at the end of `validate()` (after the image/build checks) call the new validators:

```go
	if err := validatePorts(rc); err != nil {
		return err
	}
	if err := validateDevices(rc); err != nil {
		return err
	}
	if rc.Network == "host" && len(rc.Ports) > 0 {
		fmt.Fprintln(os.Stderr, "warning: network: host makes ports redundant; ports are ignored by the engine")
	}
```

Add the helpers:

```go
func validatePorts(rc RawProfile) error {
	for key, bind := range rc.Ports {
		if err := checkPortNum(key, "container port", rc.Path); err != nil {
			return err
		}
		if bind.Host != "" && bind.Host != "0" {
			if err := checkPortNum(bind.Host, "host port for container port "+key, rc.Path); err != nil {
				return err
			}
		}
		proto := bind.Protocol
		if proto == "" {
			proto = "tcp"
		}
		switch proto {
		case "tcp", "udp", "sctp":
		default:
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("ports: container port %s: invalid protocol %q (want tcp, udp, or sctp)", key, bind.Protocol)}
		}
		if proto == "sctp" && (bind.Host == "" || bind.Host == "0") {
			return ProfileError{Path: rc.Path, Message: "ports: container port " + key + ": sctp requires an explicit host port (cannot auto-allocate)"}
		}
	}
	return nil
}

func validateDevices(rc RawProfile) error {
	for key, bind := range rc.Devices {
		switch bind.Permissions {
		case "", "r", "rw", "rwm":
		default:
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("devices: %s: invalid permissions %q (want r, rw, or rwm)", key, bind.Permissions)}
		}
	}
	return nil
}

func checkPortNum(s, what, path string) error {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return ProfileError{Path: path, Message: fmt.Sprintf("%s: invalid port %q (want 1-65535)", what, s)}
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/profile/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/profile/types.go internal/profile/validate.go internal/profile/validate_test.go
git commit -m "feat(profile): add ports and devices schema fields with validation"
```

---

### Task 2: Merge semantics (extends)

**Files:**
- Modify: `internal/profile/catalog.go:239-280` (`collectNullKeys`)
- Modify: `internal/profile/merge.go:148-152,163-178`
- Test: `internal/profile/merge_test.go`

**Interfaces:**
- Consumes: `PortBind`, `DeviceBind`, `Profile.Ports/Devices` from Task 1.
- Produces: `mergePortMap(parent, child map[string]PortBind, nullKeys map[string]bool) map[string]PortBind`, `mergeDeviceMap(parent, child map[string]DeviceBind, nullKeys map[string]bool) map[string]DeviceBind` — same semantics as `mergeMounts` (whole-field `"*"` → empty map, key-by-key override, null-key deletion).

- [ ] **Step 1: Write the failing tests**

In `internal/profile/merge_test.go`, add:

```go
func TestResolvePortsDevicesMergeAndNullDelete(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\n"+
		"ports:\n  8080: {host: 5173}\n  9000: {}\n"+
		"devices:\n  /dev/fuse: {}\n  /dev/nvidia0: {permissions: rw}\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: base\n"+
		"ports:\n  8080: {host: 0}\n  9000: null\n"+
		"devices:\n  /dev/fuse: null\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Ports["8080"].Host != "0" {
		t.Errorf("8080 host = %q, want \"0\" (overridden to random)", cfg.Ports["8080"].Host)
	}
	if _, exists := cfg.Ports["9000"]; exists {
		t.Error("9000 should be deleted by null-to-delete")
	}
	if _, exists := cfg.Devices["/dev/fuse"]; exists {
		t.Error("/dev/fuse should be deleted by null-to-delete")
	}
	if cfg.Devices["/dev/nvidia0"].Permissions != "rw" {
		t.Errorf("inherited /dev/nvidia0 permissions = %q, want rw", cfg.Devices["/dev/nvidia0"].Permissions)
	}
}

func TestResolvePortsWholeFieldNull(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\nports:\n  8080: {}\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: base\nports: null\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Ports) != 0 {
		t.Errorf("whole-field null should drop all inherited ports, got %v", cfg.Ports)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/profile/ -run TestResolvePortsDevices -v`
Expected: FAIL (ports dropped entirely by merge — field not merged).

- [ ] **Step 3: Implement merge**

In `internal/profile/merge.go`, add `"ports"` and `"devices"` to the `collectNullKeys` init map in `catalog.go`:

```go
	nulls := map[string]map[string]bool{
		"mounts":      {},
		"environment": {},
		"tools":       {},
		"caches":      {},
		"labels":      {},
		"ports":       {},
		"devices":     {},
	}
```

In `merge.go`, wire into `MergeProfiles` (after the `Labels` line):

```go
	out.Ports = mergePortMap(parent.Ports, child.Ports, child.NullKeys["ports"])
	out.Devices = mergeDeviceMap(parent.Devices, child.Devices, child.NullKeys["devices"])
```

Add after `mergeMounts`:

```go
func mergePortMap(parent, child map[string]PortBind, nullKeys map[string]bool) map[string]PortBind {
	if nullKeys != nil && nullKeys["*"] {
		return map[string]PortBind{}
	}
	out := make(map[string]PortBind, len(parent)+len(child))
	for k, v := range parent {
		out[k] = v
	}
	for k, v := range child {
		out[k] = v
	}
	for k := range nullKeys {
		delete(out, k)
	}
	return out
}

func mergeDeviceMap(parent, child map[string]DeviceBind, nullKeys map[string]bool) map[string]DeviceBind {
	if nullKeys != nil && nullKeys["*"] {
		return map[string]DeviceBind{}
	}
	out := make(map[string]DeviceBind, len(parent)+len(child))
	for k, v := range parent {
		out[k] = v
	}
	for k, v := range child {
		out[k] = v
	}
	for k := range nullKeys {
		delete(out, k)
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/profile/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/profile/catalog.go internal/profile/merge.go internal/profile/merge_test.go
git commit -m "feat(profile): merge ports and devices via null-to-delete map semantics"
```

---

### Task 3: Template integration (`.Ports` + env/command rendering)

**Files:**
- Modify: `internal/profile/paths.go`
- Modify: `pkg/toolpod/spec.go:14-15` (call site — pass `nil` for now, keeps build green)
- Test: `internal/profile/paths_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `ResolveTildes(cfg Profile, mode, hostHome, runtimeHome string, ports map[string]string) (Profile, error)` — `ports` is container-port → host-port; rendered as `.Ports` in templates. `tmplData` gains `Ports map[string]string`. Renders `environment` values and `command`/`args_if_none` args when they **start with `{{`**.

- [ ] **Step 1: Write the failing tests**

In `internal/profile/paths_test.go`, add:

```go
func TestResolveTildesPortsInEnvironment(t *testing.T) {
	cfg := Profile{
		Env: map[string]string{
			"PORT":  `{{ index .Ports "8080" }}`,
			"PLAIN": "8080",
			"EMPTY": "",
		},
	}
	out, err := ResolveTildes(cfg, "B", "/home/me", "/root", map[string]string{"8080": "39483"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Env["PORT"] != "39483" {
		t.Errorf("PORT = %q, want 39483", out.Env["PORT"])
	}
	if out.Env["PLAIN"] != "8080" {
		t.Errorf("PLAIN = %q, want 8080 (untouched)", out.Env["PLAIN"])
	}
	if out.Env["EMPTY"] != "" {
		t.Errorf("EMPTY = %q, want \"\" (passthrough preserved)", out.Env["EMPTY"])
	}
}

func TestResolveTildesPortsInCommand(t *testing.T) {
	cfg := Profile{
		Command:   []string{"opencode", "web", "--port", `{{ index .Ports "8080" }}`},
		ArgsIfNone: []string{`{{ index .Ports "8080" }}`},
	}
	out, err := ResolveTildes(cfg, "B", "/home/me", "/root", map[string]string{"8080": "39483"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Command[3] != "39483" {
		t.Errorf("command arg = %q, want 39483", out.Command[3])
	}
	if out.ArgsIfNone[0] != "39483" {
		t.Errorf("args_if_none = %q, want 39483", out.ArgsIfNone[0])
	}
}

func TestResolveTildesLiteralBracePassthrough(t *testing.T) {
	cfg := Profile{
		Command: []string{"sh", "-c", "echo {{x}} {{y}}"},
	}
	out, err := ResolveTildes(cfg, "B", "/home/me", "/root", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Command[2] != "echo {{x}} {{y}}" {
		t.Errorf("literal braces must pass through untouched, got %q", out.Command[2])
	}
}

func TestResolveTildesMissingPortKeyErrors(t *testing.T) {
	cfg := Profile{
		Env: map[string]string{"PORT": `{{ index .Ports "9999" }}`},
	}
	if _, err := ResolveTildes(cfg, "B", "/home/me", "/root", map[string]string{"8080": "39483"}); err == nil {
		t.Error("expected error when template references a port not in the map")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/profile/ -run TestResolveTildesPorts -v`
Expected: FAIL (signature mismatch / `.Ports` missing).

- [ ] **Step 3: Update the call site in spec.go and existing tests**

In `pkg/toolpod/spec.go:15`, change:

```go
	cfg, err := profile.ResolveTildes(cfg, mode, hostHome, runtimeHome)
```
to:
```go
	cfg, err := profile.ResolveTildes(cfg, mode, hostHome, runtimeHome, nil)
```

The signature change breaks the 8 existing `ResolveTildes` calls in
`internal/profile/paths_test.go` (lines 19, 44, 63, 81, 98, 114, 135, 156).
Update each from `ResolveTildes(cfg, "A", "/home/me", "/home/me")` (and the
mode-B variants) to append `, nil` as a fifth argument, e.g.:

```go
	out, err := ResolveTildes(cfg, "A", "/home/me", "/home/me", nil)
```

Do not change anything else in those tests — the new `.Ports` tests are
added in Step 1's test run and verified in Step 5.

- [ ] **Step 4: Implement template rendering**

In `internal/profile/paths.go`, change `tmplData` to:

```go
type tmplData struct {
	Env   map[string]string
	UID   string
	Ports map[string]string
}
```

Change `renderTemplate` to take the full data (drop the `env` parameter):

```go
func renderTemplate(s string, data tmplData) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	tmpl, err := template.New("path").Funcs(template.FuncMap{
		"trimPrefix": strings.TrimPrefix,
		"uid":        currentUID,
	}).Parse(s)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}
```

Change `expandTarget`/`expandSource` to take `data tmplData` instead of `env map[string]string` (bodies unchanged except the renderTemplate call). Rewrite `ResolveTildes`:

```go
func ResolveTildes(cfg Profile, mode, hostHome, runtimeHome string, ports map[string]string) (Profile, error) {
	out := cfg
	data := tmplData{Env: expandEnvMap(), UID: currentUID(), Ports: ports}

	if out.Mounts != nil {
		expanded := make(map[string]Mount, len(out.Mounts))
		for target, m := range out.Mounts {
			newTarget, err := expandTarget(target, runtimeHome, data)
			if err != nil {
				return out, err
			}
			m.Source, err = expandSource(m.Source, hostHome, data)
			if err != nil {
				return out, err
			}
			expanded[newTarget] = m
		}
		out.Mounts = expanded
	}

	if out.Caches != nil {
		expanded := make(map[string]string, len(out.Caches))
		for name, target := range out.Caches {
			var err error
			expanded[name], err = expandTarget(target, runtimeHome, data)
			if err != nil {
				return out, err
			}
		}
		out.Caches = expanded
	}

	if out.Env != nil {
		for k, v := range out.Env {
			if !strings.HasPrefix(v, "{{") {
				continue
			}
			rendered, err := renderTemplate(v, data)
			if err != nil {
				return out, fmt.Errorf("environment %s: %w", k, err)
			}
			out.Env[k] = rendered
		}
	}

	if out.Command != nil {
		rendered, err := renderArgs(out.Command, data)
		if err != nil {
			return out, fmt.Errorf("command: %w", err)
		}
		out.Command = rendered
	}
	if out.ArgsIfNone != nil {
		rendered, err := renderArgs(out.ArgsIfNone, data)
		if err != nil {
			return out, fmt.Errorf("args_if_none: %w", err)
		}
		out.ArgsIfNone = rendered
	}

	return out, nil
}

// renderArgs renders args that start with "{{" as templates; all other
// args pass through literally (so shell snippets with literal braces work).
func renderArgs(args []string, data tmplData) ([]string, error) {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.HasPrefix(a, "{{") {
			rendered, err := renderTemplate(a, data)
			if err != nil {
				return nil, fmt.Errorf("arg %d: %w", i, err)
			}
			out[i] = rendered
			continue
		}
		out[i] = a
	}
	return out, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/profile/ ./pkg/toolpod/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/profile/paths.go internal/profile/paths_test.go pkg/toolpod/spec.go
git commit -m "feat(profile): expose .Ports template values and render env/command templates"
```

---

### Task 4: Port allocation and spec assembly

**Files:**
- Modify: `internal/runtime/runtime.go`
- Modify: `pkg/toolpod/types.go`
- Modify: `pkg/toolpod/launch.go`
- Modify: `pkg/toolpod/spec.go`
- Test: `pkg/toolpod/spec_test.go`

**Interfaces:**
- Consumes: `ResolveTildes(..., ports map[string]string)` from Task 3; `PortBind`/`DeviceBind` from Task 1.
- Produces:
  - `runtime.PortSpec{HostIP, HostPort, Container, Protocol string}` and `runtime.DeviceSpec{Container, Host, Perms string; Cgroup bool}`; `runtime.Spec` gains `PortSpecs []PortSpec` and `DeviceSpecs []DeviceSpec`.
  - `pkg/toolpod` aliases `PortSpec = runtime.PortSpec`, `DeviceSpec = runtime.DeviceSpec`.
  - `PortAllocator func(protocol, hostIP string) (string, error)`; `LaunchOpts.PortAllocator` (default = ephemeral bind).
  - `buildPortSpecs(ports map[string]profile.PortBind, alloc PortAllocator) ([]PortSpec, map[string]string, error)` and `buildDeviceSpecs(devices map[string]profile.DeviceBind) []DeviceSpec` — sorted; `.Ports` value map is container-port → host-port.

- [ ] **Step 1: Write the failing tests**

In `pkg/toolpod/spec_test.go`, add:

```go
func fakePortAllocator(ports ...string) PortAllocator {
	i := 0
	return func(protocol, hostIP string) (string, error) {
		p := ports[i%len(ports)]
		i++
		return p, nil
	}
}

func TestBuildSpecPortsAllocationAndTemplates(t *testing.T) {
	cfg := profile.Profile{
		Version: 1,
		Image:   "img",
		Command: []string{"opencode", "web", "--port", `{{ index .Ports "8080" }}`},
		Env:     map[string]string{"WEB_PORT": `{{ index .Ports "8080" }}`},
		Ports: map[string]profile.PortBind{
			"8080": {},
			"5432": {Host: "5432"},
			"53":   {Protocol: "udp"},
			"9000": {Host: "9000", HostIP: "127.0.0.1"},
		},
	}
	opts := LaunchOpts{ProfileName: "web", Workspace: "/p", PortAllocator: fakePortAllocator("40001", "40002")}
	spec, err := buildSpec(opts, cfg, "B", "/home/me", "/root")
	if err != nil {
		t.Fatal(err)
	}
	wantPorts := []PortSpec{
		{Container: "53", HostPort: "40002", Protocol: "udp"},
		{Container: "5432", HostPort: "5432", Protocol: "tcp"},
		{Container: "8080", HostPort: "40001", Protocol: "tcp"},
		{Container: "9000", HostIP: "127.0.0.1", HostPort: "9000", Protocol: "tcp"},
	}
	if len(spec.PortSpecs) != len(wantPorts) {
		t.Fatalf("PortSpecs = %+v, want %+v", spec.PortSpecs, wantPorts)
	}
	for i, p := range spec.PortSpecs {
		if p != wantPorts[i] {
			t.Errorf("PortSpecs[%d] = %+v, want %+v", i, p, wantPorts[i])
		}
	}
	if spec.Command[3] != "40001" {
		t.Errorf("template command arg = %q, want 40001", spec.Command[3])
	}
	if spec.Env["WEB_PORT"] != "40001" {
		t.Errorf("template env = %q, want 40001", spec.Env["WEB_PORT"])
	}
}

func TestBuildSpecDevices(t *testing.T) {
	cfg := profile.Profile{
		Version: 1, Image: "img", Command: []string{"x"},
		Devices: map[string]profile.DeviceBind{
			"/dev/fuse":    {},
			"/dev/nvidia0": {Source: "/dev/nvidia0", Permissions: "rw"},
			"/dev/bus/usb": {Source: "/dev/bus/usb", Cgroup: true},
		},
	}
	opts := LaunchOpts{ProfileName: "x", Workspace: "/p"}
	spec, err := buildSpec(opts, cfg, "B", "/home/me", "/root")
	if err != nil {
		t.Fatal(err)
	}
	want := []DeviceSpec{
		{Container: "/dev/bus/usb", Host: "/dev/bus/usb", Perms: "rwm", Cgroup: true},
		{Container: "/dev/fuse", Host: "/dev/fuse", Perms: "rwm"},
		{Container: "/dev/nvidia0", Host: "/dev/nvidia0", Perms: "rw"},
	}
	if len(spec.DeviceSpecs) != len(want) {
		t.Fatalf("DeviceSpecs = %+v, want %+v", spec.DeviceSpecs, want)
	}
	for i, d := range spec.DeviceSpecs {
		if d != want[i] {
			t.Errorf("DeviceSpecs[%d] = %+v, want %+v", i, d, want[i])
		}
	}
}

func TestDefaultPortAllocatorDistinct(t *testing.T) {
	tcp1, err := defaultPortAllocator("tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	tcp2, err := defaultPortAllocator("tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	udp1, err := defaultPortAllocator("udp", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{tcp1: true, tcp2: true, udp1: true}
	if len(seen) != 3 {
		t.Errorf("allocated ports must be distinct: tcp=%s,%s udp=%s", tcp1, tcp2, udp1)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/toolpod/ -run TestBuildSpecPorts -v`
Expected: FAIL (types missing).

- [ ] **Step 3: Add spec types and aliases**

In `internal/runtime/runtime.go`, after `MountSpec`:

```go
type PortSpec struct {
	HostIP    string
	HostPort  string
	Container string
	Protocol  string
}

type DeviceSpec struct {
	Container string
	Host      string
	Perms     string
	Cgroup    bool
}
```

Add to `Spec` (after `Mounts`):

```go
	PortSpecs  []PortSpec
	DeviceSpecs []DeviceSpec
```

In `pkg/toolpod/types.go`, add to the type alias block:

```go
	PortSpec      = runtime.PortSpec
	DeviceSpec    = runtime.DeviceSpec
```

- [ ] **Step 4: Add the allocator**

In `pkg/toolpod/launch.go`, add imports `net`, `strconv`, and:

```go
// PortAllocator reserves an unused host port for a published binding.
// protocol is "tcp", "udp", or "sctp"; hostIP is the requested bind address
// ("" = all interfaces). Returns the allocated port as a string.
type PortAllocator func(protocol, hostIP string) (string, error)

func defaultPortAllocator(protocol, hostIP string) (string, error) {
	addr := net.JoinHostPort(hostIP, "0")
	switch protocol {
	case "udp":
		pc, err := net.ListenPacket("udp", addr)
		if err != nil {
			return "", err
		}
		defer pc.Close()
		return strconv.Itoa(pc.LocalAddr().(*net.UDPAddr).Port), nil
	default: // tcp (sctp auto-allocation is rejected at validation)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return "", err
		}
		defer ln.Close()
		return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port), nil
	}
}
```

In `pkg/toolpod/types.go`, add to `LaunchOpts`:

```go
	// PortAllocator reserves host ports for auto-allocated bindings. If
	// nil, an ephemeral socket bind is used. Injectable for deterministic
	// tests.
	PortAllocator PortAllocator
```

- [ ] **Step 5: Implement buildPortSpecs/buildDeviceSpecs in buildSpec**

In `pkg/toolpod/spec.go`, add imports `sort`, `strconv` is not needed; add before the `ResolveTildes` call:

```go
	alloc := opts.PortAllocator
	if alloc == nil {
		alloc = defaultPortAllocator
	}
	portSpecs, portValues, err := buildPortSpecs(cfg.Ports, alloc)
	if err != nil {
		return Spec{}, fmt.Errorf("allocate ports: %w", err)
	}
	deviceSpecs := buildDeviceSpecs(cfg.Devices)

	cfg, err = profile.ResolveTildes(cfg, mode, hostHome, runtimeHome, portValues)
```

(`portValues` is the `.Ports` map: container port → host port.)

Add the helpers at the bottom of `spec.go`:

```go
func buildPortSpecs(ports map[string]profile.PortBind, alloc PortAllocator) ([]PortSpec, map[string]string, error) {
	specs := make([]PortSpec, 0, len(ports))
	values := make(map[string]string, len(ports))
	for container, bind := range ports {
		proto := bind.Protocol
		if proto == "" {
			proto = "tcp"
		}
		hostPort := bind.Host
		if hostPort == "" || hostPort == "0" {
			allocated, err := alloc(proto, bind.HostIP)
			if err != nil {
				return nil, nil, fmt.Errorf("port %s: %w", container, err)
			}
			hostPort = allocated
		}
		values[container] = hostPort
		specs = append(specs, PortSpec{
			HostIP:    bind.HostIP,
			HostPort:  hostPort,
			Container: container,
			Protocol:  proto,
		})
	}
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].Container != specs[j].Container {
			return specs[i].Container < specs[j].Container
		}
		if specs[i].Protocol != specs[j].Protocol {
			return specs[i].Protocol < specs[j].Protocol
		}
		return specs[i].HostPort < specs[j].HostPort
	})
	return specs, values, nil
}

func buildDeviceSpecs(devices map[string]profile.DeviceBind) []DeviceSpec {
	specs := make([]DeviceSpec, 0, len(devices))
	for container, bind := range devices {
		source := bind.Source
		if source == "" {
			source = container
		}
		perms := bind.Permissions
		if perms == "" {
			perms = "rwm"
		}
		specs = append(specs, DeviceSpec{
			Container: container,
			Host:      source,
			Perms:     perms,
			Cgroup:    bind.Cgroup,
		})
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Container < specs[j].Container })
	return specs
}
```

In the returned `Spec{...}` literal, add:

```go
		PortSpecs:  portSpecs,
		DeviceSpecs: deviceSpecs,
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/runtime/ ./pkg/toolpod/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/runtime/runtime.go pkg/toolpod/types.go pkg/toolpod/launch.go pkg/toolpod/spec.go pkg/toolpod/spec_test.go
git commit -m "feat(toolpod): allocate host ports and assemble port/device specs"
```

---

### Task 5: Runtime translation (Docker Engine API)

**Files:**
- Modify: `internal/runtime/docker_run.go`
- Test: `internal/runtime/docker_test.go`

**Interfaces:**
- Consumes: `Spec.PortSpecs`/`Spec.DeviceSpecs` from Task 4.
- Produces (pure helpers, unit-testable):
  - `buildPortBindings(spec Spec) (nat.PortSet, nat.PortMap)` — exposed ports + host bindings.
  - `buildDevices(spec Spec) []container.DeviceMapping` — missing host source → stderr warning + skip.
  - `buildDeviceCgroupRules(spec Spec) []string` — `c <major>:<minor> rwm` when sysfs entry exists, else `c <major>:* rwm` + warning. Never `c *:*`.

- [ ] **Step 1: Write the failing tests**

In `internal/runtime/docker_test.go`, add:

```go
func TestBuildPortBindings(t *testing.T) {
	spec := Spec{PortSpecs: []PortSpec{
		{Container: "8080", HostPort: "40001", Protocol: "tcp"},
		{Container: "53", HostIP: "127.0.0.1", HostPort: "40002", Protocol: "udp"},
	}}
	exposed, bindings := buildPortBindings(spec)
	if _, ok := exposed["8080/tcp"]; !ok {
		t.Errorf("ExposedPorts missing 8080/tcp: %v", exposed)
	}
	if _, ok := exposed["53/udp"]; !ok {
		t.Errorf("ExposedPorts missing 53/udp: %v", exposed)
	}
	tcp := bindings["8080/tcp"]
	if len(tcp) != 1 || tcp[0].HostPort != "40001" || tcp[0].HostIP != "" {
		t.Errorf("bindings[8080/tcp] = %+v, want [{ 40001}]", tcp)
	}
	udp := bindings["53/udp"]
	if len(udp) != 1 || udp[0].HostIP != "127.0.0.1" {
		t.Errorf("bindings[53/udp] = %+v, want [{127.0.0.1 40002}]", udp)
	}
}

func TestBuildDevicesSkipsMissingSource(t *testing.T) {
	spec := Spec{DeviceSpecs: []DeviceSpec{
		{Container: "/dev/null", Host: "/dev/null", Perms: "rwm"},
		{Container: "/dev/nonexistent-xyz", Host: "/dev/nonexistent-xyz", Perms: "rwm"},
	}}
	devices := buildDevices(spec)
	if len(devices) != 1 {
		t.Fatalf("Devices = %+v, want only /dev/null (missing source skipped)", devices)
	}
	if devices[0].PathInContainer != "/dev/null" || devices[0].PathOnHost != "/dev/null" || devices[0].CgroupPermissions != "rwm" {
		t.Errorf("device mapping = %+v, want /dev/null -> /dev/null rwm", devices[0])
	}
}

func TestBuildDeviceCgroupRulesScoped(t *testing.T) {
	spec := Spec{DeviceSpecs: []DeviceSpec{
		{Container: "/dev/null", Host: "/dev/null", Perms: "rwm", Cgroup: true},
		{Container: "/dev/fuse", Host: "/dev/fuse", Cgroup: false},
	}}
	rules := buildDeviceCgroupRules(spec)
	if len(rules) != 1 {
		t.Fatalf("rules = %v, want exactly one (cgroup: false must not emit rules)", rules)
	}
	// /dev/null is char major 1; either the scoped 1:<minor> form or the
	// 1:* fallback must be used — never a blanket rule.
	if !strings.HasPrefix(rules[0], "c 1:") || !strings.HasSuffix(rules[0], " rwm") {
		t.Errorf("rule = %q, want \"c 1:<minor> rwm\" or \"c 1:* rwm\"", rules[0])
	}
	if strings.Contains(rules[0], "*:*") {
		t.Errorf("blanket c *:* rule must never be emitted, got %q", rules[0])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/ -run TestBuildPortBindings -v`
Expected: FAIL (helpers undefined).

- [ ] **Step 3: Implement the helpers**

`nat` (`github.com/docker/go-connections/nat` v0.6.0) is currently an
indirect dependency — importing it directly means promoting it in `go.mod`.
Run `go get github.com/docker/go-connections@v0.6.0` (or `go mod tidy`
after writing the imports) before the test pass in Step 6.

In `internal/runtime/docker_run.go`, add imports `github.com/docker/go-connections/nat`, `golang.org/x/sys/unix`. Add after `buildMounts`:

```go
func buildPortBindings(spec Spec) (nat.PortSet, nat.PortMap) {
	exposed := nat.PortSet{}
	bindings := nat.PortMap{}
	for _, p := range spec.PortSpecs {
		port := nat.Port(p.Container + "/" + p.Protocol)
		exposed[port] = struct{}{}
		bindings[port] = []nat.PortBinding{{HostIP: p.HostIP, HostPort: p.HostPort}}
	}
	return exposed, bindings
}

func buildDevices(spec Spec) []container.DeviceMapping {
	var out []container.DeviceMapping
	for _, d := range spec.DeviceSpecs {
		if _, err := os.Stat(d.Host); err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping device %s: %s not found\n", d.Container, d.Host)
			continue
		}
		out = append(out, container.DeviceMapping{
			PathOnHost:        d.Host,
			PathInContainer:   d.Container,
			CgroupPermissions: d.Perms,
		})
	}
	return out
}

func buildDeviceCgroupRules(spec Spec) []string {
	var out []string
	for _, d := range spec.DeviceSpecs {
		if !d.Cgroup {
			continue
		}
		major, minor, ok := deviceMajorMinor(d.Host)
		if !ok {
			continue
		}
		rule := fmt.Sprintf("c %d:%d rwm", major, minor)
		if _, err := os.Lstat(fmt.Sprintf("/sys/dev/char/%d:%d", major, minor)); err != nil {
			if _, err2 := os.Lstat(fmt.Sprintf("/sys/dev/block/%d:%d", major, minor)); err2 != nil {
				rule = fmt.Sprintf("c %d:* rwm", major)
				fmt.Fprintf(os.Stderr, "warning: device %s: no sysfs entry for %d:%d, using broad rule %s\n", d.Container, major, minor, rule)
			}
		}
		out = append(out, rule)
	}
	return out
}

func deviceMajorMinor(path string) (int, int, bool) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, 0, false
	}
	return int(unix.Major(uint64(st.Rdev))), int(unix.Minor(uint64(st.Rdev))), true
}
```

- [ ] **Step 4: Wire into Run + post-start output**

In `Run`, before `ContainerCreate`, after `envList`/`containerName`:

```go
	exposedPorts, portBindings := buildPortBindings(spec)
	devices := buildDevices(spec)
	cgroupRules := buildDeviceCgroupRules(spec)
```

Add to the `container.Config` literal:

```go
		ExposedPorts: exposedPorts,
```

Add to the `container.HostConfig` literal:

```go
		PortBindings:       portBindings,
		Devices:            devices,
		DeviceCgroupRules:  cgroupRules,
```

After `ContainerStart` succeeds (after the `if err := d.cli.ContainerStart(...)` block, before the pump goroutine), add:

```go
	for _, p := range spec.PortSpecs {
		ip := p.HostIP
		if ip == "" || ip == "0.0.0.0" {
			ip = "127.0.0.1"
		}
		fmt.Fprintf(os.Stderr, "listening on %s://%s:%s\n", p.Protocol, ip, p.HostPort)
	}
```

- [ ] **Step 5: Add gated integration test**

In `internal/runtime/docker_test.go`, add:

```go
func TestIntegrationRunPublishesPort(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if os.Getenv("DOCKER_HOST") == "" {
		t.Skip("DOCKER_HOST not set")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hostPort := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	ln.Close()

	rt, err := NewDockerRuntime()
	if err != nil {
		t.Fatalf("NewDockerRuntime: %v", err)
	}
	spec := Spec{
		ProfileName: "test-port",
		Image:       "alpine:latest",
		Command:     []string{"sh", "-c", "echo hi | nc -l -p 8080"},
		Workspace:   WorkspaceSpec{HostPath: "/tmp", Target: "/workspace", Mode: "B"},
		Network:     "none",
		PortSpecs:   []PortSpec{{Container: "8080", HostPort: hostPort, Protocol: "tcp"}},
	}
	if _, err := rt.Prepare(context.Background(), spec, NoopProgressWriter{}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := rt.Run(context.Background(), spec)
		done <- err
	}()
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+hostPort, 10*time.Second)
	if err != nil {
		t.Fatalf("dial published port: %v", err)
	}
	defer conn.Close()
	buf := make([]byte, 16)
	n, _ := conn.Read(buf)
	if string(buf[:n]) != "hi" {
		t.Errorf("got %q from published port, want \"hi\"", string(buf[:n]))
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}
```

Add imports `net`, `strconv`, `time` to `docker_test.go`.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/runtime/`
Expected: PASS (integration test skips without DOCKER_HOST).

- [ ] **Step 7: Commit**

```bash
git add internal/runtime/docker_run.go internal/runtime/docker_test.go
git commit -m "feat(runtime): map ports/devices to engine API and print listening URLs"
```

---

### Task 6: RenderSpec output and README

**Files:**
- Modify: `pkg/toolpod/dryrun.go`
- Create: `pkg/toolpod/dryrun_test.go`
- Modify: `README.md`
- Test: `pkg/toolpod/dryrun_test.go`

**Interfaces:**
- Consumes: `Spec.PortSpecs`/`Spec.DeviceSpecs` from Task 4.

- [ ] **Step 1: Write the failing tests**

Create `pkg/toolpod/dryrun_test.go`:

```go
package toolpod

import (
	"strings"
	"testing"
)

func TestRenderSpecPortsAndDevices(t *testing.T) {
	spec := Spec{
		ProfileName: "web",
		Image:       "img",
		Command:     []string{"x"},
		Workspace:   WorkspaceSpec{HostPath: "/p", Target: "/workspace", Mode: "B"},
		PortSpecs: []PortSpec{
			{Container: "8080", HostPort: "40001", Protocol: "tcp"},
			{Container: "53", HostIP: "127.0.0.1", HostPort: "40002", Protocol: "udp"},
		},
		DeviceSpecs: []DeviceSpec{
			{Container: "/dev/fuse", Host: "/dev/fuse", Perms: "rwm"},
		},
	}
	var out strings.Builder
	if err := RenderSpec(&out, spec); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	for _, want := range []string{
		"ports:",
		"  8080/tcp -> :40001",
		"  53/udp -> 127.0.0.1:40002",
		"devices:",
		"  /dev/fuse <- /dev/fuse (rwm)",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("dry-run output missing %q; got:\n%s", want, output)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/toolpod/ -run TestRenderSpecPortsAndDevices -v`
Expected: FAIL (no ports/devices sections).

- [ ] **Step 3: Implement RenderSpec sections**

In `pkg/toolpod/dryrun.go`, after the `caches:` block, add:

```go
	if len(spec.PortSpecs) > 0 {
		_, err = fmt.Fprintln(w, "ports:")
		if err != nil {
			return err
		}
		for _, p := range spec.PortSpecs {
			_, err = fmt.Fprintf(w, "  %s/%s -> %s:%s\n", p.Container, p.Protocol, p.HostIP, p.HostPort)
			if err != nil {
				return err
			}
		}
	}
	if len(spec.DeviceSpecs) > 0 {
		_, err = fmt.Fprintln(w, "devices:")
		if err != nil {
			return err
		}
		for _, d := range spec.DeviceSpecs {
			suffix := ""
			if d.Cgroup {
				suffix = " cgroup"
			}
			_, err = fmt.Fprintf(w, "  %s <- %s (%s%s)\n", d.Container, d.Host, d.Perms, suffix)
			if err != nil {
				return err
			}
		}
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/toolpod/`
Expected: PASS.

- [ ] **Step 5: Document in README**

In `README.md`, in the profile schema section (find the table or example that documents `mounts:`/`environment:`), add a `ports` and `devices` block:

```markdown
### ports / devices

`ports` publishes container ports to the host. The key is the container port;
`host` is optional (missing or `0` = random, auto-allocated host port).

```yaml
ports:
  8080: {}          # random host port -> 8080/tcp
  5432:
    host: 5432      # fixed host port
  53:
    protocol: udp
  5433:
    host: 5433
    host_ip: 127.0.0.1
```

Allocated (and fixed) host ports are available to `{{ }}` templates via
`.Ports` — keyed by container port — in `command`, `args_if_none`, and
`environment`:

```yaml
command: ["opencode", "web", "--port", "{{ index .Ports \"8080\" }}"]
environment:
  PORT: '{{ index .Ports "8080" }}'
```

`devices` attaches host device nodes into the container:

```yaml
devices:
  /dev/fuse: {}            # source defaults to the same path on the host
  /dev/nvidia0: { permissions: rw }
  /dev/bus/usb: { source: /dev/bus/usb, cgroup: true }
```

`source` defaults to the key path; `permissions` is `r`, `rw`, or `rwm`
(default `rwm`); `cgroup: true` additionally emits a device-cgroup allow-rule
scoped to the device's major:minor (advanced; leave off unless the container
needs to create arbitrary device nodes).
```

- [ ] **Step 6: Commit**

```bash
git add pkg/toolpod/dryrun.go pkg/toolpod/dryrun_test.go README.md
git commit -m "feat(toolpod): render ports/devices in dry-run output; document schema"
```

---

### Task 7: Full verification pass

**Files:** none (verification only)

- [ ] **Step 1: Run the full suite**

Run: `go vet ./... && go test ./...`
Expected: all PASS, no vet complaints. (If the mise `go` shim errors, run `mise use -g go@1.26.5` first.)

- [ ] **Step 2: Manual smoke test (optional, requires Docker)**

```bash
mkdir -p /tmp/toolpod-smoke && cat > /tmp/toolpod-smoke/web.yaml <<'EOF'
version: 1
extends: shell
image: alpine:latest
command: ["sh", "-c", "echo PORT=$PORT; nc -l -p 8080"]
ports:
  8080: {}
environment:
  PORT: '{{ index .Ports "8080" }}'
EOF
toolpod --profile-dir /tmp/toolpod-smoke run --dry-run web
toolpod --profile-dir /tmp/toolpod-smoke run web
```

Expected: dry-run shows the allocated port in `ports:` and `PORT=` in the
environment; a real run prints `listening on tcp://127.0.0.1:<port>` and the
container echoes the same `PORT=` value.
