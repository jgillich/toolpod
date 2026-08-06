package runtime

import "context"

// FakeRuntime is a test helper that records runtime calls. Exported so
// pkg/tpd tests can import it without redefining.
type FakeRuntime struct {
	PreparedSpec            *Spec
	PreparePull             bool
	PrepareErr              error
	PrepareImage            string
	CreatedSpec             *Spec
	CreateResult            CreateResult
	CreateErr               error
	RanSpec                 *Spec
	RunErr                  error
	ExitCode                int
	StartServicesSpec       *Spec
	StartServicesPull       bool
	StartServicesErr        error
	ServiceBindings         ServiceBindings
	StopServicesSpec        *Spec
	StopServicesErr         error
	ConnectedContainerID    string
	ConnectedNetworkName    string
	ConnectedNetworkAliases []string
	ConnectErr              error
	RemovedContainerID      string
	RemoveErr               error
}

func (f *FakeRuntime) Prepare(ctx context.Context, spec Spec, w ProgressWriter, pull bool) (string, error) {
	f.PreparedSpec = &spec
	f.PreparePull = pull
	return f.PrepareImage, f.PrepareErr
}

func (f *FakeRuntime) CreateContainer(ctx context.Context, spec Spec) (CreateResult, error) {
	f.CreatedSpec = &spec
	return f.CreateResult, f.CreateErr
}

func (f *FakeRuntime) RunContainer(ctx context.Context, spec Spec, created CreateResult) (int, error) {
	f.RanSpec = &spec
	return f.ExitCode, f.RunErr
}

func (f *FakeRuntime) StartServices(ctx context.Context, spec Spec, w ProgressWriter, pull bool) (ServiceBindings, error) {
	f.StartServicesSpec = &spec
	f.StartServicesPull = pull
	return f.ServiceBindings, f.StartServicesErr
}

func (f *FakeRuntime) StopServices(ctx context.Context, spec Spec) error {
	f.StopServicesSpec = &spec
	return f.StopServicesErr
}

func (f *FakeRuntime) ConnectContainerToNetwork(ctx context.Context, containerID, networkName string, aliases []string) error {
	f.ConnectedContainerID = containerID
	f.ConnectedNetworkName = networkName
	f.ConnectedNetworkAliases = aliases
	return f.ConnectErr
}

func (f *FakeRuntime) RemoveContainer(ctx context.Context, containerID string) error {
	f.RemovedContainerID = containerID
	return f.RemoveErr
}
