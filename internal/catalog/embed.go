package catalog

import "embed"

//go:embed configs/*.yaml
var Configs embed.FS
