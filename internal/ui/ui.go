package ui

import (
	"io"
	"os"

	"golang.org/x/term"
)

type Output struct {
	isTTY bool
}

func NewOutput(isTTY bool) *Output {
	return &Output{isTTY: isTTY}
}

func IsTTY(w io.Writer) bool {
	if f, ok := w.(*os.File); ok {
		return term.IsTerminal(int(f.Fd()))
	}
	return false
}

func (o *Output) Color(color, msg string) string {
	if !o.isTTY {
		return msg
	}
	switch color {
	case "green":
		return "\033[32m" + msg + "\033[0m"
	case "red":
		return "\033[31m" + msg + "\033[0m"
	case "yellow":
		return "\033[33m" + msg + "\033[0m"
	case "blue":
		return "\033[34m" + msg + "\033[0m"
	case "reset":
		return "\033[0m" + msg
	default:
		return msg
	}
}
