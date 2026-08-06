# Service Network Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give every tpd service a stable DNS alias and built-in HOST environment variable through one persistent managed network.

**Architecture:** Lazily create one labeled tpd-services bridge network, connect each service with alias tpd-svc-<name>, then attach service-using consumers before they start. Keep the existing primary network, lifecycle locks, consumer labels, and Unix-socket mounts; dynamic nested ports continue to come from the nested engine API.

**Design decision (2026-08-06):** Service networking is **always on**. There is no per-service `network:` opt-in field. Every declared service joins tpd-services with its stable alias and produces a `TPD_SERVICE_<NAME>_HOST` env var. Profile, fragment, service, and socket names are restricted to letters, numbers, and dashes only (`^[a-z0-9][a-z0-9-]*$`), which makes alias and env-name collisions structurally impossible. A profile whose consumer primary network is `host` that also declares services is a validation error (a host-network consumer cannot join a bridge network).

**Tech Stack:** Go 1.25, Docker Engine API client compatible with Podman and Docker, Cobra, existing httptest runtime fakes and launch fake runtime.

## Global Constraints

- Primary target is rootless Podman on Linux; Docker and rootful Podman remain supported through the Docker API.
- Network name is exactly tpd-services.
- Network labels are exactly tpd.managed=true and tpd.network-role=services.
- Service alias is exactly tpd-svc-<NAME>; NAME is the service name unchanged (names are already DNS-safe by the grammar below).
- Name grammar: profile, fragment, service, and socket (exposes key) names match exactly `^[a-z0-9][a-z0-9-]*$`. No dots, no underscores, no other characters. This is enforced by the single shared `profileNameRe` regex used for catalog segments, service names, socket names, and profile names.
- Consumer variable is exactly TPD_SERVICE_<ENV_NAME>_HOST for every declared service; ENV_NAME uppercases ASCII and replaces hyphens with underscore.
- Values contain aliases, never resolved IPs.
- Existing network remains the consumer primary network; tpd-services is an additional attachment.
- Existing service socket mounts and lifecycle behavior remain compatible.
- No proxy, firewall rule, watcher, per-service network, or port/endpoint schema is part of this implementation.
- Prune never removes an unmanaged network or one referenced by any running container.
- A profile-level `network: host` combined with any declared service is rejected at validation time with a clear message (the consumer cannot attach to a bridge network while using host networking).
- A stray `network:` key on a service is a validation error, not silently ignored.
- Preserve unrelated worktree changes and use comments only for intent, constraints, or engine behavior.
- Each task follows TDD, ends with a focused test run, and is committed separately.

---

## File map

Create:

- internal/runtime/docker_network.go — managed network creation, ownership validation, membership, and alias attachment.
- internal/runtime/docker_network_test.go — Docker-compatible HTTP API tests for create, reuse, conflicts, and connect calls.
- internal/runtime/labels_test.go — DNS alias and environment name tests.

Modify:

- internal/profile/types.go — reject the removed service `network` key (captured, then rejected) and restrict name grammar.
- internal/profile/validate.go — validate the name grammar everywhere it applies; reject service `network:`; reject `network: host` + services.
- internal/profile/types_test.go, internal/profile/validate_test.go, internal/profile/merge_multi_test.go — schema, validation, and whole-service replacement tests.
- internal/runtime/runtime.go — extend service bindings/runtime interface for post-create attachment.
- internal/runtime/labels.go — canonical network name, network labels, alias helper, and environment-name helper.
- internal/runtime/fake.go — record consumer network attachment in launch tests.
- internal/runtime/docker_services.go — ensure the network and attach both new and reused service containers.
- internal/runtime/docker_services_test.go — verify aliases, repair, bindings, and existing socket behavior.
- pkg/tpd/spec.go — mark every declared service in tpd.uses-service and generate HOST env vars for every service.
- pkg/tpd/spec_test.go — label and env tests.
- pkg/tpd/launch.go — inject service HOST variables and attach consumers before start.
- pkg/tpd/launch_test.go — ordering, no-service, and failure-cleanup tests.
- internal/prune/prune.go — explicit, ownership-safe network pruning.
- internal/prune/prune_test.go — unmanaged and running-reference protection.
- cmd/tpd/cli.go — --networks flag, result output, and help text.
- cmd/tpd/cli_test.go — CLI flag and output tests.
- internal/doctor/checks.go — managed network diagnostic.
- internal/doctor/checks_test.go — absent, valid, and malformed network cases.
- cmd/tpd/e2e_runtime_test.go — real-engine alias and same-port validation.
- docs/superpowers/plans/2026-08-04-services.md — link the existing service lifecycle plan to the network specification.

No profile schema field is added by this plan; the previous `services.<name>.network` opt-in is removed and rejected.

---

### Task 1: Restrict name grammar and reject the removed service network key

Files:

- Modify: internal/profile/types.go
- Modify: internal/profile/validate.go
- Modify: internal/profile/catalog.go
- Test: internal/profile/types_test.go
- Test: internal/profile/validate_test.go
- Test: internal/profile/merge_multi_test.go

Interfaces:

- `profileNameRe` becomes exactly `^[a-z0-9][a-z0-9-]*$`.
- `Service.Network` is a `string` field (captured so a stray `network:` key is not silently dropped), and validation rejects any non-empty value with: `services: <name>: network is always enabled for services; remove the network field`.
- A profile-level `network: host` with any declared service is rejected with a message naming the incompatibility.

- [ ] Step 1: Write parsing and validation tests:
  - service `network: true` and `network: host` both parse (captured) then fail validation naming the service.
  - service name with `_` or `.` fails validation.
  - profile name with `_` or `.` fails validation.
  - `network: host` + services fails validation; `network: host` alone still passes.
- [ ] Step 2: Run the focused tests to verify they fail.

Run: go test ./internal/profile -run 'Test.*Service.*Network|Test.*Network.*Host' -count=1

Expected: FAIL because Service.Network currently accepts values and the name regex is unchanged.

- [ ] Step 3: Change `profileNameRe` to `^[a-z0-9][a-z0-9-]*$` (remove `._` from the character class). Keep every existing validation site that uses it (catalog segments, service names, socket names, profile names).
- [ ] Step 4: Change `Service.Network` back to `string` and add the rejection in validateServices. Add the `network: host` + services rejection in validate.
- [ ] Step 5: Add a merge test proving the existing service-entry replacement rule remains intact: a child service declaration replaces the parent service as a whole. Do not introduce field-by-field service merging.
- [ ] Step 6: Run package tests.

Run: go test ./internal/profile -count=1

Expected: PASS.

- [ ] Step 7: Commit.

~~~bash
git add internal/profile/types.go internal/profile/validate.go internal/profile/catalog.go internal/profile/types_test.go internal/profile/validate_test.go internal/profile/merge_multi_test.go
git commit -m "feat: restrict name grammar and make service networking implicit"
~~~

---

### Task 2: Define the managed-network contract

Files:

- Modify: internal/runtime/runtime.go
- Modify: internal/runtime/labels.go
- Modify: internal/runtime/fake.go
- Create: internal/runtime/labels_test.go

Interfaces:

- Produce these constants:

~~~go
const ServiceNetworkName = "tpd-services"
const NetworkRoleLabel = "tpd.network-role"
const NetworkRoleServices = "services"
~~~

- Produce ServiceNetworkAlias(serviceName string) string.
- Produce ServiceHostEnvName(serviceName string) string.
- Extend ServiceBindings with Network string.
- Extend Runtime with:

~~~go
ConnectContainerToNetwork(ctx context.Context, containerID, networkName string, aliases []string) error
RemoveContainer(ctx context.Context, containerID string) error
~~~

- FakeRuntime records ConnectedContainerID, ConnectedNetworkName, ConnectedNetworkAliases, RemovedContainerID, and configured errors.
- **No collision helpers.** The name grammar makes collisions impossible, so ServiceNetworkAliasCollision is not defined and no collision check exists. Alias = "tpd-svc-" + name (name already DNS-safe). Env name = "TPD_SERVICE_" + UPPER(name with - replaced by _) + "_HOST".

Changing runtime.Runtime is a deliberate breaking change for external Go embedders that implement the exported interface, even though tpd is primarily a CLI. Task 8 documents this compatibility impact in the existing service plan.

- [ ] Step 1: Write helper tests.

~~~go
func TestServiceNetworkAlias(t *testing.T) {
    if got := ServiceNetworkAlias("postgres-main"); got != "tpd-svc-postgres-main" {
        t.Fatalf("alias = %q", got)
    }
}

func TestServiceHostEnvName(t *testing.T) {
    if got := ServiceHostEnvName("postgres-main"); got != "TPD_SERVICE_POSTGRES_MAIN_HOST" {
        t.Fatalf("environment name = %q", got)
    }
}
~~~

Also test that the alias and env name are injective over a small sample of valid names (no two distinct valid names share an alias or env name).

- [ ] Step 2: Run tests to verify failure.

Run: go test ./internal/runtime -run 'TestService(NetworkAlias|HostEnvName)' -count=1

Expected: FAIL because helpers do not exist.

- [ ] Step 3: Add constants and helpers.

ServiceNetworkAlias returns "tpd-svc-" + serviceName unchanged. ServiceHostEnvName uppercases ASCII letters and replaces hyphen with underscore, wrapped in TPD_SERVICE_..._HOST.

- [ ] Step 4: Extend Runtime, ServiceBindings, and FakeRuntime.

FakeRuntime connection and removal methods record exact arguments before returning configured errors. Add compile-time interface assertions if the file's existing pattern uses them.

- [ ] Step 5: Run runtime tests and vet.

Run: go test ./internal/runtime -count=1

Expected: PASS.

Run: go vet ./internal/runtime

Expected: PASS.

- [ ] Step 6: Commit.

~~~bash
git add internal/runtime/runtime.go internal/runtime/labels.go internal/runtime/labels_test.go internal/runtime/fake.go
git commit -m "feat: define managed service network contract"
~~~

---

### Task 3: Create, validate, and connect the managed network

Files:

- Create: internal/runtime/docker_network.go
- Create: internal/runtime/docker_network_test.go

Interfaces:

- Consume constants and helpers from Task 2.
- Produce:

~~~go
func (d *DockerRuntime) ensureServiceNetwork(ctx context.Context) (string, error)
func (d *DockerRuntime) ConnectContainerToNetwork(ctx context.Context, containerID, networkName string, aliases []string) error
func (d *DockerRuntime) RemoveContainer(ctx context.Context, containerID string) error
~~~

- ensureServiceNetwork returns ServiceNetworkName after a valid create or reuse.

- [ ] Step 1: Extend the existing httptest daemon pattern with endpoints for network inspect, create, and connect.

Record the create request's name, driver, and labels. Record the connect request's container ID and aliases.

- [ ] Step 2: Write these tests:

~~~go
func TestEnsureServiceNetworkCreatesLabeledBridge(t *testing.T)
func TestEnsureServiceNetworkRecoversFromConcurrentCreateConflict(t *testing.T)
func TestEnsureServiceNetworkReusesOwnedBridge(t *testing.T)
func TestEnsureServiceNetworkRejectsUnownedCanonicalName(t *testing.T)
func TestEnsureServiceNetworkRejectsWrongRole(t *testing.T)
func TestEnsureServiceNetworkRejectsNonBridge(t *testing.T)
func TestConnectContainerToNetworkPassesAliases(t *testing.T)
~~~

The create test checks name tpd-services, driver bridge, and both exact labels. The conflict test returns 404 from the first inspect, 409 from create, then an owned valid bridge from the second inspect and expects success.

- [ ] Step 3: Run tests to verify failure.

Run: go test ./internal/runtime -run 'TestEnsureServiceNetwork|TestConnectContainerToNetwork' -count=1

Expected: FAIL because docker_network.go does not exist.

- [ ] Step 4: Implement inspect-or-create with the duplicate-create race handled.

Inspect ServiceNetworkName. If not found, call NetworkCreate with Driver "bridge" and both labels. If NetworkCreate returns conflict/409 because another launch won the race, inspect again and validate exactly as a reused network. Return an error naming the network and failed invariant for any other create error or invalid result.

- [ ] Step 5: Implement connection.

Call NetworkConnect with EndpointSettings.Aliases equal to a copy of aliases. Wrap errors with container and network names. Do not resolve the network gateway or container IP.

- [ ] Step 6: Implement explicit removal for pre-start failures.

RemoveContainer calls ContainerRemove with Force true and wraps errors. RunContainer retains its current deferred removal for containers that reach the run phase.

- [ ] Step 7: Run focused and package tests.

Run: go test ./internal/runtime -run 'TestEnsureServiceNetwork|TestConnectContainerToNetwork|TestCreateContainer|TestRunContainer' -count=1

Expected: PASS.

- [ ] Step 8: Commit.

~~~bash
git add internal/runtime/docker_network.go internal/runtime/docker_network_test.go
git commit -m "feat: manage persistent service bridge network"
~~~

---

### Task 4: Attach new and reused services with stable aliases

Files:

- Modify: internal/runtime/docker_services.go
- Modify: internal/runtime/docker_services_test.go

Interfaces:

- Consume ensureServiceNetwork and ConnectContainerToNetwork from Task 3.
- StartServices returns ServiceBindings{Network: ServiceNetworkName, Sockets: existingBindings, Release: existingRelease} whenever at least one service exists; otherwise Network is empty.
- Every service is attached with alias ServiceNetworkAlias(serviceName) on ServiceNetworkName, on create and on reuse.

- [ ] Step 1: Extend fakeServicesDaemon with network inspect/connect behavior and container network membership.

Record every network-connect call as container ID plus aliases.

- [ ] Step 2: Write a new-service test.

Start one service and assert:

~~~go
if bindings.Network != ServiceNetworkName {
    t.Fatalf("network = %q", bindings.Network)
}
if diff := cmp.Diff([]string{"tpd-svc-registry"}, daemon.connectAliases); diff != "" {
    t.Fatal(diff)
}
~~~

Keep the existing socket binding assertions in the same test.

- [ ] Step 3: Write reuse and repair tests.

One reused container already attached with the expected alias must not receive a redundant connect. One reused container with no service-network attachment must be connected. A connect failure must release acquired service locks and return an error.

- [ ] Step 4: Run tests to verify failure.

Run: go test ./internal/runtime -run 'TestStartServices.*Network|TestStartServices.*Reuse' -count=1

Expected: FAIL because StartServices does not ensure or attach the network.

- [ ] Step 5: Ensure the network before acquiring per-service locks.

Call ensureServiceNetwork once before acquiring any per-service lock (when services exist). The helper tolerates concurrent callers as specified in Task 3.

- [ ] Step 6: Attach each service.

For a newly created service, connect it with []string{ServiceNetworkAlias(name)} before reporting success. For a reused service, ensure the container is attached to tpd-services: inspect NetworkSettings.Networks and repair missing membership by connecting with the expected alias. Alias repair for an already-attached container is out of scope — tpd does not inspect every reused container solely to verify aliases. If an externally modified or legacy container lacks the expected alias, the stable discovery contract is not guaranteed; tpd doctor should report the malformed state, and the operator can recreate the service. Hold the existing service lock throughout.

- [ ] Step 7: Return network and socket bindings.

Every successful StartServices result containing at least one service includes Network: ServiceNetworkName. Error and empty-service returns initialize the network field, socket maps, and no-op Release functions safely.

- [ ] Step 8: Run all service tests.

Run: go test ./internal/runtime -run TestStartServices -count=1

Expected: PASS, including socket, hash replacement, rootful, pull, and lock tests.

- [ ] Step 9: Commit.

~~~bash
git add internal/runtime/docker_services.go internal/runtime/docker_services_test.go
git commit -m "feat: attach services with stable network aliases"
~~~

---

### Task 5: Generate built-in service host variables and consumer labels

Files:

- Modify: pkg/tpd/spec.go
- Modify: pkg/tpd/spec_test.go

Interfaces:

- Consume runtime.ServiceHostEnvName and runtime.ServiceNetworkAlias.
- Produce built-in environment entries for every declared service.
- tpd.uses-service includes every declared service, not only socket-mounted services.

- [ ] Step 1: Write a service test.

Build a profile with service postgres-main and no service socket mount. Assert:

~~~go
if got := spec.Env["TPD_SERVICE_POSTGRES_MAIN_HOST"]; got != "tpd-svc-postgres-main" {
    t.Fatalf("host = %q", got)
}
if got := spec.Labels[runtime.UsesServiceLabel]; got != "postgres-main" {
    t.Fatalf("uses-service = %q", got)
}
~~~

- [ ] Step 2: Write multiple-service and user-collision tests.

Use service names alpha and postgres-main. Assert sorted tpd.uses-service value "alpha,postgres-main" and both normalized variables. Add user environment TPD_SERVICE_ALPHA_HOST=custom and assert buildSpec returns an error naming the reserved variable. Add a socket-only service (no exposes) and assert it still receives a TPD_SERVICE_*_HOST variable (networking is always on).

- [ ] Step 3: Run focused tests to verify failure.

Run: go test ./pkg/tpd -run 'TestBuildSpec.*Service(Host|Label|Collision)' -count=1

Expected: FAIL because variables are absent.

- [ ] Step 4: Generate host variables in buildSpec.

While converting services, iterate declared services in sorted name order, reject any generated key already present in cfg.Env (naming the reserved variable), and set the value to runtime.ServiceNetworkAlias(name). Add every declared service name to usedServices before constructing tpd.uses-service.

- [ ] Step 5: Keep socket behavior unchanged.

Existing service/socket mount conversion and missing-binding validation must remain byte-for-byte compatible except for the additional built-in environment entries and complete consumer label.

- [ ] Step 6: Run spec tests.

Run: go test ./pkg/tpd -run 'TestBuildSpec' -count=1

Expected: PASS.

- [ ] Step 7: Commit.

~~~bash
git add pkg/tpd/spec.go pkg/tpd/spec_test.go
git commit -m "feat: inject service host variables"
~~~

---

### Task 6: Attach consumers before start and clean up failures

Files:

- Modify: pkg/tpd/launch.go
- Modify: pkg/tpd/launch_test.go

Interfaces:

- Consume ServiceBindings.Network, ConnectContainerToNetwork, and RemoveContainer.
- Produce exact launch order: StartServices, CreateContainer, ConnectContainerToNetwork, Release, RunContainer.
- No-service launches do not call ConnectContainerToNetwork.

- [ ] Step 1: Extend orderRuntime.

Record start-services, create, connect, remove, release, and run events. Wrap ServiceBindings.Release in tests so release is observable.

- [ ] Step 2: Update existing exact-order tests and write the new success ordering test.

Configure ServiceBindings.Network = "tpd-services", launch one service, and assert:

~~~go
want := []string{"start-services", "create", "connect", "release", "run"}
~~~

Also assert connect receives created.ContainerID, tpd-services, and nil aliases.

Add a network-capable fixture (a profile with a service) for connection tests. Update TestLaunchWithServices to include connect between create-container and release. Retain a no-service launch test without connect. Retain the create-error ordering in TestLaunchStopsServicesOnCreateError because CreateContainer fails before attachment. Update the run-error test so successful attachment appears before release and run-container.

- [ ] Step 3: Write failure tests.

When connect returns an error, assert RunContainer is not called, RemoveContainer receives the created ID, service locks are released, and the launch error includes "connect service network". When RemoveContainer also fails, assert the attachment failure remains primary and cleanup failure is included or emitted as a warning according to existing error style.

- [ ] Step 4: Write no-network-service tests.

Launch a profile with no services and assert no network connection or explicit pre-start removal occurs. Launch a socket-only service profile and assert ServiceBindings.Network is "tpd-services" and the consumer network connection occurs, while its socket mount lifecycle remains unchanged.

- [ ] Step 5: Run tests to verify failure.

Run: go test ./pkg/tpd -run 'TestLaunch.*ServiceNetwork' -count=1

Expected: FAIL because launch currently proceeds directly from create to run.

- [ ] Step 6: Connect after create and before release.

If serviceBindings.Network is non-empty, call rt.ConnectContainerToNetwork(ctx, created.ContainerID, serviceBindings.Network, nil). On failure, force-remove the created container, release locks, and return without RunContainer.

- [ ] Step 7: Preserve all existing cleanup.

Keep deferred StopServices registration before StartServices. Keep missing socket handling. Release locks once on every path. RunContainer owns cleanup only after successful network attachment.

- [ ] Step 8: Run launch tests.

Run: go test ./pkg/tpd -run TestLaunch -count=1

Expected: PASS.

- [ ] Step 9: Commit.

~~~bash
git add pkg/tpd/launch.go pkg/tpd/launch_test.go
git commit -m "feat: attach consumers before container start"
~~~

---

### Task 7: Add safe network pruning

Files:

- Modify: internal/prune/prune.go
- Modify: internal/prune/prune_test.go
- Modify: cmd/tpd/cli.go
- Modify: cmd/tpd/cli_test.go

Interfaces:

- Add Networks bool to prune.Options.
- Add NetworksRemoved []string to prune.Result.
- Extend prune dockerClient with NetworkList and NetworkRemove.
- --networks selects networks only; --volumes --networks selects both; --images --networks selects both; all three selects all three. With no type flags, preserve the current default of volumes and images only. --all never implies network scope.

- [ ] Step 1: Extend fakeClient with network summaries, removal records, and errors.

Include running container inspect data with NetworkSettings.Networks entries.

- [ ] Step 2: Write prune safety tests.

Add:

~~~go
func TestPruneNetworksRequiresNetworkScope(t *testing.T)
func TestPruneNetworkScopeCombinations(t *testing.T)
func TestPruneNetworksUsesConfirmationPrompt(t *testing.T)
func TestPruneNetworksSkipsUnmanagedCanonicalName(t *testing.T)
func TestPruneNetworksSkipsWrongRole(t *testing.T)
func TestPruneNetworksSkipsRunningReference(t *testing.T)
func TestPruneNetworksRemovesOwnedUnusedNetwork(t *testing.T)
~~~

The running-reference test uses an unlabeled running container to prove ownership does not weaken protection.

- [ ] Step 3: Run tests to verify failure.

Run: go test ./internal/prune -run TestPruneNetworks -count=1

Expected: FAIL because network scope and API methods do not exist.

- [ ] Step 4: Implement scope selection and confirmation.

A supplied --networks flag sets scopeNetworks true. Compute volume/image scopes so a type flag selects exactly the requested types, with the special existing default that no type flags means volumes and images. --all relaxes catalog liveness only and does not imply scopeNetworks or bypass ownership/running-reference protection. Pass selected networks through the same confirm(kind, items, stdin) prompt used for volumes and images unless --force/--yes is set.

- [ ] Step 5: Select only the owned canonical network.

List networks and select only name tpd-services with OwnershipLabel == "true" and NetworkRoleLabel == NetworkRoleServices. Report same-name unowned networks as not tpd-owned and never remove them.

- [ ] Step 6: Protect running references and remove safely.

Do not reuse runningContainerRefs: it deliberately filters for tpd.managed=true. Add a separate unfiltered running-container scan, inspect each container, and collect every network ID/name in NetworkSettings.Networks. Use it once before presenting confirmation, then repeat it after confirmation immediately before each removal so a container started while the prompt was open is protected. Skip selected networks in use, including references from unlabeled containers. Remove remaining networks by ID and append tpd-services to NetworksRemoved.

- [ ] Step 7: Wire CLI flag and output.

Add --networks help, pass Networks into prune.Options, print removed network count/names, and include NetworksRemoved in the "Nothing to prune" condition. Update command Short text to include networks.

- [ ] Step 8: Run prune and CLI tests.

Run: go test ./internal/prune ./cmd/tpd -run 'Test.*Prune' -count=1

Expected: PASS.

- [ ] Step 9: Commit.

~~~bash
git add internal/prune/prune.go internal/prune/prune_test.go cmd/tpd/cli.go cmd/tpd/cli_test.go
git commit -m "feat: safely prune managed service network"
~~~

---

### Task 8: Add doctor diagnostics and real-engine validation

Files:

- Modify: internal/doctor/checks.go
- Modify: internal/doctor/checks_test.go
- Modify: cmd/tpd/e2e_runtime_test.go
- Modify: docs/superpowers/plans/2026-08-04-services.md

Interfaces:

- Doctor adds a service network check.
- E2E test proves stable aliases, same-port services, and consumer attachment on an available engine.

- [ ] Step 1: Extend fakeDocker with network list/inspect responses.

Represent absent, valid bridge, wrong labels, wrong driver, and connected-container cases.

- [ ] Step 2: Write doctor tests.

Absent network returns Info. A valid owned bridge returns Pass and reports connected count. Wrong ownership or driver returns Warn or Fail with the canonical network name and failed invariant.

- [ ] Step 3: Implement checkServiceNetwork.

Add it after runtime reachability/rootless checks. Use Docker network API data only; do not probe DNS by creating a diagnostic container.

- [ ] Step 4: Run doctor tests.

Run: go test ./internal/doctor -run 'Test.*ServiceNetwork' -count=1

Expected: PASS.

- [ ] Step 5: Add a guarded e2e scenario.

Following the existing e2e engine guard, create temporary services named svc-one and svc-two, both listening on container port 8080. Launch a consumer and assert tpd-svc-svc-one and tpd-svc-svc-two return their distinct responses. Recreate one service and assert a new consumer reaches the replacement through the unchanged alias.

- [ ] Step 6: Add the nested validation boundary.

Document in the e2e test or adjacent design doc that nested child dynamic-port lookup belongs to the nested engine API. The host validation already proved alias plus nested published port; do not embed privileged nested Podman in the default e2e suite.

- [ ] Step 7: Cross-link service documentation.

Add a short section to docs/superpowers/plans/2026-08-04-services.md linking the new specification, stating that socket mounts remain compatible while consumers use TPD_SERVICE_<NAME>_HOST, and noting that adding ConnectContainerToNetwork and RemoveContainer to runtime.Runtime is a breaking interface change for external Go implementations.

- [ ] Step 8: Run doctor and e2e tests.

Run: go test ./internal/doctor ./cmd/tpd -run 'Test.*(ServiceNetwork|Service.*Alias)' -count=1 -v

Expected: PASS with a compatible engine, or an explicit e2e skip without one.

- [ ] Step 9: Commit.

~~~bash
git add internal/doctor/checks.go internal/doctor/checks_test.go cmd/tpd/e2e_runtime_test.go docs/superpowers/plans/2026-08-04-services.md
git commit -m "test: validate managed service discovery"
~~~

---

### Task 9: Full verification

Files:

- Modify no source files unless verification reveals a regression.

- [ ] Step 1: Search for contract drift.

Run:

~~~bash
rg -n 'tpd-services|tpd.network-role|TPD_SERVICE_|ConnectContainerToNetwork|NetworksRemoved|network: true|network: false' internal pkg cmd docs
~~~

Expected: one canonical spelling for each network name, label, alias, variable, and interface; no lingering `network: true`/`network: false` service opt-in in code or docs.

- [ ] Step 2: Format changed Go files.

Run:

~~~bash
gofmt -w internal/profile/types.go internal/profile/validate.go internal/profile/catalog.go internal/profile/types_test.go internal/profile/validate_test.go internal/profile/merge_multi_test.go internal/runtime/runtime.go internal/runtime/labels.go internal/runtime/labels_test.go internal/runtime/fake.go internal/runtime/docker_network.go internal/runtime/docker_network_test.go internal/runtime/docker_services.go internal/runtime/docker_services_test.go pkg/tpd/spec.go pkg/tpd/spec_test.go pkg/tpd/launch.go pkg/tpd/launch_test.go internal/prune/prune.go internal/prune/prune_test.go cmd/tpd/cli.go cmd/tpd/cli_test.go internal/doctor/checks.go internal/doctor/checks_test.go cmd/tpd/e2e_runtime_test.go
~~~

Expected: only Go files already changed by this feature are formatted; unrelated dirty files are not touched.

- [ ] Step 3: Run full tests.

Run: go test ./...

Expected: PASS.

- [ ] Step 4: Run vet.

Run: go vet ./...

Expected: PASS.

- [ ] Step 5: Inspect the worktree.

Run: git status --short and git diff --check. Verify feature edits do not overwrite unrelated user changes.

- [ ] Step 6: If verification finds a defect, return to the owning task, add a focused regression test, and commit only that task's exact files with its feature commit. If verification passes, create no extra commit.

## Self-review checklist

- Tasks 1 through 6 cover implicit always-on networking, stable aliases, built-in HOST variables, direct and nested service reachability, lifecycle ordering, service recreation, socket compatibility, and pre-start failure.
- Task 7 covers explicit conservative pruning.
- Task 8 covers diagnostics, real-engine behavior, and documentation.
- The runtime method signatures and ServiceBindings.Network field are introduced before their consumers.
- No port/endpoint schema, proxy, watcher, firewall rule, per-service network, guessed IP, or guessed port is introduced.
- Names are restricted to letters, numbers, and dashes, making collisions impossible; no collision-check code exists.
- A stray service `network:` key and a `network: host` + services combination are both rejected at validation.
- Every task has exact files, interfaces, focused tests, commands, expected outcomes, and a commit.
- Existing dirty worktree changes remain untouched.
