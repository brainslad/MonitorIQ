package store

import (
	"testing"

	"github.com/brainslad/monitoriq/internal/checks"
)

func TestNextState(t *testing.T) {
	// retries=1: first failure keeps previous status, second marks down.
	st, f := NextState(checks.Up, 0, 1, checks.Down)
	if st != checks.Up || f != 1 {
		t.Fatalf("first failure: %v %d", st, f)
	}
	st, f = NextState(st, f, 1, checks.Down)
	if st != checks.Down || f != 2 {
		t.Fatalf("second failure: %v %d", st, f)
	}
	st, f = NextState(st, f, 1, checks.Up)
	if st != checks.Up || f != 0 {
		t.Fatalf("recovery: %v %d", st, f)
	}
	st, _ = NextState(checks.Up, 0, 0, checks.Down)
	if st != checks.Down {
		t.Fatalf("retries=0 should go down immediately")
	}
	st, _ = NextState(checks.Up, 0, 3, checks.Degraded)
	if st != checks.Degraded {
		t.Fatalf("degraded applies immediately")
	}
}

func TestRetentionClamp(t *testing.T) {
	r := Retention{RawDays: 0, AggregateDays: 5000, EventsDays: 30}
	r.clamp()
	if r.RawDays != 1 || r.AggregateDays != MaxRetentionDays || r.EventsDays != 30 {
		t.Fatalf("clamp: %+v", r)
	}
}
