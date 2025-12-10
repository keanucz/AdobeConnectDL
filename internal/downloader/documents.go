package downloader

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DocumentInfo represents a downloadable document from the recording.
type DocumentInfo struct {
	Name        string
	DownloadURL string
	Size        int64
}

// convertToDirectDownloadURL converts a /system/download URL to a direct download URL.
// From: /system/download?download-url=/_accountid/recordingid/output/&name=Document.pptx
// To:   /_accountid/recordingid/output/Document.pptx?download=true.
func convertToDirectDownloadURL(systemURL, filename string) string {
	// Parse the download-url parameter
	u, err := url.Parse(systemURL)
	if err != nil {
		return systemURL // Return original if parsing fails
	}

	downloadPath := u.Query().Get("download-url")
	if downloadPath == "" {
		return systemURL
	}

	// URL-encode the filename for the path
	encodedName := url.PathEscape(filename)

	// Build direct URL: path + filename + ?download=true
	return downloadPath + encodedName + "?download=true"
}

// documentEntry tracks a document during extraction, preferring playbackFileName URLs.
type documentEntry struct {
	info            DocumentInfo
	hasPlaybackName bool
}

// extractDocumentLinks parses ftfileshare*.xml for document links.
// Only ftfileshare*.xml contains reliable document URLs. The playbackFileName is
// preferred as it's the persistent URL for archived recordings, while downloadUrl
// may point to the original upload location which may no longer exist.
func extractDocumentLinks(rawDir, hostname string) []DocumentInfo {
	// Map by filename to deduplicate and prefer playbackFileName entries
	docsByName := make(map[string]*documentEntry)

	// Only parse ftfileshare*.xml - these contain the file share pod data with reliable URLs
	files, _ := filepath.Glob(filepath.Join(rawDir, "ftfileshare*.xml"))

	// Match newValue blocks containing downloadUrl or playbackFileName
	newValueRe := regexp.MustCompile(`(?s)<newValue>(.*?)</newValue>`)
	// playbackFileName is the persistent playback URL (preferred)
	playbackRe := regexp.MustCompile(`<playbackFileName><!\[CDATA\[([^\]]+)\]\]></playbackFileName>`)
	// downloadUrl is the original upload URL (fallback, may return 404)
	downloadRe := regexp.MustCompile(`<downloadUrl><!\[CDATA\[([^\]]+)\]\]></downloadUrl>`)
	nameRe := regexp.MustCompile(`<name><!\[CDATA\[([^\]]+)\]\]></name>`)
	sizeRe := regexp.MustCompile(`<size><!\[CDATA\[(\d+)\]\]></size>`)

	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		content := string(data)

		// Find all newValue blocks
		newValueMatches := newValueRe.FindAllStringSubmatch(content, -1)
		for _, nvMatch := range newValueMatches {
			if len(nvMatch) < 2 {
				continue
			}
			block := nvMatch[1]

			// Extract playbackFileName and downloadUrl from this block
			playbackMatch := playbackRe.FindStringSubmatch(block)
			dlMatch := downloadRe.FindStringSubmatch(block)
			nameMatch := nameRe.FindStringSubmatch(block)
			sizeMatch := sizeRe.FindStringSubmatch(block)

			// Need at least a name
			if len(nameMatch) < 2 {
				continue
			}
			name := nameMatch[1]

			// Determine URL: prefer playbackFileName over downloadUrl
			var downloadURL string
			hasPlayback := false
			if len(playbackMatch) >= 2 && strings.Contains(playbackMatch[1], "/system/download") {
				downloadURL = playbackMatch[1]
				hasPlayback = true
			} else if len(dlMatch) >= 2 && strings.Contains(dlMatch[1], "/system/download") {
				downloadURL = dlMatch[1]
			}

			if downloadURL == "" {
				continue
			}

			var size int64
			if len(sizeMatch) >= 2 {
				_, _ = fmt.Sscanf(sizeMatch[1], "%d", &size)
			}

			// Convert /system/download URL to direct download URL
			directURL := convertToDirectDownloadURL(downloadURL, name)
			fullURL := fmt.Sprintf("https://%s%s", hostname, directURL)

			// Check if we already have this document
			existing, exists := docsByName[name]
			if !exists {
				// First time seeing this document
				docsByName[name] = &documentEntry{
					info: DocumentInfo{
						Name:        name,
						DownloadURL: fullURL,
						Size:        size,
					},
					hasPlaybackName: hasPlayback,
				}
			} else if hasPlayback && !existing.hasPlaybackName {
				// We have a playbackFileName URL and the existing one doesn't - replace it
				existing.info.DownloadURL = fullURL
				existing.hasPlaybackName = true
				if size > 0 {
					existing.info.Size = size
				}
			}
		}
	}

	// Convert map to slice
	docs := make([]DocumentInfo, 0, len(docsByName))
	for _, entry := range docsByName {
		docs = append(docs, entry.info)
	}

	return docs
}

// writeDocumentList writes the document list to a file.
func writeDocumentList(path string, docs []DocumentInfo) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Fprintf(f, "LECTURE DOCUMENTS\n")
	fmt.Fprintf(f, "=================\n\n")

	for i, doc := range docs {
		sizeKB := doc.Size / 1024
		if sizeKB == 0 {
			sizeKB = 1
		}
		fmt.Fprintf(f, "%d. %s (%d KB)\n", i+1, doc.Name, sizeKB)
		fmt.Fprintf(f, "   URL: %s\n\n", doc.DownloadURL)
	}

	return nil
}
