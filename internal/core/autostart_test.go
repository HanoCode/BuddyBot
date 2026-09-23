package core

import "testing"

func TestAppBundlePath(t *testing.T) {
	cases := []struct {
		exe  string
		want string
	}{
		{"/Applications/BuddyBot.app/Contents/MacOS/BuddyBot", "/Applications/BuddyBot.app"},
		{"/Users/bws/code/BuddyBot/bin/BuddyBot", ""},
		{"/Applications/BuddyBot/Contents/MacOS/BuddyBot", ""},
		{"/Applications/My App.app/Contents/MacOS/binary", "/Applications/My App.app"},
	}
	for _, c := range cases {
		if got := appBundlePath(c.exe); got != c.want {
			t.Errorf("appBundlePath(%q) = %q, want %q", c.exe, got, c.want)
		}
	}
}
