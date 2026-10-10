package version

import (
	"runtime"
	"strings"
	"testing"
)

// setBuildInfo overrides the ldflags-injected variables for the duration of a test.
func setBuildInfo(t *testing.T, version, commit, date string) {
	t.Helper()
	origVersion, origCommit, origDate := Version, Commit, Date
	t.Cleanup(func() {
		Version, Commit, Date = origVersion, origCommit, origDate
	})
	Version, Commit, Date = version, commit, date
}

func TestDefaults(t *testing.T) {
	if Version != "dev" || Commit != "unknown" || Date != "unknown" {
		t.Fatalf("unexpected defaults: Version=%q Commit=%q Date=%q", Version, Commit, Date)
	}
}

func TestShort(t *testing.T) {
	setBuildInfo(t, "2.2.1", "abc1234", "2026-10-10T00:00:00Z")

	if got, want := Short(), "2.2.1 (abc1234)"; got != want {
		t.Fatalf("Short() = %q, want %q", got, want)
	}
}

func TestInfo(t *testing.T) {
	setBuildInfo(t, "2.2.1", "abc1234", "2026-10-10T00:00:00Z")

	want := strings.Join([]string{
		"Version:    2.2.1",
		"Commit:     abc1234",
		"Built:      2026-10-10T00:00:00Z",
		"Go version: " + runtime.Version(),
		"OS/Arch:    " + runtime.GOOS + "/" + runtime.GOARCH,
	}, "\n")

	if got := Info(); got != want {
		t.Fatalf("Info() = %q, want %q", got, want)
	}
}
