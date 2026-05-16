package main

import "testing"

func TestIsForbiddenReleaseEntry(t *testing.T) {
	cases := []struct {
		rel  string
		want bool
	}{
		{rel: "settings.json", want: true},
		{rel: "docs/settings.json", want: true},
		{rel: "settings.example.json", want: false},
		{rel: "engine/state/", want: false},
		{rel: "engine/state/limitdock.pid", want: true},
		{rel: "engine/state/logs/limitdock.log", want: true},
		{rel: "engine/downloads/openusage_windows_amd64/configs/example_settings.json", want: false},
	}

	for _, tc := range cases {
		if got := isForbiddenReleaseEntry(tc.rel); got != tc.want {
			t.Fatalf("isForbiddenReleaseEntry(%q) = %v, want %v", tc.rel, got, tc.want)
		}
	}
}
