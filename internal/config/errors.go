package config

import "strconv"

// ConfigError is a config-layer error (parse, merge, validation)
// carrying the source file path and line for reporting (spec §10: exit code 2).
type ConfigError struct {
	Path    string
	Line    int
	Message string
}

func (e ConfigError) Error() string {
	if e.Line > 0 {
		return e.Path + ":" + strconv.Itoa(e.Line) + ": " + e.Message
	}
	return e.Path + ": " + e.Message
}

// ExitCode returns the exit code for this error type (spec §10: config errors = 2).
func (e ConfigError) ExitCode() int { return 2 }
