package billingestimate

import (
	"testing"
	"time"
)

func TestSeasonForCalendarMonth(t *testing.T) {
	for month := time.January; month <= time.December; month++ {
		want := SeasonNonSummer
		if month >= time.June && month <= time.September {
			want = SeasonSummer
		}
		if got := SeasonForMonth(month); got != want {
			t.Fatalf("month=%s season=%s want=%s", month, got, want)
		}
	}
}
