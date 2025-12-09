package ffmpegexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Runner wraps execution of the ffmpeg binary.
type Runner struct {
	path string
}

// Locate finds the ffmpeg binary to use. Order:
// 1) explicit path argument if non-empty
// 2) ADOBECONNECTDL_FFMPEG environment variable
// 3) ffmpeg found on PATH
func Locate(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if env := os.Getenv("ADOBECONNECTDL_FFMPEG"); env != "" {
		return env, nil
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p, nil
	}
	if p, err := extractEmbedded(runtime.GOOS, runtime.GOARCH); err == nil {
		return p, nil
	}
	return "", errors.New("ffmpeg not found; set ADOBECONNECTDL_FFMPEG, provide --ffmpeg, or use a supported platform for the embedded binary")
}

// New creates a Runner using the located ffmpeg path.
func New(explicit string) (*Runner, error) {
	p, err := Locate(explicit)
	if err != nil {
		return nil, err
	}
	return &Runner{path: p}, nil
}

// Run executes ffmpeg with the provided arguments.
func (r *Runner) Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, r.path, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg: %w", err)
	}
	return nil
}

// Path returns the ffmpeg path in use.
func (r *Runner) Path() string {
	return r.path
}

// EnsureDir ensures the parent directory for a path exists.
func EnsureDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}
