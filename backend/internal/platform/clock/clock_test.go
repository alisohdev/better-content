package clock

import (
	"testing"
	"time"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

func TestClockInterfaceSupportsDeterministicTime(t *testing.T) {
	want := time.Date(2026, time.October, 25, 12, 30, 0, 0, time.UTC)
	var testClock Clock = fixedClock{now: want}
	if got := testClock.Now(); !got.Equal(want) {
		t.Fatalf("Now() = %s, want %s", got, want)
	}
}
