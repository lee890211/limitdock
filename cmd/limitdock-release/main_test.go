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
		{rel: "state/", want: false},
		{rel: "state/limitdock.pid", want: true},
		{rel: "state/logs/limitdock.log", want: true},
		{rel: "assets/icons/claude.png", want: false},
	}

	for _, tc := range cases {
		if got := isForbiddenReleaseEntry(tc.rel); got != tc.want {
			t.Fatalf("isForbiddenReleaseEntry(%q) = %v, want %v", tc.rel, got, tc.want)
		}
	}
}
