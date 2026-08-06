package profile

import (
	"strings"
	"testing"
)

func TestValidateMissingVersion(t *testing.T) {
	rc := RawProfile{Profile: Profile{Image: "x", Command: []string{"sh"}}}
	err := validate(rc)
	if err == nil {
		t.Fatal("expected error for missing version")
	}
	if _, ok := err.(ProfileError); !ok {
		t.Fatalf("expected ProfileError, got %T", err)
	}
}

func TestValidateMissingCommand(t *testing.T) {
	rc := RawProfile{Profile: Profile{Version: 1, Image: "x"}}
	err := validate(rc)
	if err == nil {
		t.Fatal("expected error for missing command")
	}
}

func TestValidateMissingImage(t *testing.T) {
	rc := RawProfile{Profile: Profile{Version: 1, Command: []string{"sh"}}}
	err := validate(rc)
	if err == nil {
		t.Fatal("expected error for missing image")
	}
}

func TestValidateReservedName(t *testing.T) {
	for _, name := range []string{"config", "doctor", "help", "version", "completion", "prune", "init"} {
		rc := RawProfile{Profile: Profile{Version: 1, Image: "x", Command: []string{"sh"}}}
		rc.Path = "/home/me/.config/tpd/" + name + ".yaml"
		err := validateReservedName(rc, name)
		if err == nil {
			t.Errorf("expected reserved-name error for %q", name)
		}
	}
}

func TestValidateValid(t *testing.T) {
	rc := RawProfile{Profile: Profile{Version: 1, Image: "x", Command: []string{"sh"}}}
	if err := validate(rc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateImage(t *testing.T) {
	base := Profile{Version: 1, Command: []string{"sh"}}
	valid := []string{"debian", "debian:13-slim", "docker.io/library/debian:13", "ghcr.io/org/repo:v1"}
	for _, img := range valid {
		rc := RawProfile{Profile: base}
		rc.Image = img
		if err := validate(rc); err != nil {
			t.Errorf("validate(image=%q) = %v, want nil", img, err)
		}
	}
	invalid := []string{"debian:13-slim\nRUN id", "../evil", "debian\x00x", "debian:13 slim"}
	for _, img := range invalid {
		rc := RawProfile{Profile: base}
		rc.Image = img
		if err := validate(rc); err == nil {
			t.Errorf("validate(image=%q) = nil, want error", img)
		}
	}
}

func TestValidateName(t *testing.T) {
	valid := []string{"foo", "my-agent", "opencode", "a/b", "postgres-main", "alpha"}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}
	invalid := []string{"", "config", "doctor", "help", "version", "completion", "prune", "init", "../x", `a\b`, "a b", "a..b", "a.b", "x_y", "lang/x_y"}
	for _, name := range invalid {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", name)
		}
	}
	if err := ValidateName("lang/go"); err != nil {
		t.Errorf("ValidateName(lang/go) = %v, want nil", err)
	}
	if err := ValidateName("core/go"); err == nil {
		t.Error("ValidateName(core/go) = nil, want reserved-namespace error")
	}
	if err := ValidateName("core"); err != nil {
		t.Errorf("ValidateName(core) = %v, want nil (a bare core profile name is allowed)", err)
	}
	if err := ValidateName("lang/foo bar"); err == nil {
		t.Error("ValidateName(lang/foo bar) = nil, want invalid-segment error")
	}
	if err := ValidateName("lang/.."); err == nil {
		t.Error("ValidateName(lang/..) = nil, want '..' error")
	}
}

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

func TestValidatePackages(t *testing.T) {
	base := Profile{Version: 1, Image: "x", Command: []string{"sh"}}
	valid := []string{"libxml2-dev", "gstreamer1.0-plugins-bad", "zlib1g-dev", "libpq-dev"}
	for _, pkg := range valid {
		rc := RawProfile{Profile: base}
		rc.Packages = []string{pkg}
		if err := validate(rc); err != nil {
			t.Errorf("validate(packages=%q) = %v, want nil", pkg, err)
		}
	}
	invalid := []string{"lib xml2", "libxml2-dev=2.12", "libxml2;rm -rf /", "", "Libxml2", "libxml2-dev!"}
	for _, pkg := range invalid {
		rc := RawProfile{Profile: base}
		rc.Packages = []string{pkg}
		if err := validate(rc); err == nil {
			t.Errorf("validate(packages=%q) = nil, want error", pkg)
		}
	}
}

func TestValidateRepos(t *testing.T) {
	base := Profile{Version: 1, Image: "x", Command: []string{"sh"}}
	cases := []struct {
		name    string
		repos   map[string]Repo
		wantErr bool
	}{
		{
			"valid extrepo-only", map[string]Repo{
				"mise": {ExtRepo: "mise"},
			}, false,
		},
		{
			"valid custom", map[string]Repo{
				"my-custom": {URL: "https://example.com/deb", KeyURL: "https://example.com/key.pub", Suites: "stable", Components: "main"},
			}, false,
		},
		{
			"extrepo plus url", map[string]Repo{
				"mise": {ExtRepo: "mise", URL: "https://example.com/deb"},
			}, true,
		},
		{
			"no url no extrepo", map[string]Repo{
				"mise": {},
			}, true,
		},
		{
			"custom without key_url", map[string]Repo{
				"my-custom": {URL: "https://example.com/deb"},
			}, true,
		},
		{
			"invalid map key", map[string]Repo{
				"bad name": {ExtRepo: "mise"},
			}, true,
		},
		{
			"invalid extrepo name", map[string]Repo{
				"mise": {ExtRepo: "bad name"},
			}, true,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			rc := RawProfile{Profile: base}
			rc.Repos = tt.repos
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

func TestValidateFiles(t *testing.T) {
	base := Profile{Version: 1, Image: "x", Command: []string{"sh"}}
	valid := []struct {
		name   string
		target string
		f      File
	}{
		{"absolute target", "/etc/tpd.conf", File{Content: "hi"}},
		{"tilde target", "~/.config/foo", File{Content: "hi"}},
		{"explicit mode", "/tmp/x", File{Content: "hi", Mode: 0o600}},
		{"tilde alone", "~", File{Content: "hi"}},
	}
	for _, tt := range valid {
		rc := RawProfile{Profile: base}
		rc.Files = map[string]File{tt.target: tt.f}
		if err := validate(rc); err != nil {
			t.Errorf("validate(files[%q]) = %v, want nil", tt.name, err)
		}
	}
	invalid := []struct {
		name   string
		target string
		f      File
	}{
		{"relative target", "relative/path", File{Content: "hi"}},
		{"tilde-username form", "~user/x", File{Content: "hi"}},
		{"path traversal", "~/../etc/passwd", File{Content: "hi"}},
		{"traversal absolute", "/etc/../../x", File{Content: "hi"}},
		{"mode too large", "~/.config/x", File{Content: "hi", Mode: 0o10000}},
	}
	for _, tt := range invalid {
		rc := RawProfile{Profile: base}
		rc.Files = map[string]File{tt.target: tt.f}
		if err := validate(rc); err == nil {
			t.Errorf("validate(files[%q]) = nil, want error", tt.name)
		}
	}
}

func TestValidateFilesAllowsEmptyContent(t *testing.T) {
	rc := RawProfile{Profile: Profile{Version: 1, Image: "x", Command: []string{"sh"}}}
	rc.Files = map[string]File{"~/.hushlogin": {Content: ""}}
	if err := validate(rc); err != nil {
		t.Errorf("empty content must be a valid empty file, got %v", err)
	}
}

func TestValidateToolsNames(t *testing.T) {
	base := Profile{Version: 1, Image: "x", Command: []string{"sh"}}
	valid := []string{"node", "npm:eslint", "appimage:pingdotgg/t3code", "rust", "python3.12"}
	for _, name := range valid {
		rc := RawProfile{Profile: base}
		rc.Tools = map[string]Tool{name: {Version: "latest"}}
		if err := validate(rc); err != nil {
			t.Errorf("validate(tools[%q]) = %v, want nil", name, err)
		}
	}
	invalid := []string{"", "node v20", "node\nlatest", "bad\x00name", "a;b", "x\t1"}
	for _, name := range invalid {
		rc := RawProfile{Profile: base}
		rc.Tools = map[string]Tool{name: {Version: "latest"}}
		if err := validate(rc); err == nil {
			t.Errorf("validate(tools[%q]) = nil, want error", name)
		}
	}
}

func TestValidateToolsRejectsControlInVersion(t *testing.T) {
	rc := RawProfile{Profile: Profile{Version: 1, Image: "x", Command: []string{"sh"}}}
	rc.Tools = map[string]Tool{"node": {Version: "20\n"}}
	if err := validate(rc); err == nil {
		t.Fatal("expected error for newline in tool version")
	}
}

func TestValidateToolsRejectsEmptyVersion(t *testing.T) {
	rc := RawProfile{Profile: Profile{Version: 1, Image: "x", Command: []string{"sh"}}}
	rc.Tools = map[string]Tool{"node": {}}
	if err := validate(rc); err == nil {
		t.Fatal("expected error for empty tool version")
	}
}

func TestValidateAppimageChecksums(t *testing.T) {
	base := Profile{Version: 1, Image: "x", Command: []string{"sh"}}
	valid := strings.Repeat("ab", 32)
	appimage := "appimage:owner/repo"
	cases := []struct {
		name    string
		tool    Tool
		wantErr bool
	}{
		{"latest without checksum", Tool{Version: "latest"}, false},
		{"malformed scalar sha256", Tool{Version: "v1", SHA256: "zz"}, true},
		{"short scalar sha256", Tool{Version: "v1", SHA256: strings.Repeat("a", 63)}, true},
		{"unknown per-arch key", Tool{Version: "v1", SHA256ByArch: map[string]string{"riscv64": valid}}, true},
		{"malformed per-arch sha256", Tool{Version: "v1", SHA256ByArch: map[string]string{"amd64": "xyz"}}, true},
		{"valid scalar sha256", Tool{Version: "v1", SHA256: valid}, false},
		{"valid per-arch sha256", Tool{Version: "v1", SHA256ByArch: map[string]string{"amd64": valid, "aarch64": valid}}, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			rc := RawProfile{Profile: base}
			rc.Tools = map[string]Tool{appimage: tt.tool}
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

func TestValidateEnv(t *testing.T) {
	base := Profile{Version: 1, Image: "x", Command: []string{"sh"}}
	valid := RawProfile{Profile: base}
	valid.Env = map[string]string{"GOOD_KEY": "value", "_X": "{{ .Env.HOME }}"}
	if err := validate(valid); err != nil {
		t.Fatalf("valid env rejected: %v", err)
	}
	for _, bad := range []map[string]string{
		{"BAD KEY": "x"},
		{"bad-key": "x"},
		{"GOOD\nKEY": "x"},
		{"1BAD": "x"},
		{"GOOD": "bad\nvalue"},
		{"GOOD": "bad\x00value"},
	} {
		rc := RawProfile{Profile: base}
		rc.Env = bad
		if err := validate(rc); err == nil {
			t.Errorf("validate(env=%v) = nil, want error", bad)
		}
	}
}

func TestValidateResources(t *testing.T) {
	base := Profile{Version: 1, Image: "x", Command: []string{"sh"}}
	valid := []struct {
		name string
		res  Resources
	}{
		{"memory 512m", Resources{Memory: "512m"}},
		{"memory 512mb", Resources{Memory: "512mb"}},
		{"cpus 2", Resources{CPUs: "2"}},
		{"cpus 1.5", Resources{CPUs: "1.5"}},
	}
	for _, tt := range valid {
		rc := RawProfile{Profile: base}
		rc.Resources = &tt.res
		if err := validate(rc); err != nil {
			t.Errorf("validate(resources=%+v) = %v, want nil", tt.res, err)
		}
	}
	invalid := []struct {
		name string
		res  Resources
	}{
		{"memory bogus", Resources{Memory: "bogus"}},
		{"cpus -1", Resources{CPUs: "-1"}},
		{"cpus NaN", Resources{CPUs: "NaN"}},
		{"cpus Inf", Resources{CPUs: "Inf"}},
		{"cpus 1e10", Resources{CPUs: "1e10"}},
	}
	for _, tt := range invalid {
		rc := RawProfile{Profile: base}
		rc.Resources = &tt.res
		if err := validate(rc); err == nil {
			t.Errorf("validate(resources=%+v) = nil, want error", tt.res)
		}
	}
}

func TestParseNanoCPUs(t *testing.T) {
	valid := []struct {
		in   string
		want int64
	}{
		{"2", 2000000000},
		{"1.5", 1500000000},
		{"0.5", 500000000},
		{"9223372036.854775", 9223372036854774784},
	}
	for _, tt := range valid {
		got, err := ParseNanoCPUs(tt.in)
		if err != nil {
			t.Errorf("ParseNanoCPUs(%q) = _, %v, want %d", tt.in, err, tt.want)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseNanoCPUs(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
	invalid := []string{"", "abc", "NaN", "Inf", "-1", "0", "1e10", "9223372036.854776"}
	for _, in := range invalid {
		if _, err := ParseNanoCPUs(in); err == nil {
			t.Errorf("ParseNanoCPUs(%q) = nil, want error", in)
		}
	}
}

func TestValidateNetwork(t *testing.T) {
	base := Profile{Version: 1, Image: "x", Command: []string{"sh"}}
	for _, nw := range []string{"", "host", "bridge", "none", "slirp4netns", "my.net_1"} {
		rc := RawProfile{Profile: base}
		rc.Network = nw
		if err := validate(rc); err != nil {
			t.Errorf("validate(network=%q) = %v, want nil", nw, err)
		}
	}
	for _, nw := range []string{"host\n", "bad network", "net;x", "bad/name"} {
		rc := RawProfile{Profile: base}
		rc.Network = nw
		if err := validate(rc); err == nil {
			t.Errorf("validate(network=%q) = nil, want error", nw)
		}
	}
}

func validServiceProfile() RawProfile {
	return RawProfile{Profile: Profile{
		Version: 1,
		Image:   "ubuntu",
		Command: []string{"sh"},
		Services: map[string]Service{
			"registry": {
				Image:   "debian:13-slim",
				Command: []string{"registry"},
				Exposes: map[string]string{"registry": "/run/registry/registry.sock"},
			},
		},
	}}
}

func TestValidateServicesOK(t *testing.T) {
	rc := validServiceProfile()
	if err := validate(rc); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateServicesMissingImage(t *testing.T) {
	rc := validServiceProfile()
	svc := rc.Services["registry"]
	svc.Image = ""
	rc.Services["registry"] = svc
	err := validate(rc)
	if err == nil || !strings.Contains(err.Error(), "services: registry: image is required") {
		t.Fatalf("expected image-required error, got: %v", err)
	}
}

func TestValidateServicesMissingCommand(t *testing.T) {
	rc := validServiceProfile()
	svc := rc.Services["registry"]
	svc.Command = nil
	rc.Services["registry"] = svc
	err := validate(rc)
	if err == nil || !strings.Contains(err.Error(), "services: registry: command is required") {
		t.Fatalf("expected command-required error, got: %v", err)
	}
}

func TestValidateServicesRejectNetworkHost(t *testing.T) {
	rc := validServiceProfile()
	svc := rc.Services["registry"]
	svc.Network = "host"
	rc.Services["registry"] = svc
	err := validate(rc)
	if err == nil || !strings.Contains(err.Error(), "services: registry: network is always enabled for services") {
		t.Fatalf("expected rejected-network error, got: %v", err)
	}
}

func TestValidateServicesRejectNetworkTrue(t *testing.T) {
	rc := validServiceProfile()
	svc := rc.Services["registry"]
	svc.Network = "true"
	rc.Services["registry"] = svc
	err := validate(rc)
	if err == nil || !strings.Contains(err.Error(), "services: registry: network is always enabled for services") {
		t.Fatalf("expected rejected-network error, got: %v", err)
	}
}

func TestValidateServicesRejectPorts(t *testing.T) {
	rc := validServiceProfile()
	svc := rc.Services["registry"]
	svc.Ports = map[string]PortBind{"8080": {}}
	rc.Services["registry"] = svc
	err := validate(rc)
	if err == nil || !strings.Contains(err.Error(), "services: registry: must not set ports") {
		t.Fatalf("expected rejected-field error for ports, got: %v", err)
	}
}

func TestValidateServicesRejectDevices(t *testing.T) {
	rc := validServiceProfile()
	svc := rc.Services["registry"]
	svc.Devices = map[string]DeviceBind{"/dev/fuse": {}}
	rc.Services["registry"] = svc
	err := validate(rc)
	if err == nil || !strings.Contains(err.Error(), "services: registry: must not set devices") {
		t.Fatalf("expected rejected-field error for devices, got: %v", err)
	}
}

func TestValidateServicesRejectNestedServices(t *testing.T) {
	rc := validServiceProfile()
	svc := rc.Services["registry"]
	svc.Services = map[string]Service{"inner": {Image: "x", Command: []string{"x"}}}
	rc.Services["registry"] = svc
	err := validate(rc)
	if err == nil || !strings.Contains(err.Error(), "services: registry: must not declare nested services") {
		t.Fatalf("expected nested-services error, got: %v", err)
	}
}

func TestValidateServicesRejectServiceSocketMount(t *testing.T) {
	rc := validServiceProfile()
	svc := rc.Services["registry"]
	svc.Mounts = map[string]Mount{
		"/sock": {Service: "other", Socket: "x"},
	}
	rc.Services["registry"] = svc
	err := validate(rc)
	if err == nil || !strings.Contains(err.Error(), "services: registry: mount /sock: must not use service/socket") {
		t.Fatalf("expected service-socket-in-service error, got: %v", err)
	}
}

func TestValidateServicesExposesMustBeAbsolute(t *testing.T) {
	rc := validServiceProfile()
	svc := rc.Services["registry"]
	svc.Exposes = map[string]string{"registry": "relative/sock"}
	rc.Services["registry"] = svc
	err := validate(rc)
	if err == nil || !strings.Contains(err.Error(), "services: registry: exposes registry: path must be absolute") {
		t.Fatalf("expected absolute-path error, got: %v", err)
	}
}

func TestValidateServiceNameRegex(t *testing.T) {
	rc := validServiceProfile()
	rc.Services["bad/name"] = rc.Services["registry"]
	delete(rc.Services, "registry")
	err := validate(rc)
	if err == nil || !strings.Contains(err.Error(), "services: bad/name: invalid service name") {
		t.Fatalf("expected invalid-name error, got: %v", err)
	}
}

func TestValidateServiceNameGrammar(t *testing.T) {
	for _, name := range []string{"svc_one", "postgres.main", "Redis"} {
		rc := validServiceProfile()
		rc.Services[name] = rc.Services["registry"]
		delete(rc.Services, "registry")
		err := validate(rc)
		if err == nil || !strings.Contains(err.Error(), "services: "+name+": invalid service name") {
			t.Errorf("validate(service name %q) = %v, want invalid-name error", name, err)
		}
	}
}

func TestValidateNetworkHostWithServices(t *testing.T) {
	rc := validServiceProfile()
	rc.Network = "host"
	err := validate(rc)
	if err == nil || !strings.Contains(err.Error(), "network: host cannot be combined with services") {
		t.Fatalf("expected host-network-with-services error, got: %v", err)
	}
}

func TestValidateNetworkHostAloneOK(t *testing.T) {
	rc := RawProfile{Profile: Profile{Version: 1, Image: "x", Command: []string{"sh"}, Network: "host"}}
	if err := validate(rc); err != nil {
		t.Fatalf("expected no error for network: host without services, got: %v", err)
	}
}

func TestValidateMountServiceWithoutSocket(t *testing.T) {
	rc := validServiceProfile()
	rc.Mounts = map[string]Mount{"/sock": {Service: "registry"}}
	err := validate(rc)
	if err == nil || !strings.Contains(err.Error(), "mount /sock: service requires socket") {
		t.Fatalf("expected service-requires-socket error, got: %v", err)
	}
}

func TestValidateMountSocketWithoutService(t *testing.T) {
	rc := validServiceProfile()
	rc.Mounts = map[string]Mount{"/sock": {Socket: "registry"}}
	err := validate(rc)
	if err == nil || !strings.Contains(err.Error(), "mount /sock: socket requires service") {
		t.Fatalf("expected socket-requires-service error, got: %v", err)
	}
}

func TestValidateMountServiceSocketWithSource(t *testing.T) {
	rc := validServiceProfile()
	rc.Mounts = map[string]Mount{"/sock": {Service: "registry", Socket: "registry", Source: "/host"}}
	err := validate(rc)
	if err == nil || !strings.Contains(err.Error(), "mount /sock: must not set source with service/socket") {
		t.Fatalf("expected no-source-with-service error, got: %v", err)
	}
}

func TestValidateMountServiceSocketWithCreate(t *testing.T) {
	rc := validServiceProfile()
	rc.Mounts = map[string]Mount{"/sock": {Service: "registry", Socket: "registry", Create: true}}
	err := validate(rc)
	if err == nil || !strings.Contains(err.Error(), "mount /sock: must not set create with service/socket") {
		t.Fatalf("expected no-create-with-service error, got: %v", err)
	}
}

func TestValidateMountServiceSocketWithOptional(t *testing.T) {
	rc := validServiceProfile()
	rc.Mounts = map[string]Mount{"/sock": {Service: "registry", Socket: "registry", Optional: true}}
	err := validate(rc)
	if err == nil || !strings.Contains(err.Error(), "mount /sock: must not set optional with service/socket") {
		t.Fatalf("expected no-optional-with-service error, got: %v", err)
	}
}

func TestValidateMountServiceMissingService(t *testing.T) {
	rc := validServiceProfile()
	rc.Mounts = map[string]Mount{"/sock": {Service: "nonexistent", Socket: "registry"}}
	err := validate(rc)
	if err == nil || !strings.Contains(err.Error(), "mount /sock: service \"nonexistent\" not declared") {
		t.Fatalf("expected missing-service error, got: %v", err)
	}
}

func TestValidateMountServiceMissingSocket(t *testing.T) {
	rc := validServiceProfile()
	rc.Mounts = map[string]Mount{"/sock": {Service: "registry", Socket: "nonexistent"}}
	err := validate(rc)
	if err == nil || !strings.Contains(err.Error(), "mount /sock: socket \"nonexistent\" not exposed by service \"registry\"") {
		t.Fatalf("expected missing-socket error, got: %v", err)
	}
}
