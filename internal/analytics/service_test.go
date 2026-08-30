package analytics

import (
	"testing"
	"time"
)

// The divide-by-zero rule is the one most likely to be got wrong, and the one
// a user would notice: every new business would otherwise show fake growth.
func TestNewMetricLeavesChangeNilWhenPreviousIsZero(t *testing.T) {
	m := newMetric(5000, 0)

	if m.Value != 5000 {
		t.Errorf("Value = %v, want 5000", m.Value)
	}
	if m.Change != nil {
		t.Errorf("Change = %v, want nil — a change from zero is undefined, not +100%%", *m.Change)
	}
	if m.Direction != "" {
		t.Errorf("Direction = %q, want empty when there is no change", m.Direction)
	}
}

func TestNewMetricDirections(t *testing.T) {
	tests := []struct {
		name              string
		current, previous float64
		wantChange        float64
		wantDirection     string
	}{
		{"growth", 120, 100, 20, "up"},
		{"decline", 80, 100, -20, "down"},
		{"flat", 100, 100, 0, "flat"},
		{"to zero", 0, 100, -100, "down"},
		{"rounds to one decimal", 105.27, 100, 5.3, "up"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newMetric(tc.current, tc.previous)
			if m.Change == nil {
				t.Fatal("Change is nil, want a value")
			}
			if *m.Change != tc.wantChange {
				t.Errorf("Change = %v, want %v", *m.Change, tc.wantChange)
			}
			if m.Direction != tc.wantDirection {
				t.Errorf("Direction = %q, want %q", m.Direction, tc.wantDirection)
			}
		})
	}
}

// A negative previous value would otherwise flip the sign of the change.
func TestNewMetricUsesMagnitudeOfPrevious(t *testing.T) {
	m := newMetric(-50, -100)
	if m.Change == nil {
		t.Fatal("Change is nil")
	}
	// Moving from -100 to -50 is an increase, so the change must be positive.
	if *m.Change != 50 || m.Direction != "up" {
		t.Errorf("got change=%v direction=%q, want 50/up", *m.Change, m.Direction)
	}
}

func TestClampMonths(t *testing.T) {
	tests := []struct{ in, want int }{
		{0, defaultMonths}, {-5, defaultMonths}, {1, 1}, {6, 6}, {24, 24}, {25, maxMonths}, {9999, maxMonths},
	}
	for _, tc := range tests {
		if got := clampMonths(tc.in); got != tc.want {
			t.Errorf("clampMonths(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestClampLimit(t *testing.T) {
	tests := []struct{ in, want int }{
		{0, defaultLimit}, {-1, defaultLimit}, {5, 5}, {20, 20}, {21, maxLimit}, {1000, maxLimit},
	}
	for _, tc := range tests {
		if got := clampLimit(tc.in); got != tc.want {
			t.Errorf("clampLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestStartOfMonth(t *testing.T) {
	in := time.Date(2026, 8, 30, 14, 33, 21, 999, time.UTC)
	got := startOfMonth(in)
	want := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("startOfMonth = %v, want %v", got, want)
	}
}

// The chart x-axis expects "Jan", not "2026-01".
func TestMonthLabel(t *testing.T) {
	tests := []struct{ in, want string }{
		{"2026-01", "Jan"}, {"2026-06", "Jun"}, {"2026-12", "Dec"},
		{"garbage", "garbage"}, // malformed input passes through rather than panicking
	}
	for _, tc := range tests {
		if got := monthLabel(tc.in); got != tc.want {
			t.Errorf("monthLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Segment bounds must stay strictly descending, or the SQL FILTER ranges
// silently overlap and customers get counted twice.
func TestSegmentBoundsAreStrictlyDescending(t *testing.T) {
	for i := 1; i < len(segmentBounds); i++ {
		if segmentBounds[i] >= segmentBounds[i-1] {
			t.Fatalf("bounds not descending at %d: %v", i, segmentBounds)
		}
	}
}

func TestRounding(t *testing.T) {
	if got := round1(5.27); got != 5.3 {
		t.Errorf("round1(5.27) = %v, want 5.3", got)
	}
	if got := round2(1234.567); got != 1234.57 {
		t.Errorf("round2(1234.567) = %v, want 1234.57", got)
	}
}
