package alerts

import "testing"

func TestAlertSettingsValidationUsesV1Policy(t *testing.T) {
	valid := SettingsUpdate{IsEnabled: true, QuietHoursStart: "22:00", QuietHoursEnd: "06:00", PowerThresholdW: 10}
	if !validUpdate(valid) {
		t.Fatal("valid overnight policy rejected")
	}
	for _, value := range []SettingsUpdate{
		{PowerThresholdW: 10, QuietHoursStart: "22:00"},
		{PowerThresholdW: 10, QuietHoursStart: "2:00", QuietHoursEnd: "06:00"},
		{PowerThresholdW: 10, QuietHoursStart: "22:00", QuietHoursEnd: "22:00"},
		{PowerThresholdW: 0, QuietHoursStart: "22:00", QuietHoursEnd: "06:00"},
	} {
		if validUpdate(value) {
			t.Fatalf("invalid policy accepted: %+v", value)
		}
	}
}
