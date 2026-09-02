package alerts

import "testing"

func TestAlertSettingsValidationRequiresCompleteHHMMWindow(t *testing.T) {
	if !validUpdate(SettingsUpdate{NonUsageStartTime: "22:00", NonUsageEndTime: "06:00", IsEnabled: true}) {
		t.Fatal("valid overnight window rejected")
	}
	for _, value := range []SettingsUpdate{{NonUsageStartTime: "22:00"}, {NonUsageStartTime: "2:00", NonUsageEndTime: "06:00"}, {NonUsageStartTime: "22:00", NonUsageEndTime: "60:00"}} {
		if validUpdate(value) {
			t.Fatalf("invalid window accepted: %+v", value)
		}
	}
}
