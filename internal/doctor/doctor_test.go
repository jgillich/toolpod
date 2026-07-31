package doctor

import "testing"

func TestCheckPassFormat(t *testing.T) {
	c := Check{Name: "runtime", Status: Pass, Message: "docker 27.0 at unix:///var/run/docker.sock"}
	if c.Format() != "[pass] runtime: docker 27.0 at unix:///var/run/docker.sock" {
		t.Errorf("Format() = %q", c.Format())
	}
}

func TestCheckWarnFormat(t *testing.T) {
	c := Check{Name: "mise base image", Status: Warn, Message: "present"}
	if c.Format() != "[warn] mise base image: present" {
		t.Errorf("Format() = %q", c.Format())
	}
}

func TestCheckSkipFormat(t *testing.T) {
	c := Check{Name: "mise functional", Status: Skip, Message: "base image not yet pulled"}
	if c.Format() != "[skip] mise functional: base image not yet pulled" {
		t.Errorf("Format() = %q", c.Format())
	}
}

func TestResultSummary(t *testing.T) {
	r := Result{Checks: []Check{
		{Status: Pass},
		{Status: Warn},
		{Status: Pass},
		{Status: Skip},
	}}
	summary := r.Summary()
	if summary != "1 warning, all critical checks passed." {
		t.Errorf("Summary() = %q, want '1 warning, all critical checks passed.'", summary)
	}
}

func TestResultSummaryWithFailure(t *testing.T) {
	r := Result{Checks: []Check{
		{Status: Pass},
		{Status: Fail, Message: "docker daemon not running"},
	}}
	summary := r.Summary()
	if summary != "1 failure." {
		t.Errorf("Summary() = %q, want '1 failure.'", summary)
	}
}
