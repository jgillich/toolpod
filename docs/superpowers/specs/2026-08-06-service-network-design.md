# Service Container Connectivity Specification

Status: Proposed
Date: 2026-08-06

## Problem

Consumers need to reach tpd-managed services without guessing an engine-specific address, nested gateway, or dynamically allocated host port. The problem is especially visible when a service runs a nested Podman engine: a child port is published through the service container, and the consumer cannot safely infer the nested engine address or port.

The existing Unix-socket service mounts remain useful, but they are not a general TCP/UDP discovery mechanism.

## Goals

- Give every network-capable managed service a stable, deterministic network name.
- Make direct and nested service containers reachable through the same consumer contract.
- Support multiple services concurrently, including services using the same container port.
- Make service recreation transparent to newly created consumers.
- Avoid proxies, host firewall rules, port watchers, and guessed addresses.
- Preserve existing service locks, lifecycle, socket mounts, and optional host port publishing.

## Non-goals

- Exposing every service to arbitrary host applications.
- Replacing explicit host port publishing for host-to-service integrations.
- Making the shared service network a security boundary.
- Automatically discovering application protocols or ports that the service profile does not declare.

## Architecture

### One persistent managed network

tpd lazily creates or reuses one user-scoped, user-defined bridge network named tpd-services. It is persistent across launches and is removed only by explicit network pruning.

The network carries:

- tpd.managed=true
- tpd.network-role=services

The engine provides container DNS on this network. The network is shared by network-capable tpd services and every tpd consumer that declares at least one such service. This is simpler than one network per service because consumers are created on demand and may need arbitrary combinations of services.

Creation is race-safe. Concurrent launches may both inspect before the network exists; if one create succeeds and the other receives an engine conflict, the loser re-inspects and validates the winner's network instead of failing.

The shared network is a discovery fabric, not isolation. A participant attached to it can connect to another participant if it knows the alias and port. Profiles needing isolation must use a separately configured runtime/network boundary.

### Stable aliases

A service named NAME is attached to tpd-services with the DNS-safe alias tpd-svc-NAME. Because service names are restricted to letters, numbers, and hyphens (`^[a-z0-9][a-z0-9-]*$`), the name is already DNS-safe and the alias is simply the name prefixed with tpd-svc-. Thus postgres-main resolves as tpd-svc-postgres-main.

The name grammar makes alias and environment-name collisions structurally impossible: two distinct valid names always yield distinct aliases and distinct environment variable names. No collision detection code is needed.

The alias does not contain an IP address and does not change when the service is recreated.

Service containers created by the current engine and service containers that run a nested engine use the same alias rule. A nested child remains behind the service container; the nested engine may publish the child port inside that container. The consumer uses:

tpd-svc-NAME:<nested-published-port>

The nested engine API is responsible for reporting the child’s dynamically selected port to the process that launches the child. tpd does not guess or watch that port.

### Consumer attachment

Every launch that declares at least one service:

1. StartServices ensures tpd-services exists.
2. StartServices ensures each service is attached with its stable alias.
3. tpd creates the consumer using its configured primary network.
4. tpd attaches the consumer to tpd-services before starting it.

The managed network is an additional attachment. The launch’s normal network setting remains the consumer’s primary network.

If network attachment fails, tpd reports the failure and does not start user code. The existing cleanup path removes the created consumer and releases service resources.

A launch without services neither creates nor joins tpd-services. Socket-only services continue to use their existing mounts alongside the network attachment.

### Environment contract

For each declared service NAME, tpd injects:

TPD_SERVICE_NAME_HOST=tpd-svc-NAME

The variable name is produced by uppercasing ASCII letters and replacing each hyphen in NAME with underscore. Thus postgres-main becomes TPD_SERVICE_POSTGRES_MAIN_HOST. Its value is the same DNS-safe alias used for network attachment.

The value is always the stable alias, never a resolved IP. The variable is valid for both direct and nested services.

The service profile remains responsible for its port contract. Existing service profiles can use their documented container port. A nested service profile can publish a child port inside the service container and pass that port to the nested child consumer configuration through the existing service-specific mechanism. Adding an optional port metadata field is a follow-up only if current profiles need tpd-generated PORT variables.

A user-provided environment value for a generated TPD_SERVICE_*_HOST variable is rejected rather than silently overwritten.

Existing socket mounts remain unchanged. A service that exposes a socket still gets the socket mount and also joins tpd-services with its stable alias. The variable points at the alias; whether the service listens on TCP is the service profile's own contract.

### Lifecycle

StartServices retains the existing per-service locks until the consumer is created and attached to the managed network. This prevents a concurrent service stop from observing a partially registered consumer.

Service labels remain:

- tpd.managed=true
- tpd.service=NAME
- tpd.service-role=sidecar
- tpd.service-hash=HASH

The consumer label tpd.uses-service includes every declared service name, including network-only use. Existing socket use remains included.

When a service is reused, tpd verifies that it is attached to tpd-services with the expected alias and repairs the attachment if necessary. When it is recreated, the new container receives the same alias. Its IP may change; consumers resolve the alias through engine DNS.

Normal StopServices does not remove tpd-services. The network is shared and persistent.

### Host ports

Host port publishing remains opt-in and is intended for host or external clients. It is not the service-to-consumer discovery mechanism.

Consumers attached to tpd-services use the service alias and the service container’s port. For nested services, that is the port published by the nested engine inside the service container. This avoids depending on the outer host port or nested gateway address.

### Host networking

network: host remains a primary-network option. A host-network consumer cannot attach to an additional bridge network, so a profile that sets `network: host` while also declaring services is rejected at validation time with a clear message. tpd never claims service discovery is available when attachment could not succeed.

The default engine network is not the contract because its name and DNS behavior vary by engine and configuration. A user-defined managed network gives tpd a stable ownership, lifecycle, and naming point.

## Pruning and diagnostics

tpd prune gains an explicit --networks scope. It removes only the canonical tpd-services network when all conditions hold:

- the network has both tpd ownership and service-network labels;
- no running container, including an unmanaged one, references it;
- the user explicitly requested network pruning.

Without --networks, networks are untouched. With no type flags, prune retains its existing volume-and-image scope. --networks alone selects only networks and may be combined with --volumes or --images. --all never implies network scope. Selected networks pass through the same confirmation prompt as volumes and images unless confirmation is explicitly skipped.

Network reference protection uses an unfiltered scan of all running containers and is repeated immediately before removal. It does not reuse the existing tpd-owned-container helper used for volume and image liveness.

tpd doctor reports the managed network when present, including driver, labels, and connected-container count. A malformed network with the canonical name is reported as an actionable diagnostic failure.

## Security

All tpd service participants share one network. Service names are not authorization. Services must provide their own authentication and authorization. The network must not be presented as an isolation boundary.

The network is labeled so pruning can distinguish it from user-owned networks. Network removal is conservative and protects references from running containers.

## Validation evidence

Host validation used rootless Podman 5.8.4 with netavark:

- two ordinary services joined one user-defined network and both were reachable by aliases while listening on the same container port;
- recreating one service changed its IP but left the alias valid and resolving to the replacement;
- a real tpd service container was attached to the validation network;
- a nested child published a dynamically selected port inside that service container;
- an external client reached the nested child through the stable service alias and the nested published port.

This demonstrates that stable network DNS plus explicit attachment solves the discovery problem without a proxy or direct nested port forwarding.

## Acceptance criteria

1. A consumer can reach a service using TPD_SERVICE_NAME_HOST and the service’s documented port.
2. Two services can listen on the same container port and remain independently reachable by alias.
3. Recreating a service preserves its alias for new consumers.
4. A nested child published inside a service container is reachable through the service alias and reported child port.
5. A launch without services does not create or join tpd-services.
6. Existing socket service profiles continue to pass unchanged.
7. A failed service-network attachment prevents user code from starting.
8. tpd prune --networks never removes an unmanaged or running-container-referenced network.
9. A profile with `network: host` and services is rejected at validation.
