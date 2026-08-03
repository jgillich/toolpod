package tpd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jgillich/tpd/internal/profile"
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
// the profile's dbus allowlist, listening on $XDG_RUNTIME_DIR/tpd-bus-<pid>.sock.
// It returns a cleanup that kills the proxy and removes the socket, and the
// DBUS_SESSION_BUS_ADDRESS to set in the container ("" = bus disabled).
//
// When the profile has a dbus allowlist, the bus fails closed: an error is
// returned (and Launch aborts) if there is no host session bus to proxy or
// xdg-dbus-proxy is not installed, instead of silently disabling the bus.
func startBusProxy(cfg profile.Profile) (func(), string, error) {
	if !dbusEnabled(cfg) {
		return nil, "", nil
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	hostBus := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if hostBus == "" {
		if runtimeDir != "" {
			hostBus = "unix:path=" + filepath.Join(runtimeDir, "bus")
		}
	}
	if runtimeDir == "" || hostBus == "" {
		return nil, "", fmt.Errorf("profile requires filtered D-Bus but no host session bus is available")
	}
	proxy, err := exec.LookPath("xdg-dbus-proxy")
	if err != nil {
		return nil, "", fmt.Errorf("profile requires filtered D-Bus but xdg-dbus-proxy is not installed")
	}
	sockPath := filepath.Join(runtimeDir, fmt.Sprintf("tpd-bus-%d.sock", os.Getpid()))
	_ = os.Remove(sockPath)

	args := append([]string{proxy, hostBus, sockPath, "--filter"}, proxyFilterArgs(cfg)...)
	if os.Getenv("TPD_OPEN_DEBUG") != "" {
		args = append(args, "--log")
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "tpd: warning: start xdg-dbus-proxy: %v\n", err)
		return nil, "", fmt.Errorf("profile requires filtered D-Bus but xdg-dbus-proxy did not start")
	}
	// Wait for the proxy to create its socket so container clients don't race
	// startup (a client that connects before the proxy listens gets ENOENT).
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			if err := cmd.Process.Kill(); err != nil {
				fmt.Fprintf(os.Stderr, "tpd: warning: kill xdg-dbus-proxy: %v\n", err)
			}
			if _, err := cmd.Process.Wait(); err != nil {
				fmt.Fprintf(os.Stderr, "tpd: warning: wait xdg-dbus-proxy: %v\n", err)
			}
			if err := os.Remove(sockPath); err != nil {
				fmt.Fprintf(os.Stderr, "tpd: warning: remove socket %s: %v\n", sockPath, err)
			}
			fmt.Fprintln(os.Stderr, "tpd: warning: xdg-dbus-proxy did not start; container D-Bus disabled")
			return nil, "", fmt.Errorf("profile requires filtered D-Bus but xdg-dbus-proxy did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cleanup := func() {
		if err := cmd.Process.Kill(); err != nil {
			fmt.Fprintf(os.Stderr, "tpd: warning: kill xdg-dbus-proxy: %v\n", err)
		}
		if _, err := cmd.Process.Wait(); err != nil {
			fmt.Fprintf(os.Stderr, "tpd: warning: wait xdg-dbus-proxy: %v\n", err)
		}
		if err := os.Remove(sockPath); err != nil {
			fmt.Fprintf(os.Stderr, "tpd: warning: remove socket %s: %v\n", sockPath, err)
		}
	}
	return cleanup, "unix:path=" + sockPath, nil
}
