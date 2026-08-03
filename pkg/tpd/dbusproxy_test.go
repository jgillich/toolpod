package tpd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgillich/tpd/internal/profile"
)

func TestProxyFilterArgs(t *testing.T) {
	cfg := profile.Profile{Dbus: &profile.DbusConfig{
		Talk: map[string]*struct{}{"org.freedesktop.portal.Desktop": &struct{}{}},
		Own:  map[string]*struct{}{"xyz.block.buzz.app": &struct{}{}},
	}}
	args := proxyFilterArgs(cfg)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--see=org.freedesktop.portal.Desktop",
		"--talk=org.freedesktop.portal.Desktop",
		"--own=xyz.block.buzz.app",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %v", want, args)
		}
	}
}

func TestProxyFilterArgsSkipsNilNames(t *testing.T) {
	cfg := profile.Profile{Dbus: &profile.DbusConfig{
		Talk: map[string]*struct{}{
			"org.freedesktop.portal.Desktop": &struct{}{},
			"org.freedesktop.Notifications":  nil,
		},
	}}
	args := proxyFilterArgs(cfg)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "org.freedesktop.Notifications") {
		t.Errorf("nil name must not be allowlisted: %v", args)
	}
	if !strings.Contains(joined, "--talk=org.freedesktop.portal.Desktop") {
		t.Errorf("non-nil name should still be allowlisted: %v", args)
	}
}

func TestStartBusProxyNoConfigDisables(t *testing.T) {
	// No dbus config -> no proxy, empty address (bus disabled).
	cfg := profile.Profile{}
	cleanup, addr, err := startBusProxy(cfg)
	if err != nil {
		t.Fatalf("no dbus config should not error, got %v", err)
	}
	if addr != "" {
		t.Errorf("addr = %q, want empty when profile has no dbus config", addr)
	}
	if cleanup != nil {
		t.Error("cleanup should be nil when no proxy started")
	}
}

func TestStartBusProxySpawnsAndFilters(t *testing.T) {
	// Fake xdg-dbus-proxy records its args to a file, then sleeps. It creates
	// the socket file at its second positional arg (the socket path) so
	// startBusProxy's readiness poll can observe it.
	dir := t.TempDir()
	record := filepath.Join(dir, "args")
	proxy := filepath.Join(dir, "xdg-dbus-proxy")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + record + "\n: > \"$2\"\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(proxy, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")

	cfg := profile.Profile{Dbus: &profile.DbusConfig{
		Talk: map[string]*struct{}{"org.freedesktop.portal.Desktop": &struct{}{}},
	}}
	cleanup, addr, err := startBusProxy(cfg)
	if err != nil {
		t.Fatalf("startBusProxy: %v", err)
	}
	if cleanup == nil {
		t.Fatal("expected a running proxy")
	}
	defer cleanup()
	if !strings.HasPrefix(addr, "unix:path="+dir+"/tpd-bus-") {
		t.Errorf("addr = %q, want unix:path in XDG_RUNTIME_DIR", addr)
	}
	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read recorded args: %v", err)
	}
	gotS := string(got)
	for _, want := range []string{
		"unix:path=/run/user/1000/bus", // host bus address
		"--filter",
		"--talk=org.freedesktop.portal.Desktop",
	} {
		if !strings.Contains(gotS, want) {
			t.Errorf("proxy args missing %q:\n%s", want, gotS)
		}
	}
	// The socket path must be a positional arg (a plain path).
	if !strings.Contains(gotS, "\n"+filepath.Join(dir, "tpd-bus-")) {
		t.Errorf("proxy should be given the socket path as a plain path:\n%s", gotS)
	}
}

func TestStartBusProxyMissingBinaryFailsClosed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir) // no xdg-dbus-proxy here
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")
	cfg := profile.Profile{Dbus: &profile.DbusConfig{
		Talk: map[string]*struct{}{"org.freedesktop.portal.Desktop": &struct{}{}},
	}}
	cleanup, addr, err := startBusProxy(cfg)
	if err == nil {
		t.Fatal("expected an error when xdg-dbus-proxy binary is missing")
	}
	if !strings.Contains(err.Error(), "xdg-dbus-proxy") {
		t.Errorf("error should mention xdg-dbus-proxy: %v", err)
	}
	if cleanup != nil || addr != "" {
		t.Errorf("no proxy should be running (cleanup=%v addr=%q)", cleanup != nil, addr)
	}
}

func TestStartBusProxyNoHostBusFailsClosed(t *testing.T) {
	// No host session bus (neither $XDG_RUNTIME_DIR nor
	// $DBUS_SESSION_BUS_ADDRESS) must fail closed, not silently disable.
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	cfg := profile.Profile{Dbus: &profile.DbusConfig{
		Talk: map[string]*struct{}{"org.freedesktop.portal.Desktop": &struct{}{}},
	}}
	cleanup, addr, err := startBusProxy(cfg)
	if err == nil {
		t.Fatal("expected an error when no host session bus is available")
	}
	if !strings.Contains(err.Error(), "session bus") {
		t.Errorf("error should mention the missing session bus: %v", err)
	}
	if cleanup != nil || addr != "" {
		t.Errorf("no proxy should be running (cleanup=%v addr=%q)", cleanup != nil, addr)
	}
}

func TestStartBusProxyFallsBackToRuntimeDirBus(t *testing.T) {
	// With DBUS_SESSION_BUS_ADDRESS unset, the host bus address falls back to
	// unix:path=$XDG_RUNTIME_DIR/bus.
	// Fake xdg-dbus-proxy records its args, then sleeps. It creates the socket
	// file at its second positional arg so the readiness poll can observe it.
	dir := t.TempDir()
	record := filepath.Join(dir, "args")
	proxy := filepath.Join(dir, "xdg-dbus-proxy")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + record + "\n: > \"$2\"\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(proxy, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	cfg := profile.Profile{Dbus: &profile.DbusConfig{
		Talk: map[string]*struct{}{"org.freedesktop.portal.Desktop": &struct{}{}},
	}}
	cleanup, addr, err := startBusProxy(cfg)
	if err != nil {
		t.Fatalf("startBusProxy: %v", err)
	}
	if cleanup == nil {
		t.Fatal("expected a running proxy")
	}
	defer cleanup()
	if addr == "" {
		t.Fatal("expected a proxy bus address")
	}
	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read recorded args: %v", err)
	}
	if !strings.Contains(string(got), "unix:path="+dir+"/bus") {
		t.Errorf("host bus should fall back to $XDG_RUNTIME_DIR/bus:\n%s", got)
	}
}
