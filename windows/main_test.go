package main

import (
	"testing"

	"signallight/config"
)

func TestShouldOpenDashboardAfterInstall(t *testing.T) {
	paired := &config.Config{Paired: true, TargetMAC: "E8:F6:0A:BE:69:F5"}
	unpaired := &config.Config{}

	cases := []struct {
		name          string
		fromInstaller bool
		cfg           *config.Config
		want          bool
	}{
		{"fresh install, nothing paired", true, unpaired, true},
		{"upgrade with a paired light", true, paired, false},
		{"paired flag set but no MAC saved", true, &config.Config{Paired: true}, true},
		{"normal start at sign-in, unpaired", false, unpaired, false},
		{"normal start at sign-in, paired", false, paired, false},
	}
	for _, c := range cases {
		if got := shouldOpenDashboardAfterInstall(c.fromInstaller, c.cfg); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
