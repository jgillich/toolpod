package mise

// Tool is a mise tool as seen by the runtime: the version plus optional
// verification metadata. SHA256 is a universal digest; SHA256ByArch keys are
// the appimage backend's RUNTIME.archType values. At most one form is set.
type Tool struct {
	Version      string
	SHA256       string
	SHA256ByArch map[string]string
}
