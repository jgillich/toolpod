package profile

import "strconv"

// ProfileError is a profile-layer error (parse, merge, validation)
// carrying the source file path and line for reporting (spec §10: exit code 2).
type ProfileError struct {
	Path    string
	Line    int
	Message string
}

func (e ProfileError) Error() string {
	if e.Line > 0 {
		return e.Path + ":" + strconv.Itoa(e.Line) + ": " + e.Message
	}
	return e.Path + ": " + e.Message
}

// ExitCode returns the exit code for this error type (spec §10: config errors = 2).
func (e ProfileError) ExitCode() int { return 2 }

// ExitCoder is satisfied by errors that carry a process exit code (spec §10).
type ExitCoder interface {
	Error() string
	ExitCode() int
}
