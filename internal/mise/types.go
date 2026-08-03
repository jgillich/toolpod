package mise

// Tool is a mise tool as seen by the runtime: the version plus optional
// verification metadata. SHA256 is a universal digest; SHA256ByArch keys are
// the schema's arch set ("amd64", "aarch64"), which the appimage backend maps
// its RUNTIME.archType to. At most one form is set.
type Tool struct {
	Version      string
	SHA256       string
	SHA256ByArch map[string]string
}
