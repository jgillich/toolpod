package ui

import (
	"io"
	"os"

	"golang.org/x/term"
)

// IsTTYReader reports whether r is an interactive terminal.
func IsTTYReader(r io.Reader) bool {
	if f, ok := r.(*os.File); ok {
		return term.IsTerminal(int(f.Fd()))
	}
	return false
}
