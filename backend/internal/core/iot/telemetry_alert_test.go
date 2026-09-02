package iot

import (
	"testing"
)

func TestCurfewWindowSupportsSameDayAndOvernightRanges(t *testing.T) {
	for _, test := range []struct {
		name, now, start, end string
		want                  bool
	}{
		{"same-day inside", "10:30", "10:00", "11:00", true},
		{"same-day outside", "11:01", "10:00", "11:00", false},
		{"overnight late", "23:30", "22:00", "06:00", true},
		{"overnight early", "05:59", "22:00", "06:00", true},
		{"overnight outside", "12:00", "22:00", "06:00", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := inCurfewWindow(test.now, test.start, test.end); got != test.want {
				t.Fatalf("inCurfewWindow(%q,%q,%q)=%t, want %t", test.now, test.start, test.end, got, test.want)
			}
		})
	}
}
