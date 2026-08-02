package profile

import (
	"os"
	"strconv"
	"testing"
)

func TestResolveTildesMountSourceAndTarget(t *testing.T) {
	cfg := Profile{
		Mounts: map[string]Mount{
			"~/.config/opencode": {Source: "~/.config/opencode", ReadOnly: true},
			"/etc/hosts":         {Source: "/etc/hosts", ReadOnly: true},
		},
		Caches: map[string]string{
			"npm": "~/.npm",
		},
	}
	out, err := ResolveTildes(cfg, "A", "/home/me", "/home/me", nil)
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
	if out.Caches["npm"] != "/home/me/.npm" {
		t.Errorf("cache target = %q, want /home/me/.npm", out.Caches["npm"])
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
	out, err := ResolveTildes(cfg, "B", "/home/me", "/root", nil)
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
	out, err := ResolveTildes(cfg, "B", "/home/me", "/root", nil)
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
	os.Setenv("TPOD_PASSTHROUGH_VAR", "hello")
	t.Cleanup(func() { os.Unsetenv("TPOD_PASSTHROUGH_VAR") })

	cfg := Profile{
		Env: map[string]string{
			"PASSTHROUGH": `{{ .Env.TPOD_PASSTHROUGH_VAR }}`,
			"MISSING":     `{{ .Env.TPOD_PASSTHROUGH_MISSING }}`,
		},
	}
	out, err := ResolveTildes(cfg, "B", "/home/me", "/root", nil)
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
	os.Setenv("TPOD_TEST_SOCK", "/run/user/1000/podman/podman.sock")
	t.Cleanup(func() { os.Unsetenv("TPOD_TEST_SOCK") })

	cfg := Profile{
		Mounts: map[string]Mount{
			"/var/run/docker.sock": {Source: `{{ or (index .Env "TPOD_TEST_SOCK") "/var/run/docker.sock" }}`, Optional: true},
		},
	}
	out, err := ResolveTildes(cfg, "B", "/home/me", "/root", nil)
	if err != nil {
		t.Fatal(err)
	}
	m := out.Mounts["/var/run/docker.sock"]
	if m.Source != "/run/user/1000/podman/podman.sock" {
		t.Errorf("template-expanded source = %q, want /run/user/1000/podman/podman.sock", m.Source)
	}
}

func TestResolveTildesTemplateFallback(t *testing.T) {
	os.Unsetenv("TPOD_UNSET_VAR")
	cfg := Profile{
		Mounts: map[string]Mount{
			"/var/run/docker.sock": {Source: `{{ or (index .Env "TPOD_UNSET_VAR") "/var/run/docker.sock" }}`, Optional: true},
		},
	}
	out, err := ResolveTildes(cfg, "B", "/home/me", "/root", nil)
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
	out, err := ResolveTildes(cfg, "B", "/home/me", "/root", nil)
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
	out, err := ResolveTildes(cfg, "B", "/home/me", "/root", nil)
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
	out, err := ResolveTildes(cfg, "B", "/home/me", "/root", nil)
	if err != nil {
		t.Fatal(err)
	}
	m := out.Mounts["/var/run/docker.sock"]
	want := "/run/user/" + strconv.Itoa(os.Getuid()) + "/podman/podman.sock"
	if m.Source != want {
		t.Errorf("fallback source = %q, want %q", m.Source, want)
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

func TestResolveTildesEmptyMountSourceErrorsWhenRequired(t *testing.T) {
	os.Unsetenv("TPOD_UNSET_VAR")
	cfg := Profile{
		Mounts: map[string]Mount{
			"/data": {Source: `{{ or (index .Env "TPOD_UNSET_VAR") "" }}`},
		},
	}
	if _, err := ResolveTildes(cfg, "B", "/home/me", "/root", nil); err == nil {
		t.Fatal("non-optional mount with empty rendered source should error, got nil")
	}
}

func TestResolveTildesEmptyMountSourceSkippedWhenOptional(t *testing.T) {
	os.Unsetenv("TPOD_UNSET_VAR")
	cfg := Profile{
		Mounts: map[string]Mount{
			"/data": {Source: `{{ or (index .Env "TPOD_UNSET_VAR") "" }}`, Optional: true},
		},
	}
	out, err := ResolveTildes(cfg, "B", "/home/me", "/root", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := out.Mounts["/data"]; exists {
		t.Error("optional mount with empty source should be dropped, not kept")
	}
}

func TestResolveTildesEmptyCacheTargetErrors(t *testing.T) {
	os.Unsetenv("TPOD_UNSET_VAR")
	cfg := Profile{
		Caches: map[string]string{"npm": `{{ or (index .Env "TPOD_UNSET_VAR") "" }}`},
	}
	if _, err := ResolveTildes(cfg, "B", "/home/me", "/root", nil); err == nil {
		t.Fatal("cache with empty rendered target should error, got nil")
	}
}

func TestResolveTildesPortsInCommand(t *testing.T) {
	cfg := Profile{
		Command: []string{"opencode", "web", "--port", `{{ index .Ports "8080" }}`},
	}
	out, err := ResolveTildes(cfg, "B", "/home/me", "/root", map[string]string{"8080": "39483"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Command[3] != "39483" {
		t.Errorf("command arg = %q, want 39483", out.Command[3])
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

func TestResolveTildesMissingPortKeyRendersEmpty(t *testing.T) {
	cfg := Profile{
		Env: map[string]string{"PORT": `{{ index .Ports "9999" }}`},
	}
	out, err := ResolveTildes(cfg, "B", "/home/me", "/root", map[string]string{"8080": "39483"})
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
	out, err := ResolveTildes(cfg, "A", "/home/me", "/home/me", map[string]string{"8080": "5173"})
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
	_, err := ResolveTildes(cfg, "A", "/home/me", "/home/me", nil)
	if err == nil {
		t.Fatal("expected error for file target that renders empty, got nil")
	}
}

func TestResolveFilesTraversalAfterExpansionRejected(t *testing.T) {
	os.Setenv("TPOD_TEST_TRAVERSAL", "/..")
	t.Cleanup(func() { os.Unsetenv("TPOD_TEST_TRAVERSAL") })

	cfg := Profile{
		Files: map[string]File{
			"{{ .Env.TPOD_TEST_TRAVERSAL }}/etc/passwd": {Content: "x"},
		},
	}
	_, err := ResolveTildes(cfg, "A", "/home/me", "/home/me", nil)
	if err == nil {
		t.Fatal("expected error for file target expanding to a '..' path, got nil")
	}
}
