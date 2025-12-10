package downloader

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// recordingInfo holds parsed information about a recording URL.
type recordingInfo struct {
	ID       string
	BaseURL  string
	Source   string
	Hostname string
}

// pageInfo holds information extracted from the recording page HTML.
type pageInfo struct {
	Title    string
	VideoSrc string // <video src="..."> URL that redirects to actual MP4
	VTTPath  string // <track src="..."> relative path to VTT
	Cookies  []*http.Cookie
}

// metadata represents the JSON metadata written for each download.
type metadata struct {
	Title        string    `json:"title"`
	SourceURL    string    `json:"source_url"`
	RecordingID  string    `json:"recording_id"`
	Hostname     string    `json:"hostname"`
	MP4Path      string    `json:"mp4_path,omitempty"`
	ZipPath      string    `json:"zip_path,omitempty"`
	ExtractedDir string    `json:"extracted_dir,omitempty"`
	DownloadedAt time.Time `json:"downloaded_at"`
	Warnings     []string  `json:"warnings,omitempty"`
}

// log is a helper that safely logs debug messages when logger is available.
func log(logger Logger, msg string, keyvals ...any) {
	if logger != nil {
		logger.Debug(msg, keyvals...)
	}
}

// logInfo is a helper that safely logs info messages when logger is available.
func logInfo(logger Logger, msg string, keyvals ...any) {
	if logger != nil {
		logger.Info(msg, keyvals...)
	}
}

// logWarn is a helper that safely logs warning messages when logger is available.
func logWarn(logger Logger, msg string, keyvals ...any) {
	if logger != nil {
		logger.Warn(msg, keyvals...)
	}
}

// logError is a helper that safely logs error messages when logger is available.
func logError(logger Logger, msg string, keyvals ...any) {
	if logger != nil {
		logger.Error(msg, keyvals...)
	}
}

// parseRecordingURL extracts recording info from a URL.
func parseRecordingURL(raw string) (recordingInfo, error) {
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return recordingInfo{}, fmt.Errorf("parse url: %w", err)
	}
	if u.Host == "" {
		return recordingInfo{}, errors.New("invalid url: host missing")
	}

	path := strings.Trim(u.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		return recordingInfo{}, errors.New("invalid url: recording id missing")
	}
	id := parts[len(parts)-1]
	basePath := "/" + strings.Join(parts, "/")
	baseURL := fmt.Sprintf("%s://%s%s", u.Scheme, u.Host, basePath)

	return recordingInfo{
		ID:       id,
		BaseURL:  baseURL,
		Source:   raw,
		Hostname: u.Host,
	}, nil
}

// resolveVTTURL converts a VTT filename or relative path to absolute URL.
func resolveVTTURL(baseURL, vttPath string) string {
	if strings.HasPrefix(vttPath, "http://") || strings.HasPrefix(vttPath, "https://") {
		return vttPath
	}
	// Parse baseURL to get scheme and host
	u, err := url.Parse(baseURL)
	if err != nil {
		return vttPath
	}
	// If it's just a filename (from transcriptFilename), construct output path
	if !strings.HasPrefix(vttPath, "/") {
		// vttPath is like "Recording Name.vtt"
		// Full path is baseURL/output/filename.vtt
		return fmt.Sprintf("%s/output/%s", baseURL, url.PathEscape(vttPath))
	}
	// vttPath is an absolute path like "/recording-id/output/..."
	return fmt.Sprintf("%s://%s%s", u.Scheme, u.Host, vttPath)
}

// parseHTMLTitle extracts the <title> content from HTML.
func parseHTMLTitle(r io.Reader) (string, error) {
	z := html.NewTokenizer(r)
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			if errors.Is(z.Err(), io.EOF) {
				return "", nil
			}
			return "", z.Err()
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			if t.Data == "title" {
				tt = z.Next()
				if tt == html.TextToken {
					return strings.TrimSpace(z.Token().Data), nil
				}
			}
		default:
			// Ignore other token types (text, end tag, comment, doctype)
		}
	}
}

// sanitize removes invalid filesystem characters from a name.
func sanitize(name string) string {
	name = strings.TrimSpace(name)
	re := regexp.MustCompile(`[<>:"/\\|?*\x00-\x1F]+`)
	name = re.ReplaceAllString(name, " ")
	name = strings.TrimSpace(name)
	if name == "" {
		return "recording"
	}
	return name
}

// extractZip extracts a ZIP archive to the destination directory.
func extractZip(zipPath, dest string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		target := filepath.Join(dest, f.Name)
		if !strings.HasPrefix(target, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path in zip: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, f.Mode()); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			return err
		}

		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}

		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return err
		}
		out.Close()
		rc.Close()
	}

	return nil
}

// writeMetadata writes download metadata as JSON.
func writeMetadata(root string, info recordingInfo, res Result) error {
	m := metadata{
		Title:        res.Title,
		SourceURL:    info.Source,
		RecordingID:  info.ID,
		Hostname:     info.Hostname,
		MP4Path:      res.MP4Path,
		ZipPath:      res.ZipPath,
		ExtractedDir: res.ExtractedDir,
		DownloadedAt: time.Now().UTC(),
		Warnings:     res.Warnings,
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "metadata.json"), data, 0o644)
}

// copyFile copies a file from src to dst.
func copyFile(dst, src string) error {
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()

	d, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer d.Close()

	_, err = io.Copy(d, s)
	return err
}
