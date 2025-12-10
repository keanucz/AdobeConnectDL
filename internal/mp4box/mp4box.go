// Package mp4box provides an interface to run MP4Box for embedding subtitles into MP4 files.
// MP4Box is much smaller than FFmpeg and sufficient for subtitle embedding tasks.
package mp4box

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Runner wraps execution of the MP4Box binary.
type Runner struct {
	path string
}

// Locate finds the MP4Box binary to use. Order:
// 1) explicit path argument if non-empty
// 2) ADOBECONNECTDL_MP4BOX environment variable
// 3) MP4Box found on PATH
// 4) Embedded binary (extracted on first use)
func Locate(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if env := os.Getenv("ADOBECONNECTDL_MP4BOX"); env != "" {
		return env, nil
	}
	if p, err := exec.LookPath("MP4Box"); err == nil {
		return p, nil
	}
	// Also try lowercase on Unix systems
	if runtime.GOOS != "windows" {
		if p, err := exec.LookPath("mp4box"); err == nil {
			return p, nil
		}
	}
	if p, err := extractEmbedded(runtime.GOOS, runtime.GOARCH); err == nil {
		return p, nil
	}
	return "", errors.New(
		"MP4Box not found; set ADOBECONNECTDL_MP4BOX, provide --mp4box, install MP4Box, or use a supported platform for the embedded binary",
	)
}

// New creates a Runner using the located MP4Box path.
func New(explicit string) (*Runner, error) {
	p, err := Locate(explicit)
	if err != nil {
		return nil, err
	}
	return &Runner{path: p}, nil
}

// Run executes MP4Box with the provided arguments.
// workDir sets the working directory for the command; if empty, uses current directory.
func (r *Runner) Run(ctx context.Context, args []string, workDir string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, r.path, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if workDir != "" {
		cmd.Dir = workDir
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("MP4Box: %w", err)
	}
	return nil
}

// Path returns the MP4Box path in use.
func (r *Runner) Path() string {
	return r.path
}

// EmbedSubtitles adds a VTT subtitle track to an MP4 file.
// The command uses the vtt2tx3g filter to convert WebVTT to TX3G format,
// which is properly recognized as a subtitle track by ffmpeg and video players.
// Command: MP4Box -add "subtitles.vtt:lang=en:@vtt2tx3g" -tmp <dir> input.mp4
// Note: MP4Box modifies the input file in place.
func (r *Runner) EmbedSubtitles(ctx context.Context, mp4Path, vttPath, lang string, stdout, stderr io.Writer) error {
	if lang == "" {
		lang = "en"
	}

	// Convert to absolute paths to avoid issues with working directory
	absMP4, err := filepath.Abs(mp4Path)
	if err != nil {
		return fmt.Errorf("failed to resolve MP4 path: %w", err)
	}
	absVTT, err := filepath.Abs(vttPath)
	if err != nil {
		return fmt.Errorf("failed to resolve VTT path: %w", err)
	}

	// Verify files exist before calling MP4Box
	if _, err := os.Stat(absMP4); err != nil {
		return fmt.Errorf("MP4 file not found: %w", err)
	}
	if _, err := os.Stat(absVTT); err != nil {
		return fmt.Errorf("VTT file not found: %w", err)
	}

	// Create a unique temp directory for this MP4Box invocation to avoid collisions
	// when multiple MP4Box instances run concurrently on files in the same directory.
	// Each MP4Box process creates a temp file like "out_<filename>.mp4" in the temp dir,
	// and if multiple processes use the same temp dir, they collide.
	tmpDir, err := os.MkdirTemp("", "mp4box-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir) // Clean up temp dir after we're done

	// MP4Box -add "subtitles.vtt:lang=en:@vtt2tx3g" -tmp <dir> input.mp4
	// The @vtt2tx3g filter converts WebVTT to TX3G (mov_text) format
	// which is the proper MP4 subtitle format recognized by all players
	// -tmp sets the temp directory for intermediate files
	args := []string{
		"-add", fmt.Sprintf("%s:lang=%s:@vtt2tx3g", absVTT, lang),
		"-tmp", tmpDir,
		absMP4,
	}

	// Capture stderr for better error messages if not provided
	var errBuf bytes.Buffer
	if stderr == nil {
		stderr = &errBuf
	}

	// Run MP4Box from the unique temp directory to avoid any collisions
	err = r.Run(ctx, args, tmpDir, stdout, stderr)
	if err != nil && errBuf.Len() > 0 {
		return fmt.Errorf("%w: %s", err, errBuf.String())
	}
	return err
}

// EnsureDir ensures the parent directory for a path exists.
func EnsureDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}
