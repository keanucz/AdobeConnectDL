package downloader

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
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

// DocumentInfo represents a downloadable document from the recording.
type DocumentInfo struct {
	Name        string
	DownloadURL string
	Size        int64
}

// progressReader wraps an io.Reader and reports progress.
type progressReader struct {
	reader     io.Reader
	total      int64
	downloaded int64
	onProgress ProgressCallback
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	if n > 0 {
		pr.downloaded += int64(n)
		if pr.onProgress != nil {
			pr.onProgress(pr.downloaded, pr.total)
		}
	}
	return n, err
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

// requestOptions configures HTTP request headers and cookies.
type requestOptions struct {
	Cookies    []*http.Cookie
	Referer    string
	AcceptType string // "video" or "" for default
}

const defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
const videoAccept = "video/webm,video/ogg,video/*;q=0.9,application/ogg;q=0.7,audio/*;q=0.6,*/*;q=0.5"

// applyRequestOptions sets common headers and cookies on an HTTP request.
func applyRequestOptions(req *http.Request, opts requestOptions) {
	for _, c := range opts.Cookies {
		req.AddCookie(c)
	}
	if opts.Referer != "" {
		req.Header.Set("Referer", opts.Referer)
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	if opts.AcceptType == "video" {
		req.Header.Set("Accept", videoAccept)
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

// mergeCookies combines session cookie with extra cookies, avoiding duplicates.
func mergeCookies(session string, extra []*http.Cookie) []*http.Cookie {
	seen := map[string]bool{}
	cookies := make([]*http.Cookie, 0, len(extra)+1)

	if session != "" {
		cookies = append(cookies, &http.Cookie{Name: "BREEZESESSION", Value: session})
		seen["BREEZESESSION"] = true
	}

	for _, c := range extra {
		if c == nil || c.Name == "" {
			continue
		}
		key := c.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		cookies = append(cookies, c)
	}

	return cookies
}

// findCASRecordingURL extracts the casRecordingURL from JavaScript in the HTML.
// This URL points to the signed streaming endpoint that redirects to the actual MP4.
func findCASRecordingURL(body []byte) string {
	// Look for: var casRecordingURL = 'https://...';
	re := regexp.MustCompile(`var\s+casRecordingURL\s*=\s*'([^']+)'`)
	match := re.FindSubmatch(body)
	if len(match) >= 2 {
		return string(match[1])
	}
	return ""
}

// findVTTFromJS extracts the VTT filename from JavaScript.
func findVTTFromJS(body []byte) string {
	// Look for transcriptFilename variable
	re := regexp.MustCompile(`var\s+transcriptFilename\s*=\s*'([^']+\.vtt)'`)
	match := re.FindSubmatch(body)
	if len(match) >= 2 {
		// Unescape JavaScript \x20 etc
		return unescapeJS(string(match[1]))
	}
	// Fallback: look for .vtt references in paths
	vttRe := regexp.MustCompile(`['"](/[^'"]+\.vtt)['"]`)
	match = vttRe.FindSubmatch(body)
	if len(match) >= 2 {
		return string(match[1])
	}
	return ""
}

// unescapeJS handles simple JavaScript escape sequences like \x20.
func unescapeJS(s string) string {
	re := regexp.MustCompile(`\\x([0-9a-fA-F]{2})`)
	return re.ReplaceAllStringFunc(s, func(m string) string {
		hex := m[2:]
		var b byte
		_, _ = fmt.Sscanf(hex, "%x", &b)
		return string(b)
	})
}

// parseVideoElement extracts <video src="..."> and <track src="..."> from HTML.
func parseVideoElement(body []byte) (videoSrc, vttPath string) {
	z := html.NewTokenizer(bytes.NewReader(body))
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return videoSrc, vttPath
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			switch t.Data {
			case "video":
				for _, a := range t.Attr {
					if a.Key == "src" && a.Val != "" {
						videoSrc = a.Val
					}
				}
			case "track":
				for _, a := range t.Attr {
					if a.Key == "src" && a.Val != "" {
						vttPath = a.Val
					}
				}
			}
		default:
			// Ignore other token types (text, end tag, comment, doctype)
		}
	}
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

// isHTMLResponse checks if a response appears to be HTML.
func isHTMLResponse(headers http.Header, chunk []byte) bool {
	ct := headers.Get("Content-Type")
	if strings.Contains(strings.ToLower(ct), "text/html") {
		return true
	}
	trimmed := bytes.TrimSpace(chunk)
	return len(trimmed) > 0 && trimmed[0] == '<'
}

// isZipSignature checks if bytes start with ZIP file signature.
func isZipSignature(b []byte) bool {
	return len(b) >= 4 && b[0] == 'P' && b[1] == 'K' && b[2] == 0x03 && b[3] == 0x04
}

// embedSubtitles uses ffmpeg to embed VTT subtitles as mov_text into an MP4.
// The subtitles are set as the default track.
func embedSubtitles(ctx context.Context, ff FFmpegRunner, mp4Path, vttPath, outputPath string, logger Logger) error {
	// ffmpeg -i video.mp4 -i captions.vtt -c:v copy -c:a copy -c:s mov_text \
	//   -metadata:s:s:0 language=eng -disposition:s:0 default \
	//   -movflags +faststart output.mp4
	args := []string{
		"-y",
		"-i", mp4Path,
		"-i", vttPath,
		"-c:v", "copy",
		"-c:a", "copy",
		"-c:s", "mov_text",
		"-metadata:s:s:0", "language=eng",
		"-disposition:s:0", "default",
		"-movflags", "+faststart",
		outputPath,
	}

	log(logger, "embedding subtitles", "args", args)

	var stderr bytes.Buffer
	if err := ff.Run(ctx, args, nil, &stderr); err != nil {
		return fmt.Errorf("ffmpeg: %w\nstderr: %s", err, stderr.String())
	}

	return nil
}

// vttToTranscript converts a VTT file to a readable plain text transcript.
// The output is formatted with timestamps and speaker text, suitable for AI tools.
func vttToTranscript(vttPath, outputPath string) error {
	f, err := os.Open(vttPath)
	if err != nil {
		return err
	}
	defer f.Close()

	out, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer out.Close()

	scanner := bufio.NewScanner(f)
	var (
		inCue       bool
		lastText    string
		cueStart    string
		lineCount   int
		wroteHeader bool
	)

	// VTT format:
	// WEBVTT
	//
	// 00:00:00.000 --> 00:00:05.000
	// Speaker text here
	//
	// 00:00:05.000 --> 00:00:10.000
	// More text here

	timeRe := regexp.MustCompile(`^(\d{2}:\d{2}:\d{2})[.,]\d{3}\s*-->\s*\d{2}:\d{2}:\d{2}`)

	for scanner.Scan() {
		line := scanner.Text()
		lineCount++

		// Skip WEBVTT header and metadata
		if lineCount == 1 && strings.HasPrefix(line, "WEBVTT") {
			continue
		}

		// Check for timestamp line
		if match := timeRe.FindStringSubmatch(line); len(match) >= 2 {
			inCue = true
			cueStart = match[1]
			continue
		}

		// Empty line ends a cue
		if strings.TrimSpace(line) == "" {
			inCue = false
			continue
		}

		// Skip cue identifiers (numeric or alphanumeric IDs before timestamps)
		if !inCue {
			continue
		}

		// This is cue text
		text := strings.TrimSpace(line)
		// Remove HTML tags like <v Speaker>
		text = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(text, "")
		// Clean up speaker markers like @:@User1@:@ -> Lecturer:
		text = cleanSpeakerMarkers(text, "", nil)
		text = strings.TrimSpace(text)

		if text == "" || text == lastText {
			continue
		}

		if !wroteHeader {
			fmt.Fprintf(out, "TRANSCRIPT\n")
			fmt.Fprintf(out, "==========\n\n")
			wroteHeader = true
		}

		fmt.Fprintf(out, "[%s] %s\n", cueStart, text)
		lastText = text
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	return nil
}

// cleanSpeakerMarkers replaces Adobe Connect speaker markers with real names or readable labels.
// @:@User1@:@ -> actual name from mapping, or lecturerName, or "Lecturer:"
// @:@UserN@:@ -> actual name from mapping, or "Speaker N:"
// Also removes any remaining @:@ markers.
func cleanSpeakerMarkers(text, lecturerName string, userMapping map[string]string) string {
	// Replace @:@UserN@:@ with real names or fallback labels
	text = regexp.MustCompile(`@:@User(\d+)@:@`).ReplaceAllStringFunc(text, func(match string) string {
		re := regexp.MustCompile(`@:@User(\d+)@:@`)
		m := re.FindStringSubmatch(match)
		if len(m) >= 2 {
			userNum := m[1]
			userKey := "User" + userNum

			// Try to use real name from mapping
			if userMapping != nil {
				if realName, ok := userMapping[userKey]; ok && realName != "" {
					return realName + ":"
				}
			}

			// Fallback for User1 (lecturer)
			if userNum == "1" {
				if lecturerName != "" {
					return lecturerName + ":"
				}
				return "Lecturer:"
			}

			// Fallback for other users
			return fmt.Sprintf("Speaker %s:", userNum)
		}
		return match
	})

	// Remove any remaining @:@ markers
	text = strings.ReplaceAll(text, "@:@", "")
	return text
}

// cleanVTTFile creates a cleaned copy of a VTT file with speaker markers replaced by real names.
func cleanVTTFile(srcPath, dstPath, lecturerName string, userMapping map[string]string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer dst.Close()

	scanner := bufio.NewScanner(src)
	for scanner.Scan() {
		line := scanner.Text()
		// Clean speaker markers in cue text lines
		cleaned := cleanSpeakerMarkers(line, lecturerName, userMapping)
		fmt.Fprintln(dst, cleaned)
	}

	return scanner.Err()
}

// extractLecturerName finds the lecturer name from various XML sources.
// This is a fallback when user mapping from indexstream.xml is unavailable.
// It checks multiple patterns in the raw recording files.
func extractLecturerName(rawDir string) string {
	// First try: transcriptstream.xml for "[Tech - Name]" or similar patterns
	transcriptPath := filepath.Join(rawDir, "transcriptstream.xml")
	if f, err := os.Open(transcriptPath); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		// Look for pattern: [Tech - Name] or [Tech  Name] has joined the stage
		// Matches "Tech" followed by optional dash/spaces and the name
		techRe := regexp.MustCompile(`\[Tech\s*[-–]?\s*([^\]]+)\]`)

		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "has joined the stage") {
				if match := techRe.FindStringSubmatch(line); len(match) >= 2 {
					return strings.TrimSpace(match[1])
				}
			}
		}
	}

	// Second try: fttitle*.xml for "Lecturer:" pattern
	files, _ := filepath.Glob(filepath.Join(rawDir, "fttitle*.xml"))
	// Match "Lecturer:" followed by optional HTML tags and the actual name
	lecturerRe := regexp.MustCompile(`Lecturer:\s*(?:<[^>]+>\s*)*([A-Z][a-zA-Z]+(?:\s+[A-Z][a-zA-Z]+)+)`)

	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}

		if match := lecturerRe.FindStringSubmatch(string(data)); len(match) >= 2 {
			return strings.TrimSpace(match[1])
		}
	}

	return ""
}

// extractUserMapping parses indexstream.xml to get the mapping from anonymous IDs to real names.
// Returns a map like {"User1": "Jane Smith", "User13": "John Doe", ...}.
func extractUserMapping(rawDir string) map[string]string {
	mapping := make(map[string]string)

	indexPath := filepath.Join(rawDir, "indexstream.xml")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return mapping
	}

	content := string(data)

	// Match pairs of anonymousName and fullName
	anonRe := regexp.MustCompile(`<anonymousName><!\[CDATA\[([^\]]+)\]\]></anonymousName>`)
	nameRe := regexp.MustCompile(`<fullName><!\[CDATA\[([^\]]*)\]\]></fullName>`)

	anonMatches := anonRe.FindAllStringSubmatchIndex(content, -1)
	nameMatches := nameRe.FindAllStringSubmatch(content, -1)

	// Match by order - each anonymousName is followed by its fullName
	for i, anonMatch := range anonMatches {
		if i < len(nameMatches) {
			anonName := content[anonMatch[2]:anonMatch[3]]
			fullName := nameMatches[i][1]
			if fullName != "" {
				// Clean up "Tech " prefix from lecturer names
				fullName = strings.TrimPrefix(fullName, "Tech ")
				fullName = strings.TrimSpace(fullName)
				mapping[anonName] = fullName
			}
		}
	}

	return mapping
}

// extractChatLog parses transcriptstream.xml and creates a readable chat log.
func extractChatLog(rawDir, outputPath string) error {
	transcriptPath := filepath.Join(rawDir, "transcriptstream.xml")
	f, err := os.Open(transcriptPath)
	if err != nil {
		return err
	}
	defer f.Close()

	out, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer out.Close()

	fmt.Fprintf(out, "CHAT LOG\n")
	fmt.Fprintf(out, "========\n\n")

	scanner := bufio.NewScanner(f)
	// Regex patterns for extracting chat data
	iconTypeRe := regexp.MustCompile(`<iconType><!\[CDATA\[([^\]]+)\]\]></iconType>`)
	labelRe := regexp.MustCompile(`<label><!\[CDATA\[([^\]]*)\]\]></label>`)
	nameRe := regexp.MustCompile(`<name><!\[CDATA\[([^\]]*)\]\]></name>`)
	timeRe := regexp.MustCompile(`<time><!\[CDATA\[(\d+)\]\]></time>`)

	var currentIconType, currentLabel, currentName string
	var currentTime int64

	for scanner.Scan() {
		line := scanner.Text()

		if match := iconTypeRe.FindStringSubmatch(line); len(match) >= 2 {
			currentIconType = match[1]
		}
		if match := labelRe.FindStringSubmatch(line); len(match) >= 2 {
			currentLabel = match[1]
		}
		if match := nameRe.FindStringSubmatch(line); len(match) >= 2 {
			currentName = match[1]
		}
		if match := timeRe.FindStringSubmatch(line); len(match) >= 2 {
			_, _ = fmt.Sscanf(match[1], "%d", &currentTime)
		}

		// When we hit closing </Object>, output the chat message
		if strings.Contains(line, "</Object>") && currentIconType == "chat" && currentLabel != "" {
			timestamp := formatMilliseconds(currentTime)
			if currentName != "" {
				fmt.Fprintf(out, "[%s] %s: %s\n", timestamp, currentName, currentLabel)
			} else {
				fmt.Fprintf(out, "[%s] %s\n", timestamp, currentLabel)
			}
			currentIconType = ""
			currentLabel = ""
			currentName = ""
		}
	}

	return scanner.Err()
}

// formatMilliseconds converts milliseconds to HH:MM:SS format.
func formatMilliseconds(ms int64) string {
	secs := ms / 1000
	hours := secs / 3600
	mins := (secs % 3600) / 60
	seconds := secs % 60
	return fmt.Sprintf("%02d:%02d:%02d", hours, mins, seconds)
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

// extractDocumentLinks parses ftfileshare*.xml and ftcontent*.xml for document links.
func extractDocumentLinks(rawDir, hostname string) []DocumentInfo {
	var docs []DocumentInfo
	seen := make(map[string]bool)

	// Parse ftfileshare*.xml files
	files, _ := filepath.Glob(filepath.Join(rawDir, "ftfileshare*.xml"))
	files2, _ := filepath.Glob(filepath.Join(rawDir, "ftcontent*.xml"))
	files = append(files, files2...)

	// Match newValue blocks containing downloadUrl or playbackFileName
	newValueRe := regexp.MustCompile(`(?s)<newValue>(.*?)</newValue>`)
	// Prefer playbackFileName over downloadUrl as it's the persistent playback URL
	playbackRe := regexp.MustCompile(`<playbackFileName><!\[CDATA\[([^\]]+)\]\]></playbackFileName>`)
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

			// Extract playbackFileName (preferred) or downloadUrl, name, and size from this block
			playbackMatch := playbackRe.FindStringSubmatch(block)
			dlMatch := downloadRe.FindStringSubmatch(block)
			nameMatch := nameRe.FindStringSubmatch(block)
			sizeMatch := sizeRe.FindStringSubmatch(block)

			// Need at least a URL (prefer playbackFileName) and a name
			var downloadURL string
			if len(playbackMatch) >= 2 {
				downloadURL = playbackMatch[1]
			} else if len(dlMatch) >= 2 {
				downloadURL = dlMatch[1]
			}

			if downloadURL == "" || len(nameMatch) < 2 {
				continue
			}

			name := nameMatch[1]
			var size int64
			if len(sizeMatch) >= 2 {
				_, _ = fmt.Sscanf(sizeMatch[1], "%d", &size)
			}

			// Only include if it looks like a document download
			if !strings.Contains(downloadURL, "/system/download") {
				continue
			}

			// Convert /system/download URL to direct download URL
			directURL := convertToDirectDownloadURL(downloadURL, name)
			fullURL := fmt.Sprintf("https://%s%s", hostname, directURL)

			if !seen[fullURL] {
				seen[fullURL] = true
				docs = append(docs, DocumentInfo{
					Name:        name,
					DownloadURL: fullURL,
					Size:        size,
				})
			}
		}
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
