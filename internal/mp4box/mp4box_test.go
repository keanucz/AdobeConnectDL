package mp4box

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFakeMP4Box creates a shell script standing in for the MP4Box binary.
// It records its arguments and working directory, then exits with exitCode.
func writeFakeMP4Box(t *testing.T, exitCode int) (binPath, logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake MP4Box is a shell script")
	}

	dir := t.TempDir()
	binPath = filepath.Join(dir, "MP4Box")
	logPath = filepath.Join(dir, "invocation.log")
	script := "#!/bin/sh\n" +
		"{ pwd; for arg in \"$@\"; do echo \"$arg\"; done; } > '" + logPath + "'\n" +
		"echo 'fake stdout'\n" +
		"echo 'fake stderr' >&2\n" +
		"exit " + string(rune('0'+exitCode)) + "\n"
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake MP4Box: %v", err)
	}
	return binPath, logPath
}

// writeMedia creates placeholder MP4 and VTT files and returns their paths.
func writeMedia(t *testing.T) (mp4Path, vttPath string) {
	t.Helper()
	dir := t.TempDir()
	mp4Path = filepath.Join(dir, "recording.mp4")
	vttPath = filepath.Join(dir, "captions.vtt")
	for _, path := range []string{mp4Path, vttPath} {
		if err := os.WriteFile(path, []byte("placeholder"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return mp4Path, vttPath
}

func readInvocation(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read invocation log: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestLocate(t *testing.T) {
	t.Run("explicit path wins over environment", func(t *testing.T) {
		t.Setenv("ADOBECONNECTDL_MP4BOX", "/from/env/MP4Box")

		got, err := Locate("/explicit/MP4Box")
		if err != nil {
			t.Fatalf("Locate error: %v", err)
		}
		if got != "/explicit/MP4Box" {
			t.Fatalf("Locate() = %q, want explicit path", got)
		}
	})

	t.Run("environment variable used when no explicit path", func(t *testing.T) {
		t.Setenv("ADOBECONNECTDL_MP4BOX", "/from/env/MP4Box")

		got, err := Locate("")
		if err != nil {
			t.Fatalf("Locate error: %v", err)
		}
		if got != "/from/env/MP4Box" {
			t.Fatalf("Locate() = %q, want env path", got)
		}
	})

	t.Run("found on PATH", func(t *testing.T) {
		binPath, _ := writeFakeMP4Box(t, 0)
		t.Setenv("ADOBECONNECTDL_MP4BOX", "")
		t.Setenv("PATH", filepath.Dir(binPath))

		got, err := Locate("")
		if err != nil {
			t.Fatalf("Locate error: %v", err)
		}
		if got != binPath {
			t.Fatalf("Locate() = %q, want %q", got, binPath)
		}
	})

	t.Run("not found anywhere", func(t *testing.T) {
		if _, err := extractEmbedded(runtime.GOOS, runtime.GOARCH); err == nil {
			t.Skip("embedded MP4Box available in this build")
		}
		t.Setenv("ADOBECONNECTDL_MP4BOX", "")
		t.Setenv("PATH", t.TempDir())

		if _, err := Locate(""); err == nil {
			t.Fatal("expected error when MP4Box cannot be located")
		}
	})
}

func TestNew(t *testing.T) {
	runner, err := New("/explicit/MP4Box")
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	if got := runner.Path(); got != "/explicit/MP4Box" {
		t.Fatalf("Path() = %q, want explicit path", got)
	}
}

func TestNewNotFound(t *testing.T) {
	if _, err := extractEmbedded(runtime.GOOS, runtime.GOARCH); err == nil {
		t.Skip("embedded MP4Box available in this build")
	}
	t.Setenv("ADOBECONNECTDL_MP4BOX", "")
	t.Setenv("PATH", t.TempDir())

	if _, err := New(""); err == nil {
		t.Fatal("expected error when MP4Box cannot be located")
	}
}

func TestEmbedSubtitles(t *testing.T) {
	t.Run("builds MP4Box command with absolute paths", func(t *testing.T) {
		binPath, logPath := writeFakeMP4Box(t, 0)
		mp4Path, vttPath := writeMedia(t)
		runner := &Runner{path: binPath}
		var stdout bytes.Buffer

		if err := runner.EmbedSubtitles(context.Background(), mp4Path, vttPath, "", &stdout, nil); err != nil {
			t.Fatalf("EmbedSubtitles error: %v", err)
		}

		lines := readInvocation(t, logPath)
		if len(lines) != 6 {
			t.Fatalf("expected working dir plus 5 args, got %q", lines)
		}
		workDir, args := lines[0], lines[1:]

		// Language defaults to English when not specified.
		if want := vttPath + ":lang=en:@vtt2tx3g"; args[0] != "-add" || args[1] != want {
			t.Errorf("subtitle args = %q, want [-add %s]", args[:2], want)
		}
		// MP4Box runs from, and writes temp files to, its own unique directory.
		if args[2] != "-tmp" || !strings.Contains(filepath.Base(args[3]), "mp4box-") {
			t.Errorf("temp dir args = %q, want [-tmp <mp4box-* dir>]", args[2:4])
		}
		if filepath.Base(workDir) != filepath.Base(args[3]) {
			t.Errorf("working dir = %q, want temp dir %q", workDir, args[3])
		}
		if args[4] != mp4Path {
			t.Errorf("mp4 arg = %q, want %q", args[4], mp4Path)
		}
		if _, err := os.Stat(args[3]); !os.IsNotExist(err) {
			t.Errorf("expected temp dir to be removed afterwards, stat error: %v", err)
		}
		if got := stdout.String(); got != "fake stdout\n" {
			t.Errorf("stdout = %q, want fake stdout", got)
		}
	})

	t.Run("passes explicit language", func(t *testing.T) {
		binPath, logPath := writeFakeMP4Box(t, 0)
		mp4Path, vttPath := writeMedia(t)
		runner := &Runner{path: binPath}

		if err := runner.EmbedSubtitles(context.Background(), mp4Path, vttPath, "fr", nil, nil); err != nil {
			t.Fatalf("EmbedSubtitles error: %v", err)
		}

		lines := readInvocation(t, logPath)
		if want := vttPath + ":lang=fr:@vtt2tx3g"; len(lines) < 3 || lines[2] != want {
			t.Fatalf("subtitle arg = %q, want %q", lines, want)
		}
	})

	t.Run("includes captured stderr when MP4Box fails", func(t *testing.T) {
		binPath, _ := writeFakeMP4Box(t, 3)
		mp4Path, vttPath := writeMedia(t)
		runner := &Runner{path: binPath}

		err := runner.EmbedSubtitles(context.Background(), mp4Path, vttPath, "en", nil, nil)
		if err == nil {
			t.Fatal("expected error when MP4Box exits non-zero")
		}
		if !strings.Contains(err.Error(), "MP4Box:") || !strings.Contains(err.Error(), "fake stderr") {
			t.Fatalf("error = %q, want MP4Box prefix and captured stderr", err)
		}
	})

	t.Run("leaves caller-provided stderr out of the error", func(t *testing.T) {
		binPath, _ := writeFakeMP4Box(t, 3)
		mp4Path, vttPath := writeMedia(t)
		runner := &Runner{path: binPath}
		var stderr bytes.Buffer

		err := runner.EmbedSubtitles(context.Background(), mp4Path, vttPath, "en", nil, &stderr)
		if err == nil {
			t.Fatal("expected error when MP4Box exits non-zero")
		}
		if strings.Contains(err.Error(), "fake stderr") {
			t.Fatalf("error = %q, did not expect stderr text", err)
		}
		if got := stderr.String(); got != "fake stderr\n" {
			t.Fatalf("stderr = %q, want fake stderr", got)
		}
	})

	t.Run("missing input files", func(t *testing.T) {
		mp4Path, vttPath := writeMedia(t)
		runner := &Runner{path: "/nonexistent/MP4Box"}
		missing := filepath.Join(t.TempDir(), "missing")

		tests := []struct {
			name    string
			mp4     string
			vtt     string
			wantErr string
		}{
			{"missing mp4", missing, vttPath, "MP4 file not found"},
			{"missing vtt", mp4Path, missing, "VTT file not found"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := runner.EmbedSubtitles(context.Background(), tt.mp4, tt.vtt, "en", nil, nil)
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
			})
		}
	})

	t.Run("missing binary", func(t *testing.T) {
		mp4Path, vttPath := writeMedia(t)
		runner := &Runner{path: filepath.Join(t.TempDir(), "MP4Box")}

		if err := runner.EmbedSubtitles(context.Background(), mp4Path, vttPath, "en", nil, nil); err == nil {
			t.Fatal("expected error when the MP4Box binary does not exist")
		}
	})
}

func TestEnsureDir(t *testing.T) {
	t.Run("creates missing parent directories", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "a", "b", "file.mp4")

		if err := EnsureDir(path); err != nil {
			t.Fatalf("EnsureDir error: %v", err)
		}
		info, err := os.Stat(filepath.Dir(path))
		if err != nil || !info.IsDir() {
			t.Fatalf("expected parent directory to exist, stat error: %v", err)
		}
	})

	t.Run("bare filename needs no directory", func(t *testing.T) {
		if err := EnsureDir("file.mp4"); err != nil {
			t.Fatalf("EnsureDir error: %v", err)
		}
	})
}
