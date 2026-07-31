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

func TestValidateBothImageAndBuild(t *testing.T) {
	rc := RawProfile{Profile: Profile{Version: 1, Image: "x", Build: &Build{Dockerfile: "Dockerfile"}, Command: []string{"sh"}}}
	err := validate(rc)
	if err == nil {
		t.Fatal("expected error for both image and build")
	}
}

func TestValidateNeitherImageNorBuild(t *testing.T) {
	rc := RawProfile{Profile: Profile{Version: 1, Command: []string{"sh"}}}
	err := validate(rc)
	if err == nil {
		t.Fatal("expected error for neither image nor build")
	}
}

func TestValidateReservedName(t *testing.T) {
	for _, name := range []string{"config", "doctor", "help", "version", "completion", "prune", "init"} {
		rc := RawProfile{Profile: Profile{Version: 1, Image: "x", Command: []string{"sh"}}}
		rc.Path = "/home/me/.config/toolpod/" + name + ".yaml"
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
