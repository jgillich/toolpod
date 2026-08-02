package profile

import "testing"

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
		rc.Path = "/home/me/.config/tpod/" + name + ".yaml"
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

func TestValidateName(t *testing.T) {
	valid := []string{"foo", "my-agent", "a.b", "opencode", "x_y"}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}
	invalid := []string{"", "config", "doctor", "help", "version", "completion", "prune", "init", "../x", "a/b", `a\b`, "a b", "a..b"}
	for _, name := range invalid {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", name)
		}
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
