package iot

import (
	"testing"
	"time"
)

func TestCurfewConditionUsesAsiaTaipeiAndThreshold(t *testing.T) {
	start := time.Date(2026, 1, 1, 15, 0, 0, 0, time.UTC) // 23:00 Asia/Taipei
	active, err := curfewCondition(start, "23:00", "01:00", 20, 10)
	if err != nil || !active {
		t.Fatalf("start boundary active=%v err=%v", active, err)
	}
	end := start.Add(2 * time.Hour) // 01:00 Asia/Taipei, exclusive
	active, err = curfewCondition(end, "23:00", "01:00", 20, 10)
	if err != nil || active {
		t.Fatalf("end boundary active=%v err=%v", active, err)
	}
	active, err = curfewCondition(start, "23:00", "01:00", 10, 10)
	if err != nil || active {
		t.Fatalf("threshold boundary active=%v err=%v", active, err)
	}
}

func TestCurfewWindowSupportsSameDayAndOvernightRanges(t *testing.T) {
	for _, test := range []struct {
		name, now, start, end string
		want                  bool
	}{
		{"same-day inside", "10:30", "10:00", "11:00", true},
		{"same-day outside", "11:01", "10:00", "11:00", false},
		{"overnight late", "23:30", "22:00", "06:00", true},
		{"overnight early", "05:59", "22:00", "06:00", true},
		{"end boundary excluded", "06:00", "22:00", "06:00", false},
		{"overnight outside", "12:00", "22:00", "06:00", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := inCurfewWindow(test.now, test.start, test.end); got != test.want {
				t.Fatalf("inCurfewWindow(%q,%q,%q)=%t, want %t", test.now, test.start, test.end, got, test.want)
			}
		})
	}
}
