package runtime

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
)

// ensureServiceNetwork returns ServiceNetworkName after creating the managed
// bridge network or finding a valid existing one. Creation is race-safe: if
// another launch wins the create, the winner's network is re-inspected and
// validated instead of failing.
func (d *DockerRuntime) ensureServiceNetwork(ctx context.Context) (string, error) {
	inspected, err := d.cli.NetworkInspect(ctx, ServiceNetworkName, network.InspectOptions{})
	if err == nil {
		return ServiceNetworkName, validateServiceNetwork(inspected)
	}
	if !client.IsErrNotFound(err) {
		return "", fmt.Errorf("inspect network %q: %w", ServiceNetworkName, err)
	}

	_, err = d.cli.NetworkCreate(ctx, ServiceNetworkName, network.CreateOptions{
		Driver: "bridge",
		Labels: serviceNetworkLabels(),
	})
	if err == nil {
		return ServiceNetworkName, nil
	}
	if !errdefs.IsConflict(err) {
		return "", fmt.Errorf("create network %q: %w", ServiceNetworkName, err)
	}

	inspected, err = d.cli.NetworkInspect(ctx, ServiceNetworkName, network.InspectOptions{})
	if err != nil {
		return "", fmt.Errorf("inspect network %q after create conflict: %w", ServiceNetworkName, err)
	}
	return ServiceNetworkName, validateServiceNetwork(inspected)
}

func serviceNetworkLabels() map[string]string {
	labels := OwnershipLabels()
	labels[NetworkRoleLabel] = NetworkRoleServices
	return labels
}

// validateServiceNetwork rejects a network that lacks the canonical name, the
// ownership label, the services role, or the bridge driver, naming the network
// and the failed invariant.
func validateServiceNetwork(net network.Inspect) error {
	switch {
	case net.Name != ServiceNetworkName:
		return fmt.Errorf("network %q: name mismatch, want %q", net.Name, ServiceNetworkName)
	case net.Labels[OwnershipLabel] != "true":
		return fmt.Errorf("network %q is not tpd-managed (missing %s=true)", net.Name, OwnershipLabel)
	case net.Labels[NetworkRoleLabel] != NetworkRoleServices:
		return fmt.Errorf("network %q has role %s=%q, want %q", net.Name, NetworkRoleLabel, net.Labels[NetworkRoleLabel], NetworkRoleServices)
	case net.Driver != "bridge":
		return fmt.Errorf("network %q is not a bridge (driver %q)", net.Name, net.Driver)
	}
	return nil
}

// ConnectContainerToNetwork attaches a container to the managed network under
// DNS aliases, so services are reachable by name instead of a resolved IP.
func (d *DockerRuntime) ConnectContainerToNetwork(ctx context.Context, containerID, networkName string, aliases []string) error {
	err := d.cli.NetworkConnect(ctx, networkName, containerID, &network.EndpointSettings{
		Aliases: append([]string(nil), aliases...),
	})
	if err != nil {
		return fmt.Errorf("connect container %s to network %s: %w", containerID, networkName, err)
	}
	return nil
}

// RemoveContainer explicitly removes a container that failed before the run
// phase, where RunContainer's deferred cleanup does not apply.
func (d *DockerRuntime) RemoveContainer(ctx context.Context, containerID string) error {
	if err := d.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("remove container %s: %w", containerID, err)
	}
	return nil
}
