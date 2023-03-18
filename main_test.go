package main

import (
	"testing"
	"time"
)

func TestRoughDuration(t *testing.T) {
	tests := []struct {
		x    time.Duration
		want string
	}{
		{0, "0"},
		{10 * time.Second, "10s"},
		{45*time.Minute + 21*time.Second + 150*time.Millisecond, "45m21s"},
		{3*time.Hour + 21*time.Second, "3h0m"},
		{25 * time.Hour, "1d1h"},
		{51*time.Hour + 6*time.Minute, "2d3h"},
	}
	for _, test := range tests {
		got := roughDuration(test.x)
		if got != test.want {
			t.Errorf("roughDuration(%v) = %q, want %q", test.x, got, test.want)
		}
	}
}
