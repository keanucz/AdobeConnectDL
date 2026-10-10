package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// infoRecorder captures Info messages logged by the progress callback.
type infoRecorder struct {
	messages []string
	keyvals  [][]any
}

func (r *infoRecorder) Info(msg any, keyvals ...any) {
	text, _ := msg.(string)
	r.messages = append(r.messages, text)
	r.keyvals = append(r.keyvals, keyvals)
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		name  string
		bytes int64
		want  string
	}{
		{"zero", 0, "0 B"},
		{"below one kilobyte", 1023, "1023 B"},
		{"exactly one kilobyte", 1024, "1.0 KB"},
		{"fractional kilobytes", 1536, "1.5 KB"},
		{"megabytes", 5 * 1024 * 1024, "5.0 MB"},
		{"gigabytes", 3 * 1024 * 1024 * 1024, "3.0 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatBytes(tt.bytes); got != tt.want {
				t.Errorf("formatBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}

func TestDeduplicateURLs(t *testing.T) {
	tests := []struct {
		name string
		urls []string
		want []string
	}{
		{"empty", nil, []string{}},
		{"no duplicates", []string{"a", "b"}, []string{"a", "b"}},
		{"keeps first occurrence order", []string{"b", "a", "b", "c", "a"}, []string{"b", "a", "c"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deduplicateURLs(tt.urls); !slices.Equal(got, tt.want) {
				t.Errorf("deduplicateURLs(%v) = %v, want %v", tt.urls, got, tt.want)
			}
		})
	}
}

func TestReadURLsFromFile(t *testing.T) {
	t.Run("skips blanks and comments and trims whitespace", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "urls.txt")
		content := "# lecture recordings\n" +
			"https://example.com/rec1/\n" +
			"\n" +
			"   https://example.com/rec2/?session=abc  \n" +
			"  # indented comment\n" +
			"https://example.com/rec3/"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write url file: %v", err)
		}

		got, err := readURLsFromFile(path)
		if err != nil {
			t.Fatalf("readURLsFromFile error: %v", err)
		}

		want := []string{
			"https://example.com/rec1/",
			"https://example.com/rec2/?session=abc",
			"https://example.com/rec3/",
		}
		if !slices.Equal(got, want) {
			t.Fatalf("readURLsFromFile() = %v, want %v", got, want)
		}
	})

	t.Run("empty file yields no urls", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty.txt")
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatalf("write url file: %v", err)
		}

		got, err := readURLsFromFile(path)
		if err != nil {
			t.Fatalf("readURLsFromFile error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("expected no urls, got %v", got)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if _, err := readURLsFromFile(filepath.Join(t.TempDir(), "nope.txt")); err == nil {
			t.Fatal("expected error reading a missing file")
		}
	})
}

func TestMakeProgressCallback(t *testing.T) {
	const mb = 1024 * 1024
	recorder := &infoRecorder{}
	progress := makeProgressCallback("1/2", recorder)

	progress(0, 0)           // unknown total: ignored
	progress(0, 100*mb)      // first call always logs
	progress(5*mb, 100*mb)   // same 10% bucket: silent
	progress(10*mb, 100*mb)  // crosses into the 10% bucket
	progress(19*mb, 100*mb)  // same bucket: silent
	progress(55*mb, 100*mb)  // jumps several buckets: one log
	progress(100*mb, 100*mb) // completion

	want := []string{
		"video download: 0% (0.0/100.0 MB)",
		"video download: 10% (10.0/100.0 MB)",
		"video download: 55% (55.0/100.0 MB)",
		"video download: 100% (100.0/100.0 MB)",
	}
	if !slices.Equal(recorder.messages, want) {
		t.Fatalf("logged messages = %q, want %q", recorder.messages, want)
	}

	for i, keyvals := range recorder.keyvals {
		if len(keyvals) != 2 || keyvals[0] != "recording" || keyvals[1] != "1/2" {
			t.Fatalf("message %d keyvals = %v, want [recording 1/2]", i, keyvals)
		}
	}
}
