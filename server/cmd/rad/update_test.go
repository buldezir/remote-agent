package main

import (
	"slices"
	"strings"
	"testing"

	"remote-agent/internal/model"
)

func TestStartsBinary(t *testing.T) {
	env := [][2]string{{"PATH", "/usr/bin:/bin"}}
	for _, exe := range []string{
		"/home/u/.local/bin/rad",
		"/Users/a b/bin/rad",
		"/opt/$x/100%/rad",
		"/srv/a&b/<rad>",
	} {
		unit := systemdUnitFile(exe, env)
		plist := launchAgentPlist(exe, "/Users/a", "/Users/a/Library/Logs/rad.log", env)
		for name, file := range map[string]string{"unit": unit, "plist": plist} {
			if !startsBinary(file, exe) {
				t.Errorf("%s for %q: not matched:\n%s", name, exe, file)
			}
			if startsBinary(file, exe+"2") || startsBinary(file, "/other/rad") {
				t.Errorf("%s for %q matches another binary", name, exe)
			}
		}
	}
}

func TestCheckReplaceable(t *testing.T) {
	for exe, want := range map[string]string{
		"/home/u/.local/bin/rad":                                   "",
		"/tmp/go-build123/b001/exe/rad":                            "go run",
		"/Applications/Remote Agent Server.app/Contents/MacOS/rad": "part of Remote Agent Server.app",
	} {
		err := checkReplaceable(exe)
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("checkReplaceable(%q) = %v, want %q", exe, err, want)
		}
	}
}

func TestBusyTitles(t *testing.T) {
	got := busyTitles([]*model.Session{
		{Title: "Fix the tests", Status: model.SessionRunning},
		{Title: "Done", Status: model.SessionIdle},
		{Status: model.SessionAwaitingApproval},
	})
	if want := []string{"Fix the tests", "Untitled"}; !slices.Equal(got, want) {
		t.Errorf("busyTitles = %q, want %q", got, want)
	}
	if got := busyTitles(nil); got == nil || len(got) != 0 {
		t.Errorf("busyTitles(nil) = %#v, want an empty list for JSON", got)
	}
}
