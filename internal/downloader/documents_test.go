package downloader

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testFileShareXML = `<root>
<Message><newValue>
  <name><![CDATA[Week 1 Slides.pptx]]></name>
  <size><![CDATA[4096]]></size>
  <downloadUrl><![CDATA[/system/download?download-url=/_a7/upload/source/&name=Week 1 Slides.pptx]]></downloadUrl>
</newValue></Message>
<Message><newValue>
  <name><![CDATA[Week 1 Slides.pptx]]></name>
  <size><![CDATA[8192]]></size>
  <playbackFileName><![CDATA[/system/download?download-url=/_a7/p123/output/&name=Week 1 Slides.pptx]]></playbackFileName>
</newValue></Message>
<Message><newValue>
  <name><![CDATA[Notes.pdf]]></name>
  <downloadUrl><![CDATA[/system/download?download-url=/_a7/p123/output/&name=Notes.pdf]]></downloadUrl>
</newValue></Message>
<Message><newValue>
  <name><![CDATA[External.pdf]]></name>
  <downloadUrl><![CDATA[https://elsewhere.example.com/External.pdf]]></downloadUrl>
</newValue></Message>
<Message><newValue>
  <downloadUrl><![CDATA[/system/download?download-url=/_a7/p123/output/&name=Nameless.pdf]]></downloadUrl>
</newValue></Message>
</root>`

func TestExtractDocumentLinks(t *testing.T) {
	rawDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rawDir, "ftfileshare1.xml"), []byte(testFileShareXML), 0o644); err != nil {
		t.Fatalf("write fileshare xml: %v", err)
	}
	// Files from other pods must be ignored.
	if err := os.WriteFile(filepath.Join(rawDir, "ftchat1.xml"), []byte(testFileShareXML), 0o644); err != nil {
		t.Fatalf("write chat xml: %v", err)
	}

	docs := extractDocumentLinks(rawDir, "bpp.example.com")

	byName := make(map[string]DocumentInfo, len(docs))
	for _, doc := range docs {
		byName[doc.Name] = doc
	}
	if len(docs) != 2 || len(byName) != 2 {
		t.Fatalf("expected 2 unique documents, got %d: %+v", len(docs), docs)
	}

	tests := []struct {
		name     string
		wantURL  string
		wantSize int64
	}{
		{
			// The playbackFileName entry replaces the earlier downloadUrl entry.
			name:     "Week 1 Slides.pptx",
			wantURL:  "https://bpp.example.com/_a7/p123/output/Week%201%20Slides.pptx?download=true",
			wantSize: 8192,
		},
		{
			name:     "Notes.pdf",
			wantURL:  "https://bpp.example.com/_a7/p123/output/Notes.pdf?download=true",
			wantSize: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, ok := byName[tt.name]
			if !ok {
				t.Fatalf("document %q not found in %+v", tt.name, docs)
			}
			if doc.DownloadURL != tt.wantURL {
				t.Errorf("DownloadURL = %q, want %q", doc.DownloadURL, tt.wantURL)
			}
			if doc.Size != tt.wantSize {
				t.Errorf("Size = %d, want %d", doc.Size, tt.wantSize)
			}
		})
	}
}

// Newer Adobe Connect recordings store attachments in the resource pod
// (ftresource*.xml) instead of the file share pod.
const testResourceXML = `<root>
<Message><Array><Object>
  <code><![CDATA[change]]></code>
  <newValue>
    <downloadUrl><![CDATA[/system/download?download-url=/_a7/pup/output/&name=Topic+1.pdf]]></downloadUrl>
    <encodedName><![CDATA[Topic%201.pdf]]></encodedName>
    <name><![CDATA[Topic 1.pdf]]></name>
    <playbackFileName><![CDATA[/system/download?download-url=/_a7/pplay/output/&name=Topic+1.pdf]]></playbackFileName>
    <resourceType><![CDATA[file]]></resourceType>
    <size><![CDATA[675335]]></size>
  </newValue>
</Object></Array></Message>
<Message><Array><Object>
  <newValue>
    <name><![CDATA[Skills England]]></name>
    <resourceType><![CDATA[link]]></resourceType>
    <url><![CDATA[https://example.com/standard]]></url>
  </newValue>
</Object></Array></Message>
</root>`

func TestExtractDocumentLinksResourcePod(t *testing.T) {
	rawDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rawDir, "ftresource7.xml"), []byte(testResourceXML), 0o644); err != nil {
		t.Fatalf("write resource xml: %v", err)
	}
	// The same attachment listed by both pod types must only be reported once.
	if err := os.WriteFile(filepath.Join(rawDir, "ftfileshare1.xml"), []byte(testResourceXML), 0o644); err != nil {
		t.Fatalf("write fileshare xml: %v", err)
	}

	docs := extractDocumentLinks(rawDir, "bpp.example.com")

	if len(docs) != 1 {
		t.Fatalf("expected 1 document, got %d: %+v", len(docs), docs)
	}
	want := DocumentInfo{
		Name:        "Topic 1.pdf",
		DownloadURL: "https://bpp.example.com/_a7/pplay/output/Topic%201.pdf?download=true",
		Size:        675335,
	}
	if docs[0] != want {
		t.Errorf("document = %+v, want %+v", docs[0], want)
	}
}

func TestExtractDocumentLinksResourcePodOnly(t *testing.T) {
	rawDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rawDir, "ftresource7.xml"), []byte(testResourceXML), 0o644); err != nil {
		t.Fatalf("write resource xml: %v", err)
	}

	docs := extractDocumentLinks(rawDir, "bpp.example.com")

	if len(docs) != 1 || docs[0].Name != "Topic 1.pdf" {
		t.Fatalf("expected Topic 1.pdf from the resource pod, got %+v", docs)
	}
}

// Web links pods store each link as an unnamed index, title, URL and type.
const testWebLinksXML = `<root>
<Message><Array>
<Object>
  <code><![CDATA[change]]></code>
  <name><![CDATA[0]]></name>
  <newValue>
    <Number><![CDATA[0]]></Number>
    <String><![CDATA[Library]]></String>
    <String><![CDATA[https://library.example.com/Home]]></String>
    <String><![CDATA[link]]></String>
  </newValue>
</Object>
<Object>
  <newValue>
    <Number><![CDATA[1]]></Number>
    <String><![CDATA[Feedback]]></String>
    <String><![CDATA[https://example.com/s/?a=1&b=2]]></String>
    <String><![CDATA[link]]></String>
  </newValue>
</Object>
</Array></Message>
</root>`

func TestExtractWebLinks(t *testing.T) {
	rawDir := t.TempDir()
	// The same pod is recorded once per layout, so links repeat across files.
	for _, name := range []string{"ftresource8.xml", "ftresource33.xml"} {
		if err := os.WriteFile(filepath.Join(rawDir, name), []byte(testWebLinksXML), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// Attachments are not web links.
	if err := os.WriteFile(filepath.Join(rawDir, "ftresource7.xml"), []byte(testResourceXML), 0o644); err != nil {
		t.Fatalf("write resource xml: %v", err)
	}

	links := extractWebLinks(rawDir)

	want := []LinkInfo{
		{Title: "Library", URL: "https://library.example.com/Home"},
		{Title: "Feedback", URL: "https://example.com/s/?a=1&b=2"},
	}
	if len(links) != len(want) {
		t.Fatalf("expected %d links, got %d: %+v", len(want), len(links), links)
	}
	for i := range want {
		if links[i] != want[i] {
			t.Errorf("link %d = %+v, want %+v", i, links[i], want[i])
		}
	}
}

func TestExtractWebLinksNone(t *testing.T) {
	if links := extractWebLinks(t.TempDir()); len(links) != 0 {
		t.Fatalf("expected no links, got %+v", links)
	}
}

func TestWriteLinkList(t *testing.T) {
	links := []LinkInfo{{Title: "Library", URL: "https://library.example.com/Home"}}
	path := filepath.Join(t.TempDir(), "links.txt")

	if err := writeLinkList(path, links); err != nil {
		t.Fatalf("writeLinkList error: %v", err)
	}

	want := "LECTURE LINKS\n" +
		"=============\n\n" +
		"1. Library\n" +
		"   URL: https://library.example.com/Home\n\n"
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read links file: %v", err)
	}
	if string(got) != want {
		t.Errorf("links file = %q, want %q", got, want)
	}
}

func TestWriteLinkListCreateError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "links.txt")
	if err := writeLinkList(path, nil); err == nil {
		t.Fatal("expected an error for a missing parent directory")
	}
}

func TestExtractDocumentLinksNoFileShare(t *testing.T) {
	docs := extractDocumentLinks(t.TempDir(), "bpp.example.com")
	if len(docs) != 0 {
		t.Fatalf("expected no documents, got %+v", docs)
	}
}

func TestWriteDocumentList(t *testing.T) {
	docs := []DocumentInfo{
		{Name: "Slides.pdf", DownloadURL: "https://example.com/Slides.pdf", Size: 4096},
		{Name: "Tiny.txt", DownloadURL: "https://example.com/Tiny.txt", Size: 10},
	}
	path := filepath.Join(t.TempDir(), "documents.txt")

	if err := writeDocumentList(path, docs); err != nil {
		t.Fatalf("writeDocumentList error: %v", err)
	}

	want := "LECTURE DOCUMENTS\n" +
		"=================\n\n" +
		"1. Slides.pdf (4 KB)\n" +
		"   URL: https://example.com/Slides.pdf\n\n" +
		// Sizes under 1 KB are rounded up rather than shown as 0 KB.
		"2. Tiny.txt (1 KB)\n" +
		"   URL: https://example.com/Tiny.txt\n\n"
	assertFileContent(t, path, []byte(want))
}

func TestWriteDocumentListUnwritablePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", "documents.txt")

	if err := writeDocumentList(path, nil); err == nil {
		t.Fatal("expected error writing into a missing directory")
	}
}

func TestUnescapeJS(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no escapes", "/path/captions.vtt", "/path/captions.vtt"},
		{"space escape", `/path/my\x20captions.vtt`, "/path/my captions.vtt"},
		{"uppercase hex", `a\x2Fb`, "a/b"},
		{"multiple escapes", `a\x20b\x20c`, "a b c"},
		{"incomplete escape left alone", `a\x2`, `a\x2`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := unescapeJS(tt.input); got != tt.want {
				t.Errorf("unescapeJS(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCopyFile(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.txt")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	t.Run("copies content", func(t *testing.T) {
		dst := filepath.Join(tmp, "dst.txt")
		if err := copyFile(dst, src); err != nil {
			t.Fatalf("copyFile error: %v", err)
		}
		assertFileContent(t, dst, []byte("payload"))
	})

	t.Run("missing source", func(t *testing.T) {
		if err := copyFile(filepath.Join(tmp, "out.txt"), filepath.Join(tmp, "nope.txt")); err == nil {
			t.Fatal("expected error copying a missing source file")
		}
	})

	t.Run("unwritable destination", func(t *testing.T) {
		if err := copyFile(filepath.Join(tmp, "missing-dir", "out.txt"), src); err == nil {
			t.Fatal("expected error copying into a missing directory")
		}
	})
}

// Guards against the worker WaitGroup being double-counted, which made the
// non-pool path block forever.
func TestDownloadDocumentsWithoutPool(t *testing.T) {
	server := newPoolServer(t, map[string][]byte{
		"/docs/slides.pdf": []byte("slides"),
		"/docs/notes.pdf":  []byte("notes"),
	})
	dl := New(server.Client())
	destDir := filepath.Join(t.TempDir(), "documents")
	docs := []DocumentInfo{
		{Name: "slides.pdf", DownloadURL: server.URL + "/docs/slides.pdf"},
		{Name: "notes.pdf", DownloadURL: server.URL + "/docs/notes.pdf"},
		{Name: "gone.pdf", DownloadURL: server.URL + "/docs/gone.pdf"},
	}

	done := make(chan int, 1)
	go func() {
		done <- dl.downloadDocuments(context.Background(), docs, destDir, nil, server.URL, newRecordingLogger())
	}()

	select {
	case got := <-done:
		if got != 2 {
			t.Fatalf("downloadDocuments() = %d, want 2", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("downloadDocuments did not return: worker WaitGroup never reaches zero")
	}

	assertFileContent(t, filepath.Join(destDir, "slides.pdf"), []byte("slides"))
	assertFileContent(t, filepath.Join(destDir, "notes.pdf"), []byte("notes"))
}
