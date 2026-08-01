package toolpod

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jgillich/toolpod/internal/profile"
)

// dbusEnabled reports whether the profile has at least one non-nil talk/own
// name, i.e. any effective filter rule. A nil name (YAML `null`) clears an
// inherited entry and contributes no rules.
func dbusEnabled(cfg profile.Profile) bool {
	if cfg.Dbus == nil {
		return false
	}
	for _, v := range cfg.Dbus.Talk {
		if v != nil {
			return true
		}
	}
	for _, v := range cfg.Dbus.Own {
		if v != nil {
			return true
		}
	}
	return false
}

// proxyFilterArgs builds the flatpak-style allowlist flags for the profile's
// dbus config: --see + --talk for every talk name, --see + --own for every
// own name. Map iteration order is non-deterministic; callers must not rely
// on argument order.
func proxyFilterArgs(cfg profile.Profile) []string {
	if !dbusEnabled(cfg) {
		return nil
	}
	var args []string
	for name, v := range cfg.Dbus.Talk {
		if v == nil {
			continue
		}
		args = append(args, "--see="+name, "--talk="+name)
	}
	for name, v := range cfg.Dbus.Own {
		if v == nil {
			continue
		}
		args = append(args, "--see="+name, "--own="+name)
	}
	return args
}

// startBusProxy spawns a host-side xdg-dbus-proxy for the launch, filtered to
// the profile's dbus allowlist, listening on $XDG_RUNTIME_DIR/toolpod-bus-<pid>.sock.
// It returns a cleanup that kills the proxy and removes the socket, and the
// DBUS_SESSION_BUS_ADDRESS to set in the container ("" = bus disabled).
//
// The bus is disabled (no proxy, empty address) when the profile has no dbus
// config, there is no host session bus to proxy, or xdg-dbus-proxy is not
// installed on the host.
func startBusProxy(cfg profile.Profile) (func(), string) {
	if !dbusEnabled(cfg) {
		return nil, ""
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	hostBus := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if hostBus == "" {
		if runtimeDir != "" {
			hostBus = "unix:path=" + filepath.Join(runtimeDir, "bus")
		}
	}
	if runtimeDir == "" || hostBus == "" {
		return nil, ""
	}
	proxy, err := exec.LookPath("xdg-dbus-proxy")
	if err != nil {
		fmt.Fprintln(os.Stderr, "toolpod: warning: xdg-dbus-proxy not found; container D-Bus disabled")
		return nil, ""
	}
	sockPath := filepath.Join(runtimeDir, fmt.Sprintf("toolpod-bus-%d.sock", os.Getpid()))
	_ = os.Remove(sockPath)

	args := append([]string{proxy, hostBus, sockPath, "--filter"}, proxyFilterArgs(cfg)...)
	if os.Getenv("TOOLPOD_OPEN_DEBUG") != "" {
		args = append(args, "--log")
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "toolpod: warning: start xdg-dbus-proxy: %v\n", err)
		return nil, ""
	}
	// Wait for the proxy to create its socket so container clients don't race
	// startup (a client that connects before the proxy listens gets ENOENT).
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			_ = os.Remove(sockPath)
			fmt.Fprintln(os.Stderr, "toolpod: warning: xdg-dbus-proxy did not start; container D-Bus disabled")
			return nil, ""
		}
		time.Sleep(10 * time.Millisecond)
	}
	cleanup := func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = os.Remove(sockPath)
	}
	return cleanup, "unix:path=" + sockPath
}
