package domain

import "testing"

func TestDeviceLifecycleTransitions(t *testing.T) {
	cases := []struct {
		from, to DeviceLifecycle
		valid    bool
	}{
		{DeviceLifecycleActive, DeviceLifecycleDisabled, true},
		{DeviceLifecycleActive, DeviceLifecycleRetired, true},
		{DeviceLifecycleDisabled, DeviceLifecycleActive, true},
		{DeviceLifecycleDisabled, DeviceLifecycleRetired, true},
		{DeviceLifecycleRetired, DeviceLifecycleActive, false},
		{DeviceLifecycleRetired, DeviceLifecycleDisabled, false},
		{DeviceLifecycleDisabled, DeviceLifecycleDisabled, false},
	}
	for _, tc := range cases {
		if got := ValidateDeviceLifecycleTransition(tc.from, tc.to) == nil; got != tc.valid {
			t.Errorf("transition %s -> %s valid=%t, got %t", tc.from, tc.to, tc.valid, got)
		}
	}
}
