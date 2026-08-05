# Engine-native Debian cache via cache mounts — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep `.deb` files and the apt index warm across derived-image rebuilds by adding per-base-image cache mounts to the synthesized Dockerfile, routed through Docker's embedded BuildKit (via `version=2`) and buildah's native cache-mount support on Podman.

**Architecture:** `synthesizeDockerfile` emits `RUN --mount=type=cache` mounts for `/var/cache/apt` (keyed by base image ID) and `/var/lib/apt` (keyed by base ID + canonical repo set), neutralizes Debian's `docker-clean` hook, and drops the install-RUN `rm -rf /var/lib/apt/lists/*`. `buildDerivedImage` gains a `baseID` param and sets `options.Version = types.BuilderBuildKit`. On Docker this routes `/build` to the daemon's embedded BuildKit (no session needed); podman ignores the version param and buildah parses cache mounts natively. A TTY spinner replaces the build log on Docker, where BuildKit emits no classic `stream` lines.

**Tech Stack:** Go 1.25, docker SDK v27.1 (`github.com/docker/docker`), cobra CLI, `golang.org/x/term` (already used by `internal/ui`).

## Global Constraints

- Spec: `docs/superpowers/specs/2026-08-05-debian-cache-cache-mounts-design.md`.
- Docker floor ≥27.1, podman floor ≥6 (AGENTS.md); `go test ./...` and `go vet ./...` must pass after every task.
- Cache ids: `.deb` cache `tpd-<baseID-no-prefix>-apt` (base-keyed only); lists cache `tpd-<baseID-no-prefix>-<repoDigest16>-lists` (base + repos keyed). `<repoDigest16>` = first 16 hex of `sha256(canonicalRepos(repos))`, omitted when no repos. The `sha256:` prefix is stripped (unlike `DerivedTag`).
- Dockerfile rules: cache-mount lines are `RUN --mount=type=cache,id=<aptID>,target=/var/cache/apt,sharing=locked \` + `    --mount=type=cache,id=<listsID>,target=/var/lib/apt,sharing=locked \`. `docker-clean` is removed only at the top of the first mounted RUN; `-o APT::Keep-Downloaded-Packages=true` appears on `install` only, never on `update`. No `rm -rf /var/lib/apt/lists/*` on any mounted RUN.
- Comments: repo policy (AGENTS.md) — only comments that add non-apparent intent (e.g. why the lists cache is repos-keyed, why `version=2` is needed).
- Commit style: conventional commits; stage files individually (never `-A`).

---

### Task 1: `cacheMountIDs` helper

**Files:**
- Modify: `internal/runtime/docker_build.go` (add helper near `canonicalRepos`)
- Test: `internal/runtime/docker_build_test.go`

**Interfaces:**
- Produces: `func cacheMountIDs(baseID string, repos map[string]Repo) (aptID, listsID string)`. `canonicalRepos(repos map[string]Repo) string` already exists in the file (returns `""` when repos empty).

- [ ] **Step 1: Write the failing test**

Add to `internal/runtime/docker_build_test.go`:

```go
func TestCacheMountIDs(t *testing.T) {
	const baseID = "sha256:abc123"
	tests := []struct {
		name     string
		baseID   string
		repos    map[string]Repo
		wantApt  string
		wantList string
	}{
		{
			name:     "no repos: base-keyed ids",
			baseID:   baseID,
			wantApt:  "tpd-abc123-apt",
			wantList: "tpd-abc123-lists",
		},
		{
			name:     "prefix stripped",
			baseID:   "sha256:abcdef0123456789",
			wantApt:  "tpd-abcdef0123456789-apt",
			wantList: "tpd-abcdef0123456789-lists",
		},
		{
			name:    "repos key the lists id, not the apt id",
			baseID:  baseID,
			repos:   map[string]Repo{"mise": {ExtRepo: "mise"}},
			wantApt: "tpd-abc123-apt",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			aptID, listsID := cacheMountIDs(tt.baseID, tt.repos)
			if aptID != tt.wantApt {
				t.Errorf("apt id = %q, want %q", aptID, tt.wantApt)
			}
			if tt.wantList != "" && listsID != tt.wantList {
				t.Errorf("lists id = %q, want %q", listsID, tt.wantList)
			}
		})
	}

	// Contract: apt id varies with base only; lists id varies with repos too.
	aApt, aList := cacheMountIDs(baseID, nil)
	bApt, bList := cacheMountIDs(baseID, map[string]Repo{"a": {ExtRepo: "a"}})
	cList, _ := cacheMountIDs(baseID, map[string]Repo{"b": {ExtRepo: "b"}})
	if aApt != bApt {
		t.Errorf("apt id must be base-keyed only: %q vs %q", aApt, bApt)
	}
	if aList == bList {
		t.Errorf("lists id must vary with repos: %q vs %q", aList, bList)
	}
	if bList == cList {
		t.Errorf("distinct repo sets must produce distinct lists ids: %q", bList)
	}
	if strings.HasPrefix(aList, "tpd-sha256:") {
		t.Errorf("lists id must strip the sha256: prefix: %q", aList)
	}
	if dList, _ := cacheMountIDs(baseID, map[string]Repo{"a": {ExtRepo: "a"}}); dList != bList {
		t.Errorf("cache ids must be deterministic: %q vs %q", dList, bList)
	}
}
```

(`strings` is already imported in this test file.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runtime/ -run TestCacheMountIDs -v`
Expected: FAIL — `undefined: cacheMountIDs`.

- [ ] **Step 3: Write the implementation**

Add to `internal/runtime/docker_build.go`, after `canonicalRepos`:

```go
// cacheMountIDs returns the cache-mount ids for a derived build. The .deb
// cache is keyed by base image only (apt reuses a cached archive only when the
// current index references the same filename and size), while the lists cache
// is keyed by base and canonical repo set: index files are source-specific, so
// sharing them across profiles with different repos could resolve packages
// from a repo the profile never declared. The sha256: prefix is stripped,
// unlike DerivedTag.
func cacheMountIDs(baseID string, repos map[string]Repo) (aptID, listsID string) {
	base := strings.TrimPrefix(baseID, "sha256:")
	aptID = "tpd-" + base + "-apt"
	listsID = "tpd-" + base + "-lists"
	if cr := canonicalRepos(repos); cr != "" {
		sum := sha256.Sum256([]byte(cr))
		listsID = "tpd-" + base + "-" + hex.EncodeToString(sum[:])[:16] + "-lists"
	}
	return aptID, listsID
}
```

(`sha256` and `hex` are already imported in this file.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/runtime/ -run TestCacheMountIDs`
Expected: PASS.

- [ ] **Step 5: Run the full package tests and vet**

Run: `go test ./internal/runtime/ && go vet ./internal/runtime/`
Expected: PASS (no existing behavior changed yet).

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/docker_build.go internal/runtime/docker_build_test.go
git commit -m "feat(runtime): add cacheMountIDs for derived-build cache mounts"
```

---

### Task 2: `synthesizeDockerfile` emits cache-mount RUNs

**Files:**
- Modify: `internal/runtime/docker_build.go` (`synthesizeDockerfile`, new `cacheMountLines` helper)
- Test: `internal/runtime/docker_build_test.go`

**Interfaces:**
- Consumes: `cacheMountIDs(baseID string, repos map[string]Repo) (aptID, listsID string)` (Task 1).
- Produces: `func synthesizeDockerfile(baseRef string, repos []resolvedRepo, packages []string, aptCacheID, listsCacheID string) string`. Existing callers/tests of the 3-arg form must be updated in this task (they will not compile otherwise): `TestSynthesizeDockerfile`, `TestSynthesizeDockerfileRepos`, `TestSynthesizeDockerfileOrderIndependent`, `TestSynthesizeDockerfileShellQuotesPackages`, and `buildDerivedImage` (its signature is updated in Step 4 of this task; the `Version` flag and the extended `TestBuildDerivedImageLabelsImage` assertions land in Task 3).

- [ ] **Step 1: Update `synthesizeDockerfile` and add `cacheMountLines`**

Replace the body of `synthesizeDockerfile` in `internal/runtime/docker_build.go` with:

```go
func synthesizeDockerfile(baseRef string, repos []resolvedRepo, packages []string, aptCacheID, listsCacheID string) string {
	sorted := sortedCopy(packages)
	quoted := make([]string, len(sorted))
	for i, p := range sorted {
		quoted[i] = shellQuote([]string{p})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s\n", baseRef)
	if len(repos) > 0 {
		// The ca-certificates bootstrap is the first mounted RUN: it removes
		// docker-clean before its own install so the dpkg hook cannot purge the
		// cache mount, and its index/debs land in the shared mounts.
		b.WriteString(cacheMountLines(aptCacheID, listsCacheID))
		b.WriteString("    rm -f /etc/apt/apt.conf.d/docker-clean \\\n")
		b.WriteString("    && apt-get update \\\n")
		b.WriteString("    && apt-get -o APT::Keep-Downloaded-Packages=true install -y --no-install-recommends ca-certificates\n")
	}
	for _, r := range sortedResolvedRepos(repos) {
		fmt.Fprintf(&b, "COPY extrepo/%s.sources /etc/apt/sources.list.d/extrepo_%s.sources\n", r.name, r.name)
		fmt.Fprintf(&b, "COPY extrepo/%s.asc /etc/apt/keyrings/%s.asc\n", r.name, r.name)
	}
	b.WriteString(cacheMountLines(aptCacheID, listsCacheID))
	if len(repos) == 0 {
		// Packages-only build: this is the first mounted RUN, so it removes
		// docker-clean itself.
		b.WriteString("    rm -f /etc/apt/apt.conf.d/docker-clean \\\n")
		b.WriteString("    && apt-get update \\\n")
	} else {
		// docker-clean was already removed by the bootstrap RUN.
		b.WriteString("    apt-get update \\\n")
	}
	b.WriteString("    && apt-get -o APT::Keep-Downloaded-Packages=true install -y --no-install-recommends \\\n")
	b.WriteString("        " + strings.Join(quoted, " \\\n        ") + "\n")
	return b.String()
}

func cacheMountLines(aptCacheID, listsCacheID string) string {
	return "RUN --mount=type=cache,id=" + aptCacheID + ",target=/var/cache/apt,sharing=locked \\\n" +
		"    --mount=type=cache,id=" + listsCacheID + ",target=/var/lib/apt,sharing=locked \\\n"
}
```

Update the function's doc comment to note the cache-mount emission, the scoped `docker-clean` removal, and the absence of a lists `rm` (lists live in the mount, not a layer).

- [ ] **Step 2: Update the existing `synthesizeDockerfile` tests**

Replace `TestSynthesizeDockerfile`, `TestSynthesizeDockerfileRepos`, `TestSynthesizeDockerfileOrderIndependent`, and `TestSynthesizeDockerfileShellQuotesPackages` in `internal/runtime/docker_build_test.go` with:

```go
func TestSynthesizeDockerfile(t *testing.T) {
	const baseRef = "debian:13-slim"
	const aptID, listsID = "tpd-abc123-apt", "tpd-abc123-lists"
	got := synthesizeDockerfile(baseRef, nil, []string{"libxml2-dev", "git"}, aptID, listsID)
	if !strings.Contains(got, "FROM "+baseRef+"\n") {
		t.Errorf("dockerfile must start with FROM baseRef:\n%s", got)
	}
	wantInstall := "'git' \\\n        'libxml2-dev'"
	if !strings.Contains(got, wantInstall) {
		t.Errorf("dockerfile must contain sorted shell-quoted packages:\n%s\nwant substring:\n%s", got, wantInstall)
	}
	if !strings.Contains(got, "apt-get -o APT::Keep-Downloaded-Packages=true install -y --no-install-recommends") {
		t.Errorf("dockerfile must keep downloaded packages on install:\n%s", got)
	}
	if strings.Contains(got, "rm -rf /var/lib/apt/lists/*") {
		t.Errorf("repo-less dockerfile must not clean apt lists:\n%s", got)
	}
	if strings.Contains(got, "ca-certificates") {
		t.Errorf("repo-less dockerfile must not bootstrap ca-certificates:\n%s", got)
	}
	if strings.Count(got, "apt-get update") != 1 {
		t.Errorf("repo-less dockerfile must have a single apt-get update:\n%s", got)
	}
	for _, want := range []string{
		"--mount=type=cache,id=" + aptID + ",target=/var/cache/apt,sharing=locked",
		"--mount=type=cache,id=" + listsID + ",target=/var/lib/apt,sharing=locked",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("dockerfile must contain cache mount %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "rm -f /etc/apt/apt.conf.d/docker-clean") {
		t.Errorf("dockerfile must neutralize docker-clean inside the mount RUN:\n%s", got)
	}
}

func TestSynthesizeDockerfileRepos(t *testing.T) {
	const baseRef = "base:1"
	const aptID, listsID = "tpd-abc-apt", "tpd-abc-lists"
	repos := []resolvedRepo{
		{name: "mise"},
		{name: "nodejs"},
		{name: "extrane"},
	}
	got := synthesizeDockerfile(baseRef, repos, []string{"mise"}, aptID, listsID)
	if !strings.Contains(got, "FROM "+baseRef+"\n") {
		t.Errorf("dockerfile must start with FROM baseRef:\n%s", got)
	}
	for _, want := range []string{"COPY extrepo/extrane.sources", "COPY extrepo/mise.sources", "COPY extrepo/nodejs.sources"} {
		if !strings.Contains(got, want) {
			t.Errorf("dockerfile must contain %q:\n%s", want, got)
		}
	}
	pos := make([]int, 3)
	for i, want := range []string{"extrane", "mise", "nodejs"} {
		pos[i] = strings.Index(got, "COPY extrepo/"+want+".sources")
		if pos[i] < 0 {
			t.Fatalf("missing %q in dockerfile:\n%s", want, got)
		}
	}
	if !(pos[0] < pos[1] && pos[1] < pos[2]) {
		t.Errorf("repo COPYs must be sorted by name: %v", pos)
	}
	if !strings.Contains(got, "COPY extrepo/mise.asc /etc/apt/keyrings/mise.asc") {
		t.Errorf("dockerfile must COPY the key to apt keyrings:\n%s", got)
	}
	certInstall := "apt-get -o APT::Keep-Downloaded-Packages=true install -y --no-install-recommends ca-certificates"
	if !strings.Contains(got, certInstall) {
		t.Errorf("dockerfile must bootstrap ca-certificates when repos present:\n%s", got)
	}
	// The mounted bootstrap RUN (first apt-get update) precedes the COPYs and
	// the install RUN (second apt-get update).
	firstUpdate := strings.Index(got, "apt-get update")
	firstCopy := strings.Index(got, "COPY extrepo/extrane.sources")
	secondUpdate := strings.LastIndex(got, "apt-get update")
	if !(firstUpdate < firstCopy && firstCopy < secondUpdate) {
		t.Errorf("bootstrap RUN must precede COPYs and install RUN:\n%s", got)
	}
	if strings.Count(got, "rm -f /etc/apt/apt.conf.d/docker-clean") != 1 {
		t.Errorf("docker-clean must be removed exactly once (bootstrap RUN):\n%s", got)
	}
	if strings.Contains(got, "rm -rf /var/lib/apt/lists/*") {
		t.Errorf("no RUN may clean apt lists:\n%s", got)
	}
	aptMount := "--mount=type=cache,id=" + aptID + ",target=/var/cache/apt,sharing=locked"
	if c := strings.Count(got, aptMount); c != 2 {
		t.Errorf("apt cache mount must appear on both RUNs, got %d:\n%s", c, got)
	}
}

func TestSynthesizeDockerfileOrderIndependent(t *testing.T) {
	const baseRef = "base:1"
	a := synthesizeDockerfile(baseRef, nil, []string{"git", "curl"}, "a-apt", "a-lists")
	b := synthesizeDockerfile(baseRef, nil, []string{"curl", "git"}, "a-apt", "a-lists")
	if a != b {
		t.Errorf("dockerfile synthesis must be sort-normalised:\nA:\n%s\nB:\n%s", a, b)
	}
}

func TestSynthesizeDockerfileShellQuotesPackages(t *testing.T) {
	// A hostile package name must not break out of the RUN step. Validation
	// rejects these, but the emission path is defense-in-depth.
	got := synthesizeDockerfile("base:1", nil, []string{"libxml2-dev;rm -rf /"}, "a-apt", "a-lists")
	if strings.Contains(got, "libxml2-dev;rm -rf /") && !strings.Contains(got, "'libxml2-dev;rm -rf /'") {
		t.Errorf("package name must be shell-quoted:\n%s", got)
	}
}
```

- [ ] **Step 3: Run the tests to verify the updated Dockerfile**

Run: `go test ./internal/runtime/ -run 'TestSynthesizeDockerfile'`
Expected: FAIL to compile — `buildDerivedImage` (docker_build.go:121) still calls the 3-arg `synthesizeDockerfile`.

- [ ] **Step 4: Update `buildDerivedImage` and its test call site**

Change the `buildDerivedImage` signature to add `baseID`, derive the cache ids, and pass them into synthesis (the `Version: types.BuilderBuildKit` flag lands in Task 3):

```go
func buildDerivedImage(ctx context.Context, cli *client.Client, derivedRef, baseRef, baseID string, repos map[string]Repo, packages []string, w ProgressWriter) error {
	resolved, err := resolveExtrepoRepos(ctx, cli, baseRef, repos)
	if err != nil {
		return fmt.Errorf("resolve repos: %w", err)
	}
	aptID, listsID := cacheMountIDs(baseID, repos)
	dockerfile := []byte(synthesizeDockerfile(baseRef, resolved, packages, aptID, listsID))
```

Update the call in `TestBuildDerivedImageLabelsImage` (docker_build_test.go:256) to the new arity, passing the fake base id the Dockerfile assertions rely on (Task 3 extends the assertions):

```go
	if err := buildDerivedImage(context.Background(), cli, "tpd/packages:abc123", "debian:13-slim", "sha256:abc123", nil, []string{"git"}, &recordingWriter{}); err != nil {
		t.Fatalf("buildDerivedImage: %v", err)
	}
```

- [ ] **Step 5: Update the two production `buildDerivedImage` call sites to pass `baseID`**

In `internal/runtime/docker_prepare.go` (line ~77, `baseID` is in scope from line 26):

```go
			if err := buildDerivedImage(ctx, d.cli, derivedRef, baseRef, baseID, spec.Repos, spec.Packages, w); err != nil {
```

In `internal/runtime/docker_services.go` (line ~194, `baseID` is in scope from line 178):

```go
			if err := buildDerivedImage(ctx, d.cli, derivedRef, svc.Image, baseID, svc.Repos, svc.Packages, w); err != nil {
```

- [ ] **Step 6: Run the tests to verify the task boundary is green**

Run: `go test ./internal/runtime/`
Expected: PASS. (`TestBuildDerivedImageLabelsImage` still asserts labels only at this point; the `version=2` and cache-mount-Dockerfile assertions are added in Task 3.)

- [ ] **Step 7: Commit**

```bash
git add internal/runtime/docker_build.go internal/runtime/docker_prepare.go internal/runtime/docker_services.go internal/runtime/docker_build_test.go
git commit -m "feat(runtime): emit cache-mount RUNs in derived Dockerfile"
```

---

### Task 3: BuildKit `version=2` on Docker + call-site/tests updates

**Files:**
- Modify: `internal/runtime/docker_build.go` (`buildDerivedImage`), `internal/runtime/docker_build_test.go`, `internal/runtime/docker_services_test.go`
- Test: `internal/runtime/docker_build_test.go`, `internal/runtime/docker_services_test.go`

**Interfaces:**
- Consumes: `synthesizeDockerfile(baseRef, repos, packages, aptCacheID, listsCacheID)` (Task 2), `cacheMountIDs` (Task 1).
- Produces: `buildDerivedImage(ctx, cli, derivedRef, baseRef, baseID, repos, packages, w)` sets `Version: types.BuilderBuildKit`.

- [ ] **Step 1: Write the failing `version=2` + Dockerfile assertions**

Replace `TestBuildDerivedImageLabelsImage` in `internal/runtime/docker_build_test.go` with:

```go
func TestBuildDerivedImageLabelsImage(t *testing.T) {
	var gotLabels, gotVersion, gotDockerfile string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/build") {
			http.NotFound(w, r)
			return
		}
		gotLabels = r.URL.Query().Get("labels")
		gotVersion = r.URL.Query().Get("version")
		tr := tar.NewReader(r.Body)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, "bad tar", http.StatusBadRequest)
				return
			}
			content, err := io.ReadAll(tr)
			if err != nil {
				http.Error(w, "read tar", http.StatusBadRequest)
				return
			}
			if hdr.Name == "Dockerfile" {
				gotDockerfile = string(content)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"stream":"Successfully built abc123\n"}`+"\n")
	}))
	defer srv.Close()

	cli, err := client.NewClientWithOpts(
		client.WithHost("tcp://"+srv.Listener.Addr().String()),
		client.WithVersion("1.41"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := buildDerivedImage(context.Background(), cli, "tpd/packages:abc123", "debian:13-slim", "sha256:abc123", nil, []string{"git"}, &recordingWriter{}); err != nil {
		t.Fatalf("buildDerivedImage: %v", err)
	}
	want := OwnershipLabels()
	want["tpd.build"] = "1"
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if gotLabels != string(wantJSON) {
		t.Errorf("build labels = %s, want %s", gotLabels, wantJSON)
	}
	if gotVersion != "2" {
		t.Errorf("build request version = %q, want %q (daemon must use buildkit)", gotVersion, "2")
	}
	for _, want := range []string{
		"--mount=type=cache,id=tpd-abc123-apt,target=/var/cache/apt,sharing=locked",
		"--mount=type=cache,id=tpd-abc123-lists,target=/var/lib/apt,sharing=locked",
	} {
		if !strings.Contains(gotDockerfile, want) {
			t.Errorf("build Dockerfile must contain %q:\n%s", want, gotDockerfile)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/runtime/ -run TestBuildDerivedImageLabelsImage -v`
Expected: FAIL — the request currently has no `version=2` and the Dockerfile has no cache mounts.

- [ ] **Step 3: Set `Version` in `buildDerivedImage`**

In `internal/runtime/docker_build.go`, add `Version: types.BuilderBuildKit` to the `ImageBuildOptions`:

```go
	resp, err := cli.ImageBuild(ctx, buildContext, types.ImageBuildOptions{
		Tags:       []string{derivedRef},
		Dockerfile: "Dockerfile",
		Labels:     labels,
		Remove:     true,
		Version:    types.BuilderBuildKit,
	})
```

Update the `buildDerivedImage` doc comment: the `version=2` flag is required so the Docker daemon dispatches to its embedded buildkit (which parses the cache-mount Dockerfile); podman's compat endpoint ignores the param and buildah parses cache mounts natively.

`types` is already imported in this file.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/runtime/ -run TestBuildDerivedImageLabelsImage`
Expected: PASS.

- [ ] **Step 5: Write the failing service build-path test**

Add to `internal/runtime/docker_services_test.go`. The service uses `packages:` **without** `repos:` — a repos entry would drive `buildDerivedImage` through `resolveExtrepoRepos` (live extrepo catalog fetch + a `CopyFromContainer` read of `/etc/os-release` the fake does not serve), so the repos-free path keeps this test hermetic; the repo-keyed lists id is already covered by `TestCacheMountIDs` (Task 1) and the repos `synthesizeDockerfile` test (Task 2):

```go
func TestStartServicesBuildPassesBaseID(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	// The fake creates the exposed socket on ContainerStart, so the socket
	// poll in StartServices succeeds (mirrors TestStartServicesCreatesNewService).
	daemon.sockets = map[string][]string{
		"tpd-svc-db": {filepath.Join(runDir, "db", "run", "db.sock")},
	}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	spec.Services[0].Packages = []string{"pkg1"}

	bindings, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("StartServices: %v", err)
	}
	defer bindings.Release()

	if len(daemon.buildReqs) != 1 {
		t.Fatalf("build requests = %d, want 1", len(daemon.buildReqs))
	}
	req := daemon.buildReqs[0]
	if req.version != "2" {
		t.Errorf("service build version = %q, want %q", req.version, "2")
	}
	// The fake's base image id is "sha256:base"; the cache ids must derive from
	// it, proving createService threads its resolved baseID through.
	aptID, listsID := cacheMountIDs(daemon.imageID, nil)
	for _, want := range []string{
		"--mount=type=cache,id=" + aptID + ",target=/var/cache/apt,sharing=locked",
		"--mount=type=cache,id=" + listsID + ",target=/var/lib/apt,sharing=locked",
	} {
		if !strings.Contains(req.dockerfile, want) {
			t.Errorf("service build Dockerfile must contain %q:\n%s", want, req.dockerfile)
		}
	}
}
```

- [ ] **Step 6: Run the test to verify it fails**

Run: `go test ./internal/runtime/ -run TestStartServicesBuildPassesBaseID -v`
Expected: FAIL — `build requests = 0, want 1`. The fake's `/json` handler reports every image (including `tpd/packages:...`) as present, so `createService`'s `imageExists` short-circuits and no build is requested.

- [ ] **Step 7: Extend the services fake daemon for `/build`**

In `internal/runtime/docker_services_test.go`:
- Add to the `fakeServicesDaemon` struct:

```go
	derivedPresent bool
	derivedID      string
	buildReqs      []fakeBuildReq
```

- Add the type:

```go
type fakeBuildReq struct {
	version    string
	dockerfile string
}
```

- Add `"archive/tar"` and `"net/url"` to the imports (neither is currently in the file; the import block ends with `"golang.org/x/sys/unix"`).
- Replace the `/json` case in `ServeHTTP` with a ref-aware handler and add a `/build` case:

```go
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/json"):
		if !strings.HasPrefix(p, "images/") {
			fmt.Fprintf(w, `{"Id":%q}`, f.imageID)
			return
		}
		ref, err := url.PathUnescape(strings.TrimSuffix(strings.TrimPrefix(p, "images/"), "/json"))
		if err != nil {
			http.Error(w, "bad ref", http.StatusBadRequest)
			return
		}
		if strings.HasPrefix(ref, "tpd/packages:") {
			if !f.derivedPresent {
				http.Error(w, `{"message":"No such image"}`, http.StatusNotFound)
				return
			}
			fmt.Fprintf(w, `{"Id":%q}`, f.derivedID)
			return
		}
		if !f.imagePresent {
			http.Error(w, `{"message":"No such image"}`, http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"Id":%q}`, f.imageID)
	case r.Method == http.MethodPost && p == "build":
		f.buildReqs = append(f.buildReqs, readFakeBuildReq(w, r))
		io.Copy(io.Discard, r.Body)
		f.derivedPresent = true
		f.derivedID = "sha256:derived"
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"stream":"Successfully built derived\n"}`+"\n")
```

- Add the helper function at the end of the file:

```go
func readFakeBuildReq(w http.ResponseWriter, r *http.Request) fakeBuildReq {
	var req fakeBuildReq
	req.version = r.URL.Query().Get("version")
	tr := tar.NewReader(r.Body)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return req
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			return req
		}
		if hdr.Name == "Dockerfile" {
			req.dockerfile = string(content)
		}
	}
	return req
}
```

- [ ] **Step 8: Run the test to verify it passes**

Run: `go test ./internal/runtime/ -run TestStartServicesBuildPassesBaseID`
Expected: PASS — the ref-aware `/json` reports the derived ref as absent, so `createService` builds it and the captured Dockerfile carries the `baseID`-derived ids.

- [ ] **Step 9: Run the full package and the whole suite**

Run: `go test ./internal/runtime/ && go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/runtime/docker_build.go internal/runtime/docker_build_test.go internal/runtime/docker_services_test.go
git commit -m "feat(runtime): route derived builds through buildkit (version=2)"
```

---

### Task 4: TTY spinner progress writer

**Files:**
- Create: `pkg/tpd/spinner.go`
- Modify: `pkg/tpd/launch.go`
- Test: `pkg/tpd/spinner_test.go` (new)

**Interfaces:**
- Consumes: `runtime.ProgressWriter` (`WriteProgress(line string)`), `ui.IsTTY(w io.Writer) bool`, `stderrProgress` (launch.go:217).
- Produces: `newSpinnerProgress(out io.Writer, inner runtime.ProgressWriter, tty bool) *spinnerProgress` with `Start()`, `Stop()`, `WriteProgress(string)`, and an `interval time.Duration` field (tests set it small).

- [ ] **Step 1: Write the failing spinner tests**

Create `pkg/tpd/spinner_test.go`:

```go
package tpd

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordProgress struct {
	mu    sync.Mutex
	lines []string
}

func (r *recordProgress) WriteProgress(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
}

func TestSpinnerNonTTYPassesThrough(t *testing.T) {
	var out bytes.Buffer
	var inner recordProgress
	s := newSpinnerProgress(&out, &inner, false)
	s.Start()
	s.WriteProgress("pull: debian:13-slim")
	s.WriteProgress("build: tpd/packages:abc")
	s.Stop()
	inner.mu.Lock()
	defer inner.mu.Unlock()
	if len(inner.lines) != 2 {
		t.Fatalf("inner lines = %v, want 2 pass-through lines", inner.lines)
	}
	if out.Len() != 0 {
		t.Errorf("non-TTY must not write spinner output: %q", out.String())
	}
}

func TestSpinnerTTYSwallowsLinesAndClears(t *testing.T) {
	var out bytes.Buffer
	var inner recordProgress
	s := newSpinnerProgress(&out, &inner, true)
	s.interval = time.Millisecond
	s.Start()
	s.WriteProgress("build: tpd/packages:abc")
	time.Sleep(20 * time.Millisecond)
	s.Stop()
	inner.mu.Lock()
	defer inner.mu.Unlock()
	if len(inner.lines) != 0 {
		t.Errorf("TTY must not pass lines through: %v", inner.lines)
	}
	if !strings.Contains(out.String(), "\r\x1b[2K") {
		t.Errorf("TTY must render frames and clear the line on Stop: %q", out.String())
	}
}

func TestSpinnerConcurrentWriteAndStop(t *testing.T) {
	var out bytes.Buffer
	var inner recordProgress
	s := newSpinnerProgress(&out, &inner, true)
	s.interval = time.Millisecond
	s.Start()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s.WriteProgress(strings.Repeat("x", 100))
			}
		}()
	}
	time.Sleep(5 * time.Millisecond)
	s.Stop()
	wg.Wait()
}

func TestSpinnerStopIdempotent(t *testing.T) {
	var out bytes.Buffer
	var inner recordProgress
	s := newSpinnerProgress(&out, &inner, true)
	s.Start()
	s.Stop()
	s.Stop()
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./pkg/tpd/ -run TestSpinner -v`
Expected: FAIL — `undefined: newSpinnerProgress`.

- [ ] **Step 3: Implement the spinner**

Create `pkg/tpd/spinner.go`:

```go
package tpd

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/jgillich/tpd/internal/runtime"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerProgress shows an indeterminate spinner in place of build log lines on
// a TTY; on a non-TTY it passes lines through to inner unchanged. Docker's
// buildkit backend emits no classic stream lines, so the build log would be
// silent there without it.
type spinnerProgress struct {
	mu       sync.Mutex
	out      io.Writer
	inner    runtime.ProgressWriter
	tty      bool
	interval time.Duration
	label    string
	stop     chan struct{}
	done     chan struct{}
	active   bool
}

func newSpinnerProgress(out io.Writer, inner runtime.ProgressWriter, tty bool) *spinnerProgress {
	return &spinnerProgress{out: out, inner: inner, tty: tty, interval: 100 * time.Millisecond}
}

func (s *spinnerProgress) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active || !s.tty {
		return
	}
	s.active = true
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	go s.run()
}

func (s *spinnerProgress) run() {
	defer close(s.done)
	i := 0
	for {
		select {
		case <-s.stop:
			return
		case <-time.After(s.interval):
		}
		s.mu.Lock()
		frame := spinnerFrames[i%len(spinnerFrames)]
		i++
		label := s.label
		s.mu.Unlock()
		fmt.Fprintf(s.out, "\r\x1b[2K%s %s", frame, label)
	}
}

func (s *spinnerProgress) Stop() {
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return
	}
	s.active = false
	close(s.stop)
	s.mu.Unlock()
	<-s.done
	s.mu.Lock()
	fmt.Fprint(s.out, "\r\x1b[2K")
	s.mu.Unlock()
}

func (s *spinnerProgress) WriteProgress(line string) {
	s.mu.Lock()
	if s.tty && s.active {
		s.label = truncate(line, 80)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.inner.WriteProgress(line)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./pkg/tpd/ -run TestSpinner -race`
Expected: PASS (run with `-race` to exercise the concurrency test).

- [ ] **Step 5: Wire the spinner into launch**

In `pkg/tpd/launch.go`:
- Add the import `"github.com/jgillich/tpd/internal/ui"`.
- In `LaunchWithWriter`, replace the `progress := progress` shadow line (currently line ~133) and the `rt.Prepare(...progress...)`/`rt.StartServices(...progress...)` calls so the spinner wraps the active writer and is stopped before the container runs:

```go
		sp := newSpinnerProgress(os.Stderr, progress, ui.IsTTY(os.Stderr))
		sp.Start()
		defer sp.Stop()

		imageRef, err := rt.Prepare(ctx, spec, sp, opts.Pull)
		if err != nil {
			return Result{ExitCode: 3, Err: fmt.Errorf("prepare: %w", err)}
		}
```

Change the `rt.StartServices(ctx, spec, progress, opts.Pull)` call (line ~167) to pass `sp` instead of `progress`. After the service socket-binding loop (just before `created, err := rt.CreateContainer(ctx, runSpec)`), add:

```go
		sp.Stop()
```

The `defer sp.Stop()` covers every error path in the block; the explicit `sp.Stop()` stops the animation before the (potentially long) container run. `Stop` is idempotent, so the double call is safe.

- [ ] **Step 6: Run the launch tests**

Run: `go test ./pkg/tpd/`
Expected: PASS (tests run non-TTY, so the spinner passes lines through unchanged).

- [ ] **Step 7: Run the full suite and vet**

Run: `go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add pkg/tpd/spinner.go pkg/tpd/spinner_test.go pkg/tpd/launch.go
git commit -m "feat(cli): replace build log with TTY spinner"
```

---

### Task 5: Hermetic integration test for cache reuse

**Files:**
- Test: `internal/runtime/docker_test.go` (append)

**Interfaces:**
- Consumes: `buildDerivedImage(ctx, cli, derivedRef, baseRef, baseID, repos, packages, w)`, `DerivedTag`, `ResolveImageID` (all exist).

- [ ] **Step 1: Write the integration test and helpers**

First add the missing imports to `internal/runtime/docker_test.go` (the file already imports `bytes`, `context`, `fmt`, `net`, `net/http`, `net/http/httptest`, `os`, `strings`, `sync`, `testing`, `time`, `github.com/docker/docker/api/types`, `github.com/docker/docker/api/types/image`, and `github.com/docker/docker/client`):

```go
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"io"
	"net/url"
```

Then append to the file:

```go
const mirrorSuite = "tpd"

type aptMirror struct {
	srv     *httptest.Server
	port    int
	arch    string
	mu      sync.Mutex
	gets    map[string]int
	debs    map[string][]byte
	index   []byte
	release []byte
}

func startAptMirror(t *testing.T, arch string) *aptMirror {
	m := &aptMirror{arch: arch, gets: map[string]int{}}
	pkgs := []struct {
		name, version, arch string
		data                []byte
	}{
		{"pkg1", "1.0", arch, makeDeb("pkg1", "1.0", arch)},
		{"pkg2", "1.0", arch, makeDeb("pkg2", "1.0", arch)},
	}
	m.debs = map[string][]byte{}
	for _, p := range pkgs {
		m.debs[p.name] = p.data
	}
	m.index = buildPackagesIndex(pkgs)
	now := time.Now().UTC()
	m.release = []byte(fmt.Sprintf(
		"Suite: %s\nCodename: %s\nComponents: main\nArchitectures: %s\nDate: %s\nValid-Until: %s\n",
		mirrorSuite, mirrorSuite, arch, now.Format(time.RFC1123Z), now.AddDate(0, 0, 10).Format(time.RFC1123Z),
	))

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// URL layout: the source line uses the repo ROOT as the base URI and
		// "tpd" as the suite, so apt requests dists/tpd/... for indexes and
		// pool/main/<filename> (the index Filename) for .debs.
		path := strings.TrimPrefix(r.URL.Path, "/")
		m.mu.Lock()
		m.gets[path]++
		m.mu.Unlock()
		switch path {
		case "dists/" + mirrorSuite + "/Release":
			_, _ = w.Write(m.release)
		case "dists/" + mirrorSuite + "/main/binary-" + arch + "/Packages":
			_, _ = w.Write(m.index)
		case "pool/main/pkg1_1.0_" + arch + ".deb":
			_, _ = w.Write(m.debs["pkg1"])
		case "pool/main/pkg2_1.0_" + arch + ".deb":
			_, _ = w.Write(m.debs["pkg2"])
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewUnstartedServer(mux)
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	m.srv = srv
	m.port = ln.Addr().(*net.TCPAddr).Port
	return m
}

func (m *aptMirror) debGets(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gets["pool/main/"+name+"_1.0_"+m.arch+".deb"]
}

func buildPackagesIndex(pkgs []struct{ name, version, arch string; data []byte }) []byte {
	var b strings.Builder
	for _, p := range pkgs {
		sum := sha256.Sum256(p.data)
		fmt.Fprintf(&b, "Package: %s\nVersion: %s\nArchitecture: %s\nFilename: pool/main/%s_%s_%s.deb\nSize: %d\nSHA256: %x\n\n",
			p.name, p.version, p.arch, p.name, p.version, p.arch, len(p.data), sum)
	}
	return []byte(b.String())
}

func makeDeb(name, version, arch string) []byte {
	control := gzipBytes(tarBytes(map[string]string{
		"./control": fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: %s\nMaintainer: tpd test\nDescription: test package\n",
			name, version, arch),
	}))
	data := gzipBytes(tarBytes(map[string]string{
		"./usr/share/doc/" + name + "/README": "tpd integration test package " + name + "\n",
	}))
	return arArchive([]arMember{
		{name: "debian-binary", data: []byte("2.0\n")},
		{name: "control.tar.gz", data: control},
		{name: "data.tar.gz", data: data},
	})
}

type arMember struct {
	name string
	data []byte
}

// arArchive writes a minimal ar archive: 60-byte headers with space-padded
// names, matching dpkg's member layout for .deb files.
func arArchive(members []arMember) []byte {
	var b bytes.Buffer
	b.WriteString("!<arch>\n")
	for _, m := range members {
		fmt.Fprintf(&b, "%-16s%-12d%-6d%-6d%-8o%-10d`\n", m.name, 0, 0, 0, 0o100644, len(m.data))
		b.Write(m.data)
		if len(m.data)%2 == 1 {
			b.WriteByte('\n')
		}
	}
	return b.Bytes()
}

func tarBytes(files map[string]string) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))})
		_, _ = tw.Write([]byte(content))
	}
	_ = tw.Close()
	return buf.Bytes()
}

func gzipBytes(data []byte) []byte {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, _ = gw.Write(data)
	_ = gw.Close()
	return buf.Bytes()
}

func primaryNonLoopbackIPv4(t *testing.T) string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	host, _, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func isLocalDockerHost(host string) bool {
	if host == "" {
		return false
	}
	u, err := url.Parse(host)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "unix", "npipe":
		return true
	case "tcp", "http", "https":
		h := u.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	default:
		return false
	}
}

func TestIntegrationCacheMountsReuse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if os.Getenv("DOCKER_HOST") == "" {
		t.Skip("DOCKER_HOST not set")
	}
	if !isLocalDockerHost(os.Getenv("DOCKER_HOST")) {
		t.Skip("remote DOCKER_HOST: test mirror unreachable")
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	baseRef := "debian:13-slim"
	if _, _, err := cli.ImageInspectWithRaw(ctx, baseRef); err != nil {
		// The base build below pulls it; pull explicitly so the failure is
		// reported here with context.
		reader, err := cli.ImagePull(ctx, baseRef, image.PullOptions{})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, reader)
		_ = reader.Close()
	}

	arch := "amd64"
	if inspect, _, err := cli.ImageInspectWithRaw(ctx, baseRef); err == nil && inspect.Architecture != "" {
		arch = inspect.Architecture
	}

	mirror := startAptMirror(t, arch)
	sourceLine := fmt.Sprintf("deb [trusted=yes] http://%s:%d/ %s main", primaryNonLoopbackIPv4(t), mirror.port, mirrorSuite)

	// Hermetic base: default Debian sources neutralized, only the mirror.
	const baseTag = "tpd-test-cache-base:1"
	buildHermeticBase(t, cli, baseTag, sourceLine)

	baseID, err := ResolveImageID(ctx, cli, baseTag)
	if err != nil {
		t.Fatal(err)
	}

	builds := []struct {
		name string
		pkgs []string
	}{
		{"a", []string{"pkg1"}},
		{"b", []string{"pkg1", "pkg2"}},
	}
	var builtRefs []string
	for _, b := range builds {
		ref := DerivedTag(baseID, b.pkgs, nil)
		builtRefs = append(builtRefs, ref)
		if err := buildDerivedImage(ctx, cli, ref, baseTag, baseID, nil, b.pkgs, NoopProgressWriter{}); err != nil {
			t.Fatalf("build %s: %v", b.name, err)
		}
	}
	// Leave the dev engine clean: remove the derived images and the test base.
	t.Cleanup(func() {
		for _, ref := range builtRefs {
			_, _ = cli.ImageRemove(context.Background(), ref, image.RemoveOptions{Force: true, PruneChildren: true})
		}
		_, _ = cli.ImageRemove(context.Background(), baseTag, image.RemoveOptions{Force: true, PruneChildren: true})
	})

	if got := mirror.debGets("pkg1"); got != 1 {
		t.Errorf("pkg1 .deb fetched %d times, want exactly 1 (second build must reuse the cache mount)", got)
	}
	if got := mirror.debGets("pkg2"); got != 1 {
		t.Errorf("pkg2 .deb fetched %d times, want 1", got)
	}
}

func buildHermeticBase(t *testing.T, cli *client.Client, tag, sourceLine string) {
	dockerfile := fmt.Sprintf(`FROM debian:13-slim
RUN rm -f /etc/apt/sources.list.d/debian.sources /etc/apt/sources.list \
    && echo '%s' > /etc/apt/sources.list.d/tpd-test.list
`, sourceLine)
	rc := mustTarContext(t, dockerfile)
	resp, err := cli.ImageBuild(context.Background(), rc, types.ImageBuildOptions{
		Tags:       []string{tag},
		Dockerfile: "Dockerfile",
		Remove:     true,
	})
	if err != nil {
		t.Fatalf("build hermetic base: %v", err)
	}
	defer resp.Body.Close()
	if err := drainBuildStream(resp.Body, NoopProgressWriter{}); err != nil {
		t.Fatalf("build hermetic base stream: %v", err)
	}
}

func mustTarContext(t *testing.T, dockerfile string) io.Reader {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: int64(len(dockerfile))})
	_, _ = tw.Write([]byte(dockerfile))
	_ = tw.Close()
	return &buf
}
```

Notes on the above:
- The `.deb`/`Packages` artifacts are generated once in `startAptMirror` and served statically, so `Size`/`SHA256` are stable across both builds.
- If the engine reports a build failure due to the ar/gzip layout, adjust `makeDeb` (e.g. `control.tar.xz`) — `dpkg` accepts uncompressed or gzip members; the index must reference the exact bytes served.
- `startAptMirror` binds `0.0.0.0` and the sources line references the host's non-loopback IPv4, so the mirror is reachable from both podman's host-networked builds and Docker's bridged build containers; `isLocalDockerHost` gates the test to local engines.

- [ ] **Step 2: Run the test to verify the cache reuse**

The Task 1-3 code is already in place by this task, so this validates the end-to-end behavior on a real podman host:
Run: `DOCKER_HOST=unix:///run/user/$(id -u)/podman/podman.sock go test ./internal/runtime/ -run TestIntegrationCacheMountsReuse -v`
Expected: PASS — `pkg1 .deb fetched 1 times`, `pkg2 .deb fetched 1 times`.

- [ ] **Step 3: Sanity-check the test detects a broken cache**

To confirm the assertion has teeth (it must fail when reuse is broken), temporarily make `cacheMountIDs` produce a fresh id per call (e.g. append a package-list counter to the returned ids) and re-run Step 2's command:
Expected: FAIL — `pkg1 .deb fetched 2 times`.
Revert the temporary change before continuing. If the build itself errors, read the build stream output and fix the mirror/`.deb` layout (see the notes above).

- [ ] **Step 4: Run the full suite and vet**

Run: `go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/docker_test.go
git commit -m "test(runtime): integration test proving cache-mount deb reuse"
```

---

### Task 6: Documentation

**Files:**
- Modify: `AGENTS.md`

- [ ] **Step 1: Add the derived-build cache-mount note**

In `AGENTS.md`, under the "System deps (`packages:`/`repos:`)" bullet, append:

```
Derived builds add `RUN --mount=type=cache` for `/var/cache/apt` (keyed by
base image) and `/var/lib/apt` (keyed by base + canonical repos), so rebuilds
reuse downloaded `.deb`s; `docker-clean` is neutralized inside the mounted RUN
and `-o APT::Keep-Downloaded-Packages=true` keeps archives. Docker builds send
`version=2` (embedded buildkit); podman's buildah parses cache mounts natively.
Caches are engine-managed (Docker: daemon GC; podman:
`/var/tmp/buildah-cache-<uid>/`, not auto-pruned), so `tpd prune`/`doctor` do
not manage them.
```

- [ ] **Step 2: Verify the project still builds and tests**

Run: `go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add AGENTS.md
git commit -m "docs: note engine-native deb cache mounts for derived builds"
```
