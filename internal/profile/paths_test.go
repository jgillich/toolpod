package profile

import (
	"os"
	"runtime"
	"strconv"
	"testing"

	"github.com/jgillich/tpd/internal/workspace"
)

func TestResolveTildesMountSourceAndTarget(t *testing.T) {
	cfg := Profile{
		Mounts: map[string]Mount{
			"~/.config/opencode": {Source: "~/.config/opencode", ReadOnly: true},
			"/etc/hosts":         {Source: "/etc/hosts", ReadOnly: true},
		},
		Caches: map[string]CachePaths{
			"npm": {"~/.npm"},
		},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootless, "/home/me", "/home/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	m := out.Mounts["/home/me/.config/opencode"]
	if m.Source != "/home/me/.config/opencode" {
		t.Errorf("target-expanded mount source = %q, want /home/me/.config/opencode", m.Source)
	}
	if _, exists := out.Mounts["~/.config/opencode"]; exists {
		t.Error("tilde target key should be replaced with absolute path")
	}
	if got := out.Caches["npm"]; len(got) != 1 || got[0] != "/home/me/.npm" {
		t.Errorf("cache target = %v, want [/home/me/.npm]", got)
	}
	if _, exists := out.Mounts["/etc/hosts"]; !exists {
		t.Error("absolute-path mount should be left as-is")
	}
}

func TestResolveTildesModeB(t *testing.T) {
	cfg := Profile{
		Mounts: map[string]Mount{
			"~/.config/opencode": {Source: "~/.config/opencode", ReadOnly: true},
		},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootful, "/home/me", "/root", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := out.Mounts["/root/.config/opencode"]; !exists {
		t.Error("target should expand to /root/.config/opencode in Mode B")
	}
	m := out.Mounts["/root/.config/opencode"]
	if m.Source != "/home/me/.config/opencode" {
		t.Errorf("source should expand to host home /home/me/.config/opencode, got %q", m.Source)
	}
}

func TestResolveTildesNoHomeSubstitution(t *testing.T) {
	cfg := Profile{
		Mounts: map[string]Mount{
			"/data": {Source: "/data", ReadOnly: false},
		},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootful, "/home/me", "/root", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := out.Mounts["/data"]; !exists {
		t.Error("absolute /data should be unchanged")
	}
}

func TestResolveTildesEnvPassthroughTemplate(t *testing.T) {
	// Forwarding a host variable into the container is explicit: reference it
	// with a template. When the host variable is missing the value resolves
	// to empty (and the runtime leaves the variable unset).
	os.Setenv("TPD_PASSTHROUGH_VAR", "hello")
	t.Cleanup(func() { os.Unsetenv("TPD_PASSTHROUGH_VAR") })

	cfg := Profile{
		Env: map[string]string{
			"PASSTHROUGH": `{{ .Env.TPD_PASSTHROUGH_VAR }}`,
			"MISSING":     `{{ .Env.TPD_PASSTHROUGH_MISSING }}`,
		},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootful, "/home/me", "/root", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Env["PASSTHROUGH"] != "hello" {
		t.Errorf("PASSTHROUGH = %q, want hello", out.Env["PASSTHROUGH"])
	}
	if out.Env["MISSING"] != "" {
		t.Errorf("MISSING = %q, want \"\" (host var missing)", out.Env["MISSING"])
	}
}

func TestResolveTildesTemplateExpansion(t *testing.T) {
	os.Setenv("TPD_TEST_SOCK", "/run/user/1000/podman/podman.sock")
	t.Cleanup(func() { os.Unsetenv("TPD_TEST_SOCK") })

	cfg := Profile{
		Mounts: map[string]Mount{
			"/var/run/docker.sock": {Source: `{{ or (index .Env "TPD_TEST_SOCK") "/var/run/docker.sock" }}`, Optional: true},
		},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootful, "/home/me", "/root", nil)
	if err != nil {
		t.Fatal(err)
	}
	m := out.Mounts["/var/run/docker.sock"]
	if m.Source != "/run/user/1000/podman/podman.sock" {
		t.Errorf("template-expanded source = %q, want /run/user/1000/podman/podman.sock", m.Source)
	}
}

func TestResolveTildesTemplateFallback(t *testing.T) {
	os.Unsetenv("TPD_UNSET_VAR")
	cfg := Profile{
		Mounts: map[string]Mount{
			"/var/run/docker.sock": {Source: `{{ or (index .Env "TPD_UNSET_VAR") "/var/run/docker.sock" }}`, Optional: true},
		},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootful, "/home/me", "/root", nil)
	if err != nil {
		t.Fatal(err)
	}
	m := out.Mounts["/var/run/docker.sock"]
	if m.Source != "/var/run/docker.sock" {
		t.Errorf("fallback source = %q, want /var/run/docker.sock", m.Source)
	}
}

func TestResolveTildesNoDelimitersPassThrough(t *testing.T) {
	cfg := Profile{
		Mounts: map[string]Mount{
			"/data": {Source: "/data", ReadOnly: false},
		},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootful, "/home/me", "/root", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Mounts["/data"].Source != "/data" {
		t.Errorf("plain path = %q, want /data", out.Mounts["/data"].Source)
	}
}

func TestResolveTildesTrimPrefix(t *testing.T) {
	os.Setenv("DOCKER_HOST", "unix:///run/user/1000/podman/podman.sock")
	t.Cleanup(func() { os.Unsetenv("DOCKER_HOST") })

	cfg := Profile{
		Mounts: map[string]Mount{
			"/var/run/docker.sock": {
				Source:   `{{ or (trimPrefix (index .Env "DOCKER_HOST") "unix://") "/run/user/1000/podman/podman.sock" }}`,
				Optional: true,
			},
		},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootful, "/home/me", "/root", nil)
	if err != nil {
		t.Fatal(err)
	}
	m := out.Mounts["/var/run/docker.sock"]
	want := "/run/user/1000/podman/podman.sock"
	if m.Source != want {
		t.Errorf("trimPrefix source = %q, want %q", m.Source, want)
	}
}

func TestResolveTildesTrimPrefixFallback(t *testing.T) {
	os.Unsetenv("DOCKER_HOST")
	cfg := Profile{
		Mounts: map[string]Mount{
			"/var/run/docker.sock": {
				Source:   `{{ or (trimPrefix (index .Env "DOCKER_HOST") "unix://") (printf "/run/user/%s/podman/podman.sock" (uid)) }}`,
				Optional: true,
			},
		},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootful, "/home/me", "/root", nil)
	if err != nil {
		t.Fatal(err)
	}
	m := out.Mounts["/var/run/docker.sock"]
	want := "/run/user/" + strconv.Itoa(os.Getuid()) + "/podman/podman.sock"
	if m.Source != want {
		t.Errorf("fallback source = %q, want %q", m.Source, want)
	}
}

func TestResolveTildesEmbeddedEnvTemplate(t *testing.T) {
	os.Setenv("TPD_TEST_HOME", "/home/me")
	t.Cleanup(func() { os.Unsetenv("TPD_TEST_HOME") })

	cfg := Profile{
		Env: map[string]string{
			"PATH": `{{ .Env.TPD_TEST_HOME }}/bin`,
		},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootful, "/home/me", "/root", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Env["PATH"] != "/home/me/bin" {
		t.Errorf("PATH = %q, want /home/me/bin (embedded template rendered mid-string)", out.Env["PATH"])
	}
}

func TestResolveTildesServiceEmbeddedEnvTemplate(t *testing.T) {
	os.Setenv("TPD_TEST_SVC_PORT", "5000")
	t.Cleanup(func() { os.Unsetenv("TPD_TEST_SVC_PORT") })

	cfg := Profile{
		Services: map[string]Service{
			"registry": {
				Image:   "debian:13-slim",
				Command: []string{"registry"},
				Env:     map[string]string{"ADDR": `127.0.0.1:{{ .Env.TPD_TEST_SVC_PORT }}`},
			},
		},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootless, "/home/me", "/home/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Services["registry"].Env["ADDR"]; got != "127.0.0.1:5000" {
		t.Errorf("service env ADDR = %q, want 127.0.0.1:5000", got)
	}
}

func TestResolveTildesEmbeddedCommandTemplate(t *testing.T) {
	os.Setenv("TPD_TEST_PORT", "5173")
	t.Cleanup(func() { os.Unsetenv("TPD_TEST_PORT") })

	cfg := Profile{
		Command: []string{"sh", "-c", `PORT={{ .Env.TPD_TEST_PORT }} serve`},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootful, "/home/me", "/root", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Command[2] != "PORT=5173 serve" {
		t.Errorf("command arg = %q, want %q", out.Command[2], "PORT=5173 serve")
	}
}

func TestResolveTildesEnvTemplateRenderedTildeNotExpanded(t *testing.T) {
	os.Setenv("TPD_TILDE_ENV", "~/data")
	t.Cleanup(func() { os.Unsetenv("TPD_TILDE_ENV") })

	cfg := Profile{
		Env:     map[string]string{"DATA_DIR": `{{ .Env.TPD_TILDE_ENV }}`},
		Command: []string{"run", `{{ .Env.TPD_TILDE_ENV }}`},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootful, "/home/me", "/root", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Env values and command args are template-rendered but not path-resolved,
	// so a rendered ~/ is left verbatim rather than expanded.
	if out.Env["DATA_DIR"] != "~/data" {
		t.Errorf("DATA_DIR = %q, want ~/data (env values are not tilde-expanded)", out.Env["DATA_DIR"])
	}
	if out.Command[1] != "~/data" {
		t.Errorf("command arg = %q, want ~/data (args are not tilde-expanded)", out.Command[1])
	}
}

func TestResolveTildesPortsInEnvironment(t *testing.T) {
	cfg := Profile{
		Env: map[string]string{
			"PORT":  `{{ index .Ports "8080" }}`,
			"PLAIN": "8080",
			"EMPTY": "",
		},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootful, "/home/me", "/root", map[string]string{"8080": "39483"})
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

func TestResolveTildesEmptyMountSourceErrorsWhenRequired(t *testing.T) {
	os.Unsetenv("TPD_UNSET_VAR")
	cfg := Profile{
		Mounts: map[string]Mount{
			"/data": {Source: `{{ or (index .Env "TPD_UNSET_VAR") "" }}`},
		},
	}
	if _, err := ResolveTildes(cfg, workspace.ModeRootful, "/home/me", "/root", nil); err == nil {
		t.Fatal("non-optional mount with empty rendered source should error, got nil")
	}
}

func TestResolveTildesEmptyMountSourceSkippedWhenOptional(t *testing.T) {
	os.Unsetenv("TPD_UNSET_VAR")
	cfg := Profile{
		Mounts: map[string]Mount{
			"/data": {Source: `{{ or (index .Env "TPD_UNSET_VAR") "" }}`, Optional: true},
		},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootful, "/home/me", "/root", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := out.Mounts["/data"]; exists {
		t.Error("optional mount with empty source should be dropped, not kept")
	}
}

// guardedWaylandMount is the gui fragment's wayland-socket mount. The bare
// {{ .Env.XDG_RUNTIME_DIR }}/{{ .Env.WAYLAND_DISPLAY }} form is a footgun: a
// missing variable leaves a dangling "/" (or "/" itself when both are unset),
// which exists and would bind the host root. The {{ if and ... }} guard
// renders empty when either variable is missing, and ResolveTildes drops
// empty optional mounts.
const guardedWaylandMount = `{{ if and .Env.XDG_RUNTIME_DIR .Env.WAYLAND_DISPLAY }}{{ .Env.XDG_RUNTIME_DIR }}/{{ .Env.WAYLAND_DISPLAY }}{{ end }}`

func TestResolveTildesGuardedWaylandMount(t *testing.T) {
	cases := []struct {
		name       string
		runtimeDir string
		display    string
		want       string
	}{
		{"both unset", "", "", ""},
		{"both set", "/run/user/1000", "wayland-1", "/run/user/1000/wayland-1"},
		{"display set, runtime dir unset", "", "wayland-1", ""},
		{"runtime dir set, display unset", "/run/user/1000", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_RUNTIME_DIR", tc.runtimeDir)
			t.Setenv("WAYLAND_DISPLAY", tc.display)
			cfg := Profile{
				Mounts: map[string]Mount{
					guardedWaylandMount: {Source: guardedWaylandMount, Optional: true},
				},
			}
			out, err := ResolveTildes(cfg, workspace.ModeRootless, "/home/me", "/home/me", nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(out.Mounts) != 0 {
					t.Errorf("wayland mount should be dropped when a variable is unset, got %v", out.Mounts)
				}
				return
			}
			m, exists := out.Mounts[tc.want]
			if !exists {
				t.Fatalf("mount at %q missing; got %v", tc.want, out.Mounts)
			}
			if m.Source != tc.want {
				t.Errorf("source = %q, want %q", m.Source, tc.want)
			}
		})
	}
}

func TestResolveTildesEmptyCacheTargetErrors(t *testing.T) {
	os.Unsetenv("TPD_UNSET_VAR")
	cfg := Profile{
		Caches: map[string]CachePaths{"npm": {`{{ or (index .Env "TPD_UNSET_VAR") "" }}`}},
	}
	if _, err := ResolveTildes(cfg, workspace.ModeRootful, "/home/me", "/root", nil); err == nil {
		t.Fatal("cache with empty rendered target should error, got nil")
	}
}

func TestResolveTildesPortsInCommand(t *testing.T) {
	cfg := Profile{
		Command: []string{"opencode", "web", "--port", `{{ index .Ports "8080" }}`},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootful, "/home/me", "/root", map[string]string{"8080": "39483"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Command[3] != "39483" {
		t.Errorf("command arg = %q, want 39483", out.Command[3])
	}
}

func TestResolveTildesLiteralBracePassthrough(t *testing.T) {
	cfg := Profile{
		Command: []string{"sh", "-c", "echo {x} {y}"},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootful, "/home/me", "/root", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Command[2] != "echo {x} {y}" {
		t.Errorf("single braces must pass through untouched, got %q", out.Command[2])
	}
}

func TestResolveTildesMissingPortKeyRendersEmpty(t *testing.T) {
	cfg := Profile{
		Env: map[string]string{"PORT": `{{ index .Ports "9999" }}`},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootful, "/home/me", "/root", map[string]string{"8080": "39483"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Env["PORT"] != "" {
		t.Errorf("PORT = %q, want \"\" (Go template index on missing map key yields zero value, no error)", out.Env["PORT"])
	}
}

func TestResolveFilesTildeAndTemplate(t *testing.T) {
	cfg := Profile{
		Files: map[string]File{
			"~/.config/foo": {
				Content: "port={{ index .Ports \"8080\" }} uid={{ uid }}",
			},
			"~/.config/bar": {Content: "plain"},
		},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootless, "/home/me", "/home/me", map[string]string{"8080": "5173"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Files["/home/me/.config/foo"]; !ok {
		t.Fatalf("~ target should expand to runtimeHome, got %v", out.Files)
	}
	got := out.Files["/home/me/.config/foo"].Content
	want := "port=5173 uid=" + currentUID()
	if got != want {
		t.Errorf("content = %q, want %q (template rendered)", got, want)
	}
	if out.Files["/home/me/.config/bar"].Content != "plain" {
		t.Errorf("plain content must pass through unchanged, got %q", out.Files["/home/me/.config/bar"].Content)
	}
}

func TestResolveFilesEmptyRenderedTargetRejected(t *testing.T) {
	cfg := Profile{
		Files: map[string]File{
			"{{ .Env.MISSING_VAR }}": {Content: "x"},
		},
	}
	_, err := ResolveTildes(cfg, workspace.ModeRootless, "/home/me", "/home/me", nil)
	if err == nil {
		t.Fatal("expected error for file target that renders empty, got nil")
	}
}

func TestResolveFilesTraversalAfterExpansionRejected(t *testing.T) {
	os.Setenv("TPD_TEST_TRAVERSAL", "/..")
	t.Cleanup(func() { os.Unsetenv("TPD_TEST_TRAVERSAL") })

	cfg := Profile{
		Files: map[string]File{
			"{{ .Env.TPD_TEST_TRAVERSAL }}/etc/passwd": {Content: "x"},
		},
	}
	_, err := ResolveTildes(cfg, workspace.ModeRootless, "/home/me", "/home/me", nil)
	if err == nil {
		t.Fatal("expected error for file target expanding to a '..' path, got nil")
	}
}

func TestResolveTildesUnknownModeKeepsTildeTargetsLiteral(t *testing.T) {
	cfg := Profile{
		Mounts: map[string]Mount{
			"~/.config/opencode": {Source: "~/.config/opencode", ReadOnly: true},
		},
		Caches: map[string]CachePaths{
			"npm": {"~/.npm"},
		},
		Files: map[string]File{
			"~/bin/run": {Content: "x"},
		},
	}
	out, err := ResolveTildes(cfg, workspace.ModeUnknown, "/home/me", "/home/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Unknown mode (dry-run without a daemon) must not claim a home for
	// in-container targets; ~/ stays literal. Sources are host paths and still
	// expand against the host home.
	if m, ok := out.Mounts["~/.config/opencode"]; !ok {
		t.Errorf("mount target should stay literal in unknown mode, got %v", out.Mounts)
	} else if m.Source != "/home/me/.config/opencode" {
		t.Errorf("mount source = %q, want /home/me/.config/opencode", m.Source)
	}
	if got := out.Caches["npm"]; len(got) != 1 || got[0] != "~/.npm" {
		t.Errorf("cache target should stay literal in unknown mode, got %v", got)
	}
	if _, ok := out.Files["~/bin/run"]; !ok {
		t.Errorf("file target should stay literal in unknown mode, got %v", out.Files)
	}
}

func TestResolveTildesServices(t *testing.T) {
	cfg := Profile{
		Services: map[string]Service{
			"registry": {
				Image:   "debian:13-slim",
				Command: []string{"registry"},
				Env:     map[string]string{"DATA_DIR": "~/data"},
				Caches: map[string]CachePaths{
					"data": {"~/cache"},
				},
				Mounts: map[string]Mount{
					"/config": {Source: "~/.config/registry", ReadOnly: true},
				},
			},
		},
	}
	cfg, err := ResolveTildes(cfg, workspace.ModeRootless, "/home/user", "/home/user", nil)
	if err != nil {
		t.Fatalf("ResolveTildes: %v", err)
	}
	svc := cfg.Services["registry"]
	// Env values are template-rendered only; ~/data is NOT tilde-expanded
	// (same as the main profile's env, which only renders {{ }} templates).
	if svc.Env["DATA_DIR"] != "~/data" {
		t.Errorf("service env DATA_DIR = %q, want ~/data (env values are not tilde-expanded)", svc.Env["DATA_DIR"])
	}
	// Cache paths are in-container paths; ~ expands against /root (service home).
	if svc.Caches["data"][0] != "/root/cache" {
		t.Errorf("service cache = %q, want /root/cache (in-container paths expand against /root)", svc.Caches["data"][0])
	}
	// Mount sources are host paths; ~ expands against hostHome.
	if svc.Mounts["/config"].Source != "/home/user/.config/registry" {
		t.Errorf("service mount source = %q, want /home/user/.config/registry (host path expands against hostHome)", svc.Mounts["/config"].Source)
	}
}

func TestResolveTildesServiceSocketMountSkipped(t *testing.T) {
	cfg := Profile{
		Services: map[string]Service{
			"registry": {
				Image:   "debian:13-slim",
				Command: []string{"registry"},
				Exposes: map[string]string{"registry": "/run/registry/registry.sock"},
			},
		},
		Mounts: map[string]Mount{
			"/sock": {Service: "registry", Socket: "registry"},
		},
	}
	cfg, err := ResolveTildes(cfg, workspace.ModeRootless, "/home/user", "/home/user", nil)
	if err != nil {
		t.Fatalf("ResolveTildes: %v (service-socket mount should be skipped, not fail on empty Source)", err)
	}
	m := cfg.Mounts["/sock"]
	if m.Service == "" {
		t.Error("service-socket mount should survive ResolveTildes with Service intact")
	}
}

func TestResolveTildesServiceFilesRejectDotDot(t *testing.T) {
	cfg := Profile{
		Services: map[string]Service{
			"registry": {
				Image:   "debian:13-slim",
				Command: []string{"registry"},
				Files: map[string]File{
					"/etc/../etc/passwd": {Content: "x"},
				},
			},
		},
	}
	_, err := ResolveTildes(cfg, workspace.ModeRootless, "/home/user", "/home/user", nil)
	if err == nil {
		t.Fatal("expected error for '..' in service file target")
	}
}

func TestResolveTildesTemplateRenderedTildeExpanded(t *testing.T) {
	os.Setenv("TPD_TILDE_PATH", "~/.config/foo")
	t.Cleanup(func() { os.Unsetenv("TPD_TILDE_PATH") })

	cfg := Profile{
		Mounts: map[string]Mount{
			`{{ .Env.TPD_TILDE_PATH }}`: {Source: `{{ .Env.TPD_TILDE_PATH }}`},
		},
		Caches: map[string]CachePaths{
			"c": {`{{ .Env.TPD_TILDE_PATH }}`},
		},
		Files: map[string]File{
			`{{ .Env.TPD_TILDE_PATH }}`: {Content: "x"},
		},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootless, "/home/me", "/home/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Mounts["/home/me/.config/foo"]; !ok {
		t.Errorf("template-rendered ~ target should expand to /home/me/.config/foo, got %v", out.Mounts)
	}
	if out.Mounts["/home/me/.config/foo"].Source != "/home/me/.config/foo" {
		t.Errorf("template-rendered ~ source = %q, want /home/me/.config/foo", out.Mounts["/home/me/.config/foo"].Source)
	}
	if got := out.Caches["c"]; len(got) != 1 || got[0] != "/home/me/.config/foo" {
		t.Errorf("template-rendered ~ cache = %v, want [/home/me/.config/foo]", got)
	}
	if _, ok := out.Files["/home/me/.config/foo"]; !ok {
		t.Errorf("template-rendered ~ file target should expand to /home/me/.config/foo, got %v", out.Files)
	}
}

func TestResolveTildesTemplateRenderedTildeServicePaths(t *testing.T) {
	os.Setenv("TPD_TILDE_PATH", "~/cache")
	t.Cleanup(func() { os.Unsetenv("TPD_TILDE_PATH") })

	cfg := Profile{
		Services: map[string]Service{
			"registry": {
				Image:   "debian:13-slim",
				Command: []string{"registry"},
				Caches: map[string]CachePaths{
					"c": {`{{ .Env.TPD_TILDE_PATH }}`},
				},
				Mounts: map[string]Mount{
					`{{ .Env.TPD_TILDE_PATH }}`: {Source: `{{ .Env.TPD_TILDE_PATH }}`},
				},
				Files: map[string]File{
					`{{ .Env.TPD_TILDE_PATH }}/conf`: {Content: "x"},
				},
			},
		},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootless, "/home/me", "/home/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := out.Services["registry"]
	// Service in-container paths expand ~ against /root.
	if got := svc.Caches["c"]; len(got) != 1 || got[0] != "/root/cache" {
		t.Errorf("service cache = %v, want [/root/cache]", got)
	}
	if m, ok := svc.Mounts["/root/cache"]; !ok || m.Source != "/home/me/cache" {
		t.Errorf("service mount = %+v, want target /root/cache with source /home/me/cache", svc.Mounts)
	}
	if _, ok := svc.Files["/root/cache/conf"]; !ok {
		t.Errorf("service file target should expand to /root/cache/conf, got %v", svc.Files)
	}
}

func TestResolveTildesMountTraversalAfterRenderRejected(t *testing.T) {
	os.Setenv("TPD_TRAVERSAL", "/..")
	t.Cleanup(func() { os.Unsetenv("TPD_TRAVERSAL") })

	cfg := Profile{
		Mounts: map[string]Mount{
			`{{ .Env.TPD_TRAVERSAL }}/etc`: {Source: "/tmp"},
		},
	}
	if _, err := ResolveTildes(cfg, workspace.ModeRootless, "/home/me", "/home/me", nil); err == nil {
		t.Fatal("expected error for mount target expanding to a '..' path, got nil")
	}
}

func TestResolveTildesMountSourceTraversalAfterRenderRejected(t *testing.T) {
	os.Setenv("TPD_TRAVERSAL", "/..")
	t.Cleanup(func() { os.Unsetenv("TPD_TRAVERSAL") })

	cfg := Profile{
		Mounts: map[string]Mount{
			"/etc": {Source: `{{ .Env.TPD_TRAVERSAL }}/etc`},
		},
	}
	if _, err := ResolveTildes(cfg, workspace.ModeRootless, "/home/me", "/home/me", nil); err == nil {
		t.Fatal("expected error for mount source expanding to a '..' path, got nil")
	}
}

func TestResolveTildesCacheTraversalAfterRenderRejected(t *testing.T) {
	os.Setenv("TPD_TRAVERSAL", "/..")
	t.Cleanup(func() { os.Unsetenv("TPD_TRAVERSAL") })

	cfg := Profile{
		Caches: map[string]CachePaths{"c": {`{{ .Env.TPD_TRAVERSAL }}/npm`}},
	}
	if _, err := ResolveTildes(cfg, workspace.ModeRootless, "/home/me", "/home/me", nil); err == nil {
		t.Fatal("expected error for cache target expanding to a '..' path, got nil")
	}
}

func TestResolveTildesServiceMountTraversalAfterRenderRejected(t *testing.T) {
	os.Setenv("TPD_TRAVERSAL", "/..")
	t.Cleanup(func() { os.Unsetenv("TPD_TRAVERSAL") })

	cfg := Profile{
		Services: map[string]Service{
			"registry": {
				Image:   "debian:13-slim",
				Command: []string{"registry"},
				Mounts: map[string]Mount{
					`{{ .Env.TPD_TRAVERSAL }}/etc`: {Source: "/tmp"},
				},
			},
		},
	}
	if _, err := ResolveTildes(cfg, workspace.ModeRootless, "/home/me", "/home/me", nil); err == nil {
		t.Fatal("expected error for service mount target expanding to a '..' path, got nil")
	}
}

func TestResolveTildesRelativeAfterRenderRejected(t *testing.T) {
	os.Setenv("TPD_RELATIVE", "relative/path")
	t.Cleanup(func() { os.Unsetenv("TPD_RELATIVE") })

	cfg := Profile{
		Mounts: map[string]Mount{
			`{{ .Env.TPD_RELATIVE }}`: {Source: "/tmp"},
		},
	}
	if _, err := ResolveTildes(cfg, workspace.ModeRootless, "/home/me", "/home/me", nil); err == nil {
		t.Fatal("expected error for mount target rendering a relative path, got nil")
	}
}

func TestResolveTildesServiceExposesRendered(t *testing.T) {
	os.Setenv("TPD_SOCK_DIR", "/run/app")
	t.Cleanup(func() { os.Unsetenv("TPD_SOCK_DIR") })

	cfg := Profile{
		Services: map[string]Service{
			"registry": {
				Image:   "debian:13-slim",
				Command: []string{"registry"},
				Exposes: map[string]string{"registry": `{{ .Env.TPD_SOCK_DIR }}/db.sock`},
			},
		},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootless, "/home/me", "/home/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Services["registry"].Exposes["registry"]; got != "/run/app/db.sock" {
		t.Errorf("rendered expose path = %q, want /run/app/db.sock", got)
	}
}

func TestResolveTildesServiceExposeRootParentAfterRenderRejected(t *testing.T) {
	os.Setenv("TPD_SOCK_DIR", "/")
	t.Cleanup(func() { os.Unsetenv("TPD_SOCK_DIR") })

	cfg := Profile{
		Services: map[string]Service{
			"registry": {
				Image:   "debian:13-slim",
				Command: []string{"registry"},
				Exposes: map[string]string{"registry": `{{ .Env.TPD_SOCK_DIR }}db.sock`},
			},
		},
	}
	if _, err := ResolveTildes(cfg, workspace.ModeRootless, "/home/me", "/home/me", nil); err == nil {
		t.Fatal("expected error for expose path resolving into the root directory, got nil")
	}
}

func TestResolveTildesServiceExposeTraversalAfterRenderRejected(t *testing.T) {
	os.Setenv("TPD_SOCK_DIR", "/run/..")
	t.Cleanup(func() { os.Unsetenv("TPD_SOCK_DIR") })

	cfg := Profile{
		Services: map[string]Service{
			"registry": {
				Image:   "debian:13-slim",
				Command: []string{"registry"},
				Exposes: map[string]string{"registry": `{{ .Env.TPD_SOCK_DIR }}/db.sock`},
			},
		},
	}
	if _, err := ResolveTildes(cfg, workspace.ModeRootless, "/home/me", "/home/me", nil); err == nil {
		t.Fatal("expected error for expose path expanding to a '..' path, got nil")
	}
}

func TestResolveTildesServiceExposeRelativeAfterRenderRejected(t *testing.T) {
	os.Setenv("TPD_SOCK_DIR", "run")
	t.Cleanup(func() { os.Unsetenv("TPD_SOCK_DIR") })

	cfg := Profile{
		Services: map[string]Service{
			"registry": {
				Image:   "debian:13-slim",
				Command: []string{"registry"},
				Exposes: map[string]string{"registry": `{{ .Env.TPD_SOCK_DIR }}/db.sock`},
			},
		},
	}
	if _, err := ResolveTildes(cfg, workspace.ModeRootless, "/home/me", "/home/me", nil); err == nil {
		t.Fatal("expected error for expose path rendering a relative path, got nil")
	}
}

func TestResolveTildesRendersResources(t *testing.T) {
	cfg := Profile{
		Resources: &Resources{
			Memory: "{{ div .MemBytes 2 }}",
			CPUs:   "{{ .NumCPU }}",
		},
	}
	out, err := ResolveTildes(cfg, workspace.ModeRootless, "/home/me", "/home/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Resources == nil {
		t.Fatal("Resources should survive resolve")
	}
	mem, err := ParseMemoryBytes(out.Resources.Memory)
	if err != nil {
		t.Fatalf("rendered memory %q unparseable: %v", out.Resources.Memory, err)
	}
	if want := hostMemBytes() / 2; mem != want {
		t.Errorf("rendered memory = %d, want %d (half of host)", mem, want)
	}
	if want := strconv.Itoa(runtime.NumCPU()); out.Resources.CPUs != want {
		t.Errorf("rendered cpus = %q, want %q", out.Resources.CPUs, want)
	}
}

func TestDiv(t *testing.T) {
	if got := div(9, 2); got != 4 {
		t.Errorf("div(9, 2) = %d, want 4", got)
	}
	if got := div(10, 0); got != 0 {
		t.Errorf("div(10, 0) = %d, want 0 (zero divisor must not panic)", got)
	}
}
