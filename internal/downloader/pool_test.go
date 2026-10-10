package downloader

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// recordingLogger is a Logger that counts calls per level so tests can assert on logging.
type recordingLogger struct {
	mu    sync.Mutex
	calls map[string]int
}

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{calls: make(map[string]int)}
}

func (l *recordingLogger) record(level string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls[level]++
}

func (l *recordingLogger) count(level string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls[level]
}

func (l *recordingLogger) Debug(_ any, _ ...any) { l.record("debug") }
func (l *recordingLogger) Info(_ any, _ ...any)  { l.record("info") }
func (l *recordingLogger) Warn(_ any, _ ...any)  { l.record("warn") }
func (l *recordingLogger) Error(_ any, _ ...any) { l.record("error") }

// newPoolServer serves a fixed set of paths; anything else is a 404.
func newPoolServer(t *testing.T, files map[string][]byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.Write(content)
	}))
	t.Cleanup(server.Close)
	return server
}

// startPool creates and starts a pool that is stopped when the test ends.
func startPool(t *testing.T, client HTTPClient, config PoolConfig) *DownloadPool {
	t.Helper()
	pool := NewDownloadPool(client, config)
	pool.Start()
	t.Cleanup(pool.Stop)
	return pool
}

func assertPoolStats(t *testing.T, pool *DownloadPool, wantCompleted, wantFailed int64) {
	t.Helper()
	completed, failed := pool.Stats()
	if completed != wantCompleted || failed != wantFailed {
		t.Fatalf(
			"Stats() = (%d completed, %d failed), want (%d, %d)",
			completed, failed, wantCompleted, wantFailed,
		)
	}
}

func TestJobTypeString(t *testing.T) {
	tests := []struct {
		jobType JobType
		want    string
	}{
		{JobTypeDocument, "document"},
		{JobTypeZip, "zip"},
		{JobTypeMP4, "mp4"},
		{JobTypeVTT, "vtt"},
		{JobTypeExtract, "extract"},
		{JobType(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.jobType.String(); got != tt.want {
				t.Errorf("JobType(%d).String() = %q, want %q", tt.jobType, got, tt.want)
			}
		})
	}
}

func TestTruncateURL(t *testing.T) {
	long := "https://example.com/" + strings.Repeat("a", 100)

	tests := []struct {
		name string
		url  string
		want string
	}{
		{"short url unchanged", "https://example.com/a", "https://example.com/a"},
		{"exactly 80 chars unchanged", long[:80], long[:80]},
		{"long url truncated", long, long[:77] + "..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateURL(tt.url); got != tt.want {
				t.Errorf("truncateURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewDownloadPoolDefaults(t *testing.T) {
	defaults := DefaultPoolConfig()
	if defaults.NumWorkers != DefaultConcurrency || defaults.QueueSize != 1000 {
		t.Fatalf("unexpected default config: %+v", defaults)
	}

	tests := []struct {
		name        string
		config      PoolConfig
		wantWorkers int
		wantQueue   int
	}{
		{"zero config falls back to defaults", PoolConfig{}, 12, 1000},
		{"negative values fall back to defaults", PoolConfig{NumWorkers: -1, QueueSize: -5}, 12, 1000},
		{"explicit values kept", PoolConfig{NumWorkers: 3, QueueSize: 7}, 3, 7},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := NewDownloadPool(http.DefaultClient, tt.config)
			if pool.numWorkers != tt.wantWorkers {
				t.Errorf("numWorkers = %d, want %d", pool.numWorkers, tt.wantWorkers)
			}
			if cap(pool.jobs) != tt.wantQueue {
				t.Errorf("queue capacity = %d, want %d", cap(pool.jobs), tt.wantQueue)
			}
		})
	}
}

func TestPoolStartAndStopAreIdempotent(t *testing.T) {
	logger := newRecordingLogger()
	pool := NewDownloadPool(http.DefaultClient, PoolConfig{NumWorkers: 2, Logger: logger})

	pool.Start()
	pool.Start()
	pool.Stop()
	pool.Stop()

	// One "started" and one "stopped" message, despite the repeated calls.
	if got := logger.count("info"); got != 2 {
		t.Fatalf("expected 2 info logs, got %d", got)
	}
}

func TestPoolSubmitAndWaitDownloadsFile(t *testing.T) {
	content := []byte("document-content")
	server := newPoolServer(t, map[string][]byte{"/doc.pdf": content})
	pool := startPool(t, server.Client(), PoolConfig{NumWorkers: 2, Logger: newRecordingLogger()})

	dest := filepath.Join(t.TempDir(), "nested", "doc.pdf")
	var completeCalls atomic.Int32

	err := pool.SubmitAndWait(DownloadJob{
		Type:       JobTypeDocument,
		Name:       "doc.pdf",
		URL:        server.URL + "/doc.pdf",
		DestPath:   dest,
		Kind:       fileKindBinary,
		OnComplete: func(error) { completeCalls.Add(1) },
	})
	if err != nil {
		t.Fatalf("SubmitAndWait error: %v", err)
	}

	assertFileContent(t, dest, content)
	assertPoolStats(t, pool, 1, 0)
	if got := completeCalls.Load(); got != 1 {
		t.Fatalf("expected original OnComplete to be called once, got %d", got)
	}
}

func TestPoolSubmitAndWaitReportsFailure(t *testing.T) {
	server := newPoolServer(t, nil)
	logger := newRecordingLogger()
	pool := startPool(t, server.Client(), PoolConfig{NumWorkers: 1, Logger: logger})

	err := pool.SubmitAndWait(DownloadJob{
		Type:     JobTypeDocument,
		Name:     "missing.pdf",
		URL:      server.URL + "/missing.pdf",
		DestPath: filepath.Join(t.TempDir(), "missing.pdf"),
		Kind:     fileKindBinary,
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	assertPoolStats(t, pool, 0, 1)
	if logger.count("warn") == 0 {
		t.Fatal("expected failed download to be logged as a warning")
	}
}

func TestPoolRejectsJobsAfterStop(t *testing.T) {
	pool := NewDownloadPool(http.DefaultClient, PoolConfig{NumWorkers: 1})
	pool.Start()
	pool.Stop()

	if pool.Submit(DownloadJob{Name: "late"}) {
		t.Fatal("expected Submit to return false after Stop")
	}
	if err := pool.SubmitAndWait(DownloadJob{Name: "late"}); err == nil {
		t.Fatal("expected SubmitAndWait to fail after Stop")
	}
}

func TestPoolSkipsCancelledJob(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Write([]byte("should not be fetched"))
	}))
	defer server.Close()

	pool := startPool(t, server.Client(), PoolConfig{NumWorkers: 1, Logger: newRecordingLogger()})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dest := filepath.Join(t.TempDir(), "cancelled.bin")
	err := pool.SubmitAndWait(DownloadJob{
		Type:     JobTypeDocument,
		Name:     "cancelled.bin",
		URL:      server.URL + "/cancelled.bin",
		DestPath: dest,
		Kind:     fileKindBinary,
		Ctx:      ctx,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	assertPoolStats(t, pool, 0, 1)
	if got := requests.Load(); got != 0 {
		t.Fatalf("expected no HTTP requests for a cancelled job, got %d", got)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("expected no file for a cancelled job, stat error: %v", statErr)
	}
}

func TestPoolSubmitMP4(t *testing.T) {
	video := make([]byte, 8192)
	copy(video, "mp4-data-start")
	server := newPoolServer(t, map[string][]byte{
		"/video.mp4": video,
		"/tiny.mp4":  []byte("too-small"),
	})
	pool := startPool(t, server.Client(), PoolConfig{NumWorkers: 2})

	t.Run("downloads video and reports progress", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "video.mp4")
		var lastDownloaded, lastTotal atomic.Int64

		result := <-pool.SubmitMP4(
			context.Background(), server.URL+"/video.mp4", dest, server.URL, nil,
			func(downloaded, total int64) {
				lastDownloaded.Store(downloaded)
				lastTotal.Store(total)
			},
		)
		if result.Err != nil {
			t.Fatalf("SubmitMP4 error: %v", result.Err)
		}
		if result.Path != dest {
			t.Fatalf("result path = %q, want %q", result.Path, dest)
		}

		assertFileContent(t, dest, video)
		if lastDownloaded.Load() != int64(len(video)) || lastTotal.Load() != int64(len(video)) {
			t.Fatalf(
				"final progress = %d/%d, want %d/%d",
				lastDownloaded.Load(), lastTotal.Load(), len(video), len(video),
			)
		}
	})

	t.Run("rejects implausibly small video", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "tiny.mp4")

		result := <-pool.SubmitMP4(context.Background(), server.URL+"/tiny.mp4", dest, server.URL, nil, nil)
		if result.Err == nil || !strings.Contains(result.Err.Error(), "file too small") {
			t.Fatalf("expected file too small error, got %v", result.Err)
		}
		if result.Path != "" {
			t.Fatalf("expected empty path on failure, got %q", result.Path)
		}
	})
}

func TestPoolSubmitZip(t *testing.T) {
	zipContent := createZip(t, map[string]string{"a.txt": "hello"})
	server := newPoolServer(t, map[string][]byte{
		"/raw.zip":     zipContent,
		"/not-zip.zip": []byte("this is definitely not a zip archive"),
	})
	pool := startPool(t, server.Client(), PoolConfig{NumWorkers: 2})

	t.Run("downloads valid zip", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "raw.zip")

		result := <-pool.SubmitZip(context.Background(), server.URL+"/raw.zip", dest, server.URL, nil)
		if result.Err != nil {
			t.Fatalf("SubmitZip error: %v", result.Err)
		}
		if result.Path != dest {
			t.Fatalf("result path = %q, want %q", result.Path, dest)
		}
		assertFileContent(t, dest, zipContent)
	})

	t.Run("rejects content without zip signature", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "not-zip.zip")

		result := <-pool.SubmitZip(context.Background(), server.URL+"/not-zip.zip", dest, server.URL, nil)
		if !errors.Is(result.Err, ErrInvalidZip) {
			t.Fatalf("expected ErrInvalidZip, got %v", result.Err)
		}
	})
}

func TestPoolSubmitVTT(t *testing.T) {
	vtt := []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nHello\n")
	server := newPoolServer(t, map[string][]byte{"/captions.vtt": vtt})
	pool := startPool(t, server.Client(), PoolConfig{NumWorkers: 1})

	t.Run("downloads captions", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "captions.vtt")

		result := <-pool.SubmitVTT(context.Background(), server.URL+"/captions.vtt", dest, server.URL, nil)
		if result.Err != nil {
			t.Fatalf("SubmitVTT error: %v", result.Err)
		}
		if result.Path != dest {
			t.Fatalf("result path = %q, want %q", result.Path, dest)
		}
		assertFileContent(t, dest, vtt)
	})

	t.Run("reports missing captions", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "captions.vtt")

		result := <-pool.SubmitVTT(context.Background(), server.URL+"/nope.vtt", dest, server.URL, nil)
		if !errors.Is(result.Err, ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", result.Err)
		}
	})
}

func TestPoolSubmitExtract(t *testing.T) {
	logger := newRecordingLogger()
	pool := startPool(t, http.DefaultClient, PoolConfig{NumWorkers: 1, Logger: logger})

	t.Run("extracts zip", func(t *testing.T) {
		tmp := t.TempDir()
		zipPath := filepath.Join(tmp, "raw.zip")
		if err := os.WriteFile(zipPath, createZip(t, map[string]string{"a.txt": "hello"}), 0o644); err != nil {
			t.Fatalf("write zip: %v", err)
		}
		extractDir := filepath.Join(tmp, "raw")

		result := <-pool.SubmitExtract(context.Background(), zipPath, extractDir, "raw.zip")
		if result.Err != nil {
			t.Fatalf("SubmitExtract error: %v", result.Err)
		}
		if result.Path != extractDir {
			t.Fatalf("result path = %q, want %q", result.Path, extractDir)
		}
		assertFileContent(t, filepath.Join(extractDir, "a.txt"), []byte("hello"))
	})

	t.Run("reports missing zip", func(t *testing.T) {
		tmp := t.TempDir()

		result := <-pool.SubmitExtract(
			context.Background(), filepath.Join(tmp, "missing.zip"), filepath.Join(tmp, "raw"), "missing.zip",
		)
		if result.Err == nil {
			t.Fatal("expected error extracting a missing zip")
		}
		if logger.count("warn") == 0 {
			t.Fatal("expected failed extraction to be logged as a warning")
		}
	})
}

func TestPoolWaitForDocuments(t *testing.T) {
	server := newPoolServer(t, map[string][]byte{
		"/docs/slides.pdf": []byte("slides"),
		"/docs/notes.pdf":  []byte("notes"),
	})
	pool := startPool(t, server.Client(), PoolConfig{NumWorkers: 3})

	t.Run("counts only successful downloads", func(t *testing.T) {
		destDir := filepath.Join(t.TempDir(), "documents")
		docs := []DocumentInfo{
			{Name: "slides.pdf", DownloadURL: server.URL + "/docs/slides.pdf"},
			{Name: "notes.pdf", DownloadURL: server.URL + "/docs/notes.pdf"},
			{Name: "gone.pdf", DownloadURL: server.URL + "/docs/gone.pdf"},
		}

		got := pool.WaitForDocuments(context.Background(), docs, destDir, nil, server.URL)
		if got != 2 {
			t.Fatalf("WaitForDocuments() = %d, want 2", got)
		}
		assertFileContent(t, filepath.Join(destDir, "slides.pdf"), []byte("slides"))
		assertFileContent(t, filepath.Join(destDir, "notes.pdf"), []byte("notes"))
	})

	t.Run("no documents is a no-op", func(t *testing.T) {
		destDir := filepath.Join(t.TempDir(), "documents")

		got := pool.WaitForDocuments(context.Background(), nil, destDir, nil, server.URL)
		if got != 0 {
			t.Fatalf("WaitForDocuments() = %d, want 0", got)
		}
		if _, err := os.Stat(destDir); !os.IsNotExist(err) {
			t.Fatalf("expected documents dir not to be created, stat error: %v", err)
		}
	})

	t.Run("unusable destination reports an error", func(t *testing.T) {
		// A regular file where the documents directory should be makes MkdirAll fail.
		blocker := filepath.Join(t.TempDir(), "documents")
		if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
			t.Fatalf("write blocker file: %v", err)
		}
		docs := []DocumentInfo{{Name: "slides.pdf", DownloadURL: server.URL + "/docs/slides.pdf"}}

		results := pool.SubmitDocuments(context.Background(), docs, blocker, nil, server.URL)
		result, ok := <-results
		if !ok || result.Err == nil {
			t.Fatalf("expected a directory creation error, got %+v (ok=%v)", result, ok)
		}
		if _, open := <-results; open {
			t.Fatal("expected results channel to be closed after the error")
		}
	})
}

func TestDownloadWithPool(t *testing.T) {
	mp4Content := make([]byte, 4096)
	copy(mp4Content, "mp4-data-start")
	zipContent := createZip(t, map[string]string{"a.txt": "hello"})
	var server *httptest.Server

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rec/":
			fmt.Fprintf(w, `<html><head><title>Pooled Recording</title></head>
<body><script>var casRecordingURL = '%s/rec/output/rec.mp4?download=mp4';</script></body></html>`, server.URL)
		case "/rec/output/rec.mp4":
			w.Write(mp4Content)
		case "/rec/output/rec.zip":
			w.Write(zipContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	pool := startPool(t, server.Client(), PoolConfig{NumWorkers: 4})
	dl := NewWithPool(server.Client(), pool)

	res, err := dl.Download(context.Background(), server.URL+"/rec/?session=abc", Options{OutputDir: t.TempDir()})
	if err != nil {
		t.Fatalf("download error: %v", err)
	}
	if res.Title != "Pooled Recording" {
		t.Fatalf("unexpected title: %s", res.Title)
	}

	assertFileContent(t, res.MP4Path, mp4Content)
	assertFileContent(t, filepath.Join(res.ExtractedDir, "a.txt"), []byte("hello"))
	if len(res.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", res.Warnings)
	}

	completed, failed := pool.Stats()
	if completed == 0 || failed != 0 {
		t.Fatalf("expected pool to do the downloads, got %d completed, %d failed", completed, failed)
	}
}
