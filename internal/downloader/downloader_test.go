package downloader

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRecordingURL(t *testing.T) {
	info, err := parseRecordingURL("https://example.com/foo/bar/")
	if err != nil {
		t.Fatalf("parseRecordingURL error: %v", err)
	}
	if info.ID != "bar" {
		t.Fatalf("expected id bar, got %s", info.ID)
	}
	expectedBase := "https://example.com/foo/bar"
	if info.BaseURL != expectedBase {
		t.Fatalf("expected base %s, got %s", expectedBase, info.BaseURL)
	}
}

func TestDownloadHappyPath(t *testing.T) {
	mp4Content := make([]byte, 2048) // Large enough to pass size check
	copy(mp4Content, "mp4-data-start")
	zipContent := createZip(t, map[string]string{"a.txt": "hello"})
	var server *httptest.Server

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rec/":
			// Include casRecordingURL to trigger video download
			fmt.Fprintf(w, `<html><head><title>Sample Recording</title></head>
<body><script>var casRecordingURL = '%s/rec/output/rec.mp4?download=mp4';</script></body></html>`, server.URL)
		case "/rec/output/rec.mp4":
			w.Write(mp4Content)
		case "/rec/output/rec.zip":
			if r.URL.Query().Get("download") != "zip" {
				http.NotFound(w, r)
				return
			}
			w.Write(zipContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tmp := t.TempDir()
	dl := New(server.Client())
	opts := Options{OutputDir: tmp}

	url := server.URL + "/rec/?session=abc"
	res, err := dl.Download(context.Background(), url, opts)
	if err != nil {
		t.Fatalf("download error: %v", err)
	}
	if res.Title != "Sample Recording" {
		t.Fatalf("unexpected title: %s", res.Title)
	}
	if res.MP4Path == "" || res.ZipPath == "" || res.ExtractedDir == "" {
		t.Fatalf("expected mp4, zip and extracted paths")
	}
	assertFileContent(t, res.MP4Path, mp4Content)
	assertFileContent(t, filepath.Join(res.ExtractedDir, "a.txt"), []byte("hello"))
	if len(res.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", res.Warnings)
	}
}

func TestMissingMP4StillSucceedsWithZip(t *testing.T) {
	zipContent := createZip(t, map[string]string{"a.txt": "hello"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rec/":
			w.Write([]byte("<title>No MP4</title>"))
		case "/rec/output/rec.zip":
			w.Write(zipContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tmp := t.TempDir()
	dl := New(server.Client())
	res, err := dl.Download(context.Background(), server.URL+"/rec/", Options{OutputDir: tmp})
	if err != nil {
		t.Fatalf("download error: %v", err)
	}
	if res.MP4Path != "" {
		t.Fatalf("expected no mp4 path")
	}
	if len(res.Warnings) == 0 {
		t.Fatalf("expected warnings for missing mp4")
	}
}

func TestDiscoversMP4FromCASRecordingURL(t *testing.T) {
	mp4Content := make([]byte, 2048) // Large enough to pass size check
	copy(mp4Content, "mp4-data-start")
	var server *httptest.Server
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rec/":
			// Page with casRecordingURL in JavaScript
			fmt.Fprintf(w, `<html><head><title>Has CAS Recording</title></head>
<body><script>var casRecordingURL = '%s/cas/video.mp4';</script></body></html>`, server.URL)
		case "/cas/video.mp4":
			w.Write(mp4Content)
		default:
			http.NotFound(w, r)
		}
	})
	server = httptest.NewServer(handler)
	defer server.Close()

	tmp := t.TempDir()
	dl := New(server.Client())

	res, err := dl.Download(context.Background(), server.URL+"/rec/", Options{OutputDir: tmp})
	if err != nil {
		t.Fatalf("download error: %v", err)
	}
	assertFileContent(t, res.MP4Path, mp4Content)
}

func TestInvalidZipIsWarning(t *testing.T) {
	mp4Content := make([]byte, 2048) // Large enough to pass size check
	copy(mp4Content, "mp4-data-start")
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rec/":
			// Include casRecordingURL to trigger video download
			fmt.Fprintf(w, `<html><head><title>Bad Zip</title></head>
<body><script>var casRecordingURL = '%s/rec/output/rec.mp4';</script></body></html>`, server.URL)
		case "/rec/output/rec.mp4":
			w.Write(mp4Content)
		case "/rec/output/rec.zip":
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("not-a-zip"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tmp := t.TempDir()
	dl := New(server.Client())
	res, err := dl.Download(context.Background(), server.URL+"/rec/", Options{OutputDir: tmp})
	if err != nil {
		t.Fatalf("download error: %v", err)
	}
	if res.ZipPath != "" {
		t.Fatalf("zip path should be empty for invalid zip")
	}
	if res.ExtractedDir != "" {
		t.Fatalf("extracted dir should be empty for invalid zip")
	}
	if len(res.Warnings) == 0 {
		t.Fatalf("expected warnings for invalid zip")
	}
}

func TestSessionTakenFromQueryParam(t *testing.T) {
	mp4Content := make([]byte, 2048) // Large enough to pass size check
	copy(mp4Content, "mp4-data-start")
	var pageCookie, assetCookie bool
	var server *httptest.Server

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookies := r.Cookies()
		for _, c := range cookies {
			if c.Name == "BREEZESESSION" && c.Value == "abc123" {
				if strings.HasPrefix(r.URL.Path, "/rec/output/") && strings.HasSuffix(r.URL.Path, ".mp4") {
					assetCookie = true
					w.Write(mp4Content)
					return
				}
				if r.URL.Path == "/rec/" {
					pageCookie = true
					// Include casRecordingURL to trigger video download
					fmt.Fprintf(w, `<html><head><title>Cookie Test</title></head>
<body><script>var casRecordingURL = '%s/rec/output/rec.mp4';</script></body></html>`, server.URL)
					return
				}
			}
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	tmp := t.TempDir()
	dl := New(server.Client())
	res, err := dl.Download(context.Background(), server.URL+"/rec/?session=abc123", Options{OutputDir: tmp})
	if err != nil {
		t.Fatalf("download error: %v", err)
	}
	assertFileContent(t, res.MP4Path, mp4Content)
	if !pageCookie || !assetCookie {
		t.Fatalf("expected session cookie to be used for page and asset requests")
	}
}

// Tests for user mapping extraction
func TestExtractUserMapping(t *testing.T) {
	rawDir := filepath.Join("testdata", "lecture1")

	mapping := extractUserMapping(rawDir)

	if len(mapping) == 0 {
		t.Fatal("expected user mapping to contain entries")
	}

	// Check specific mappings (based on anonymized test data)
	expected := map[string]string{
		"User1": "Jane Doe", // Tech Jane Doe, prefix stripped
		"User2": "James Lewis",
		"User3": "Alex Adams",
		"User4": "Oliver Green",
	}

	for userID, expectedName := range expected {
		if got := mapping[userID]; got != expectedName {
			t.Errorf("mapping[%s] = %q, want %q", userID, got, expectedName)
		}
	}
}

func TestExtractUserMappingMissingFile(t *testing.T) {
	mapping := extractUserMapping("/nonexistent/path")
	if len(mapping) != 0 {
		t.Errorf("expected empty mapping for missing file, got %v", mapping)
	}
}

// Tests for lecturer name extraction
func TestExtractLecturerNameFromTranscript(t *testing.T) {
	rawDir := filepath.Join("testdata", "lecture1")

	name := extractLecturerName(rawDir)

	// Lecture1 uses "Tech  Jane Doe" format in transcript
	if name != "Jane Doe" {
		t.Errorf("extractLecturerName() = %q, want %q", name, "Jane Doe")
	}
}

func TestExtractLecturerNameFromTitleFiles(t *testing.T) {
	rawDir := filepath.Join("testdata", "lecture2")

	name := extractLecturerName(rawDir)

	// Lecture2 has "Lecturer: Jane Doe" in fttitle files
	if name != "Jane Doe" {
		t.Errorf("extractLecturerName() = %q, want %q", name, "Jane Doe")
	}
}

func TestExtractLecturerName(t *testing.T) {
	tests := []struct {
		name     string
		rawDir   string
		expected string
	}{
		{
			name:     "lecture1 with Tech pattern in transcript",
			rawDir:   filepath.Join("testdata", "lecture1"),
			expected: "Jane Doe",
		},
		{
			name:     "lecture5 with Tech - pattern",
			rawDir:   filepath.Join("testdata", "lecture5"),
			expected: "Sarah Miller",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractLecturerName(tt.rawDir)
			if got != tt.expected {
				t.Errorf("extractLecturerName(%q) = %q, want %q", tt.rawDir, got, tt.expected)
			}
		})
	}
}

// Tests for speaker marker cleaning
func TestCleanSpeakerMarkers(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "User1 becomes Lecturer",
			input:    "@:@User1@:@ Hello class",
			expected: "Lecturer: Hello class",
		},
		{
			name:     "Other users become Speaker N",
			input:    "@:@User5@:@ I have a question",
			expected: "Speaker 5: I have a question",
		},
		{
			name:     "Multiple users in same line",
			input:    "@:@User1@:@ Yes @:@User2@:@ thank you",
			expected: "Lecturer: Yes Speaker 2: thank you",
		},
		{
			name:     "No markers unchanged",
			input:    "Just regular text",
			expected: "Just regular text",
		},
		{
			name:     "Removes leftover markers",
			input:    "Text with @:@ leftover",
			expected: "Text with  leftover",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanSpeakerMarkers(tt.input, "", nil)
			if got != tt.expected {
				t.Errorf("cleanSpeakerMarkers(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestCleanSpeakerMarkersWithLecturer(t *testing.T) {
	input := "@:@User1@:@ Welcome to class"
	expected := "Jane Doe: Welcome to class"

	got := cleanSpeakerMarkers(input, "Jane Doe", nil)
	if got != expected {
		t.Errorf("cleanSpeakerMarkers() with lecturerName = %q, want %q", got, expected)
	}
}

func TestCleanSpeakerMarkersWithMapping(t *testing.T) {
	mapping := map[string]string{
		"User1": "Dr. Smith",
		"User2": "Alice",
		"User3": "Bob",
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "User1 uses real name",
			input:    "@:@User1@:@ Welcome",
			expected: "Dr. Smith: Welcome",
		},
		{
			name:     "User2 uses real name",
			input:    "@:@User2@:@ Hello",
			expected: "Alice: Hello",
		},
		{
			name:     "Unknown user falls back to Speaker N",
			input:    "@:@User99@:@ Hi",
			expected: "Speaker 99: Hi",
		},
		{
			name:     "Multiple mapped users",
			input:    "@:@User1@:@ Hi @:@User2@:@ Thanks @:@User3@:@ Bye",
			expected: "Dr. Smith: Hi Alice: Thanks Bob: Bye",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanSpeakerMarkers(tt.input, "", mapping)
			if got != tt.expected {
				t.Errorf("cleanSpeakerMarkers(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// Tests for VTT file cleaning
func TestCleanVTTFile(t *testing.T) {
	// Use the raw VTT file with @:@ markers
	srcPath := filepath.Join("testdata", "lecture1", "raw_captions.vtt")
	tmp := t.TempDir()
	dstPath := filepath.Join(tmp, "cleaned.vtt")

	// User1 is the lecturer (Tech Jane Doe), mapping to "Lecturer"
	mapping := map[string]string{
		"User1": "Lecturer",
		"User2": "Student A",
		"User3": "Student B",
		"User4": "Student C",
	}

	err := cleanVTTFile(srcPath, dstPath, "", mapping)
	if err != nil {
		t.Fatalf("cleanVTTFile() error = %v", err)
	}

	// Read and check the cleaned content
	content, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("read cleaned file error = %v", err)
	}

	contentStr := string(content)

	// Check that mapping was applied - User1 becomes "Lecturer"
	if !strings.Contains(contentStr, "Lecturer:") {
		t.Error("expected cleaned VTT to contain 'Lecturer:'")
	}

	// Check that raw markers are removed
	if strings.Contains(contentStr, "@:@") {
		t.Error("expected cleaned VTT to not contain '@:@' markers")
	}
}

// Tests for chat log extraction
func TestExtractChatLog(t *testing.T) {
	rawDir := filepath.Join("testdata", "lecture1")
	tmp := t.TempDir()
	outputPath := filepath.Join(tmp, "chat.txt")

	err := extractChatLog(rawDir, outputPath)
	if err != nil {
		t.Fatalf("extractChatLog() error = %v", err)
	}

	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read chat log error = %v", err)
	}

	contentStr := string(content)

	// Check header
	if !strings.Contains(contentStr, "CHAT LOG") {
		t.Error("expected chat log to contain header")
	}

	// Check for chat messages (from actual anonymized data)
	if !strings.Contains(contentStr, "Hello all") {
		t.Error("expected chat log to contain 'Hello all'")
	}
	if !strings.Contains(contentStr, "Ashley Cooper") {
		t.Error("expected chat log to contain 'Ashley Cooper'")
	}
}

// Tests for URL conversion
func TestConvertToDirectDownloadURL(t *testing.T) {
	tests := []struct {
		name      string
		systemURL string
		filename  string
		expected  string
	}{
		{
			name:      "normal conversion",
			systemURL: "/system/download?download-url=/_a123/test/output/&name=file.pdf",
			filename:  "file.pdf",
			expected:  "/_a123/test/output/file.pdf?download=true",
		},
		{
			name:      "filename with spaces",
			systemURL: "/system/download?download-url=/_a123/test/output/&name=My+File.docx",
			filename:  "My File.docx",
			expected:  "/_a123/test/output/My%20File.docx?download=true",
		},
		{
			name:      "no download-url param returns original",
			systemURL: "/system/download?name=file.pdf",
			filename:  "file.pdf",
			expected:  "/system/download?name=file.pdf",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertToDirectDownloadURL(tt.systemURL, tt.filename)
			if got != tt.expected {
				t.Errorf("convertToDirectDownloadURL() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// Tests for progress reader
func TestProgressReader(t *testing.T) {
	data := []byte("hello world")
	reader := bytes.NewReader(data)

	var callbacks []int64
	pr := &progressReader{
		reader: reader,
		total:  int64(len(data)),
		onProgress: func(downloaded, _ int64) {
			callbacks = append(callbacks, downloaded)
		},
	}

	buf := make([]byte, 5)

	// First read
	n, err := pr.Read(buf)
	if err != nil {
		t.Fatalf("first read error: %v", err)
	}
	if n != 5 {
		t.Errorf("first read n = %d, want 5", n)
	}
	if pr.downloaded != 5 {
		t.Errorf("downloaded = %d, want 5", pr.downloaded)
	}

	// Second read
	n, err = pr.Read(buf)
	if err != nil {
		t.Fatalf("second read error: %v", err)
	}
	if n != 5 {
		t.Errorf("second read n = %d, want 5", n)
	}
	if pr.downloaded != 10 {
		t.Errorf("downloaded = %d, want 10", pr.downloaded)
	}

	// Check callbacks were called
	if len(callbacks) < 2 {
		t.Errorf("expected at least 2 callbacks, got %d", len(callbacks))
	}
}

// Tests for FindVTTPath
func TestFindVTTPath(t *testing.T) {
	tmp := t.TempDir()

	// Create a captions.vtt file in the root
	vttPath := filepath.Join(tmp, "captions.vtt")
	if err := os.WriteFile(vttPath, []byte("WEBVTT\n\n"), 0o644); err != nil {
		t.Fatalf("create vtt file: %v", err)
	}

	result := Result{
		RootDir: tmp,
	}

	got := FindVTTPath(result)
	if got != vttPath {
		t.Errorf("FindVTTPath() = %q, want %q", got, vttPath)
	}
}

func TestFindVTTPathNotFound(t *testing.T) {
	tmp := t.TempDir()

	result := Result{
		RootDir: tmp,
	}

	got := FindVTTPath(result)
	if got != "" {
		t.Errorf("FindVTTPath() = %q, want empty string", got)
	}
}

func createZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry: %v", err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatalf("write zip entry: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func assertFileContent(t *testing.T, path string, expected []byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file %s: %v", path, err)
	}
	if !bytes.Equal(data, expected) {
		t.Fatalf("unexpected content for %s: %s", path, string(data))
	}
}
