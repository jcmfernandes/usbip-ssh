package main

import "testing"

func TestBackoff(t *testing.T) {
	cases := []struct {
		wait, dt    float64
		sleep, next float64
	}{
		{0.5, 0.1, 0.5, 2}, // fast failure: sleep wait, quadruple
		{2, 0.1, 2, 8},     // still failing fast
		{16, 0.1, 16, 60},  // capped at maxWait
		{60, 30, 2, 60},    // failing after 30s: sleep 60/30
		{60, 61, 0, 0.5},   // ran longer than maxWait: reset
		{0.5, 5, 0, 2},     // dt >= wait: no sleep
	}
	for _, c := range cases {
		if got := sleepAfter(c.wait, c.dt); got != c.sleep {
			t.Errorf("sleepAfter(%v, %v) = %v, want %v", c.wait, c.dt, got, c.sleep)
		}
		if got := nextWait(c.wait, c.dt); got != c.next {
			t.Errorf("nextWait(%v, %v) = %v, want %v", c.wait, c.dt, got, c.next)
		}
	}
}
