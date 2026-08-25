package billingestimate

import "time"

const (
	SeasonSummer    = "SUMMER"
	SeasonNonSummer = "NON_SUMMER"
)

func SeasonForMonth(month time.Month) string {
	if month >= time.June && month <= time.September {
		return SeasonSummer
	}
	return SeasonNonSummer
}
