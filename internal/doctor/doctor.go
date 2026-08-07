package doctor

import (
	"context"
	"strconv"
	"time"
)

type Status int

const (
	Pass Status = iota
	Fail
	Warn
	Info
	Skip
)

func (s Status) String() string {
	switch s {
	case Pass:
		return "pass"
	case Fail:
		return "fail"
	case Warn:
		return "warn"
	case Info:
		return "info"
	case Skip:
		return "skip"
	}
	return "unknown"
}

type Check struct {
	Name    string
	Status  Status
	Message string
}

func (c Check) Format() string {
	return "[" + c.Status.String() + "] " + c.Name + ": " + c.Message
}

type Result struct {
	Checks []Check
}

func (r Result) Summary() string {
	fails, warns := 0, 0
	for _, c := range r.Checks {
		switch c.Status {
		case Fail:
			fails++
		case Warn:
			warns++
		}
	}
	if fails > 0 {
		return pluralize(fails, "failure") + "."
	}
	if warns > 0 {
		return pluralize(warns, "warning") + ", all critical checks passed."
	}
	return "all checks passed."
}

func pluralize(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func (r Result) HasFailure() bool {
	for _, c := range r.Checks {
		if c.Status == Fail {
			return true
		}
	}
	return false
}

type Options struct {
	Workspace  string
	ProfileDir string
}

// doctorTimeout bounds the whole diagnostic run so a hung daemon cannot block
// tpd doctor; individual daemon calls are additionally bounded by the runtime
// package's daemonHTTPTimeout. A var so tests can shrink it.
var doctorTimeout = 30 * time.Second

func Run(ctx context.Context, opts Options) Result {
	rt, err := newRuntime()
	if err != nil {
		return Result{Checks: []Check{
			{Name: "runtime", Status: Fail, Message: "cannot connect to Docker: " + err.Error()},
		}}
	}
	ctx, cancel := context.WithTimeout(ctx, doctorTimeout)
	defer cancel()
	return runChecks(ctx, rt, opts)
}
