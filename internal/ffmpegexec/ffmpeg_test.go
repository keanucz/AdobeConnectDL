package ffmpegexec

import (
	"os"
	"runtime"
	"testing"
)

func TestLocateFallsBackToEmbedded(t *testing.T) {
	key := runtime.GOOS + "/" + runtime.GOARCH
	entry, ok := embedded[key]
	if !ok || len(entry.data) == 0 {
		t.Skipf("no embedded ffmpeg for %s", key)
	}

	t.Setenv("ADOBECONNECTDL_FFMPEG", "")
	t.Setenv("PATH", "")

	path, err := Locate("")
	if err != nil {
		t.Fatalf("Locate returned error: %v", err)
	}
	if path == "" {
		t.Fatalf("expected embedded path, got empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("embedded ffmpeg path not accessible: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("embedded ffmpeg is not executable: mode=%v", info.Mode())
	}
}
