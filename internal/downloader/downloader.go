package downloader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// HTTPClient describes the subset of http.Client used by the downloader.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// FFmpegRunner describes the subset of ffmpegexec.Runner used for subtitle embedding.
type FFmpegRunner interface {
	Run(ctx context.Context, args []string, stdout, stderr io.Writer) error
	Path() string
}

// Logger interface for logging operations.
// Compatible with github.com/charmbracelet/log.Logger
type Logger interface {
	Debug(msg interface{}, keyvals ...interface{})
	Info(msg interface{}, keyvals ...interface{})
	Warn(msg interface{}, keyvals ...interface{})
	Error(msg interface{}, keyvals ...interface{})
}

// ProgressCallback is called during file downloads with progress updates
type ProgressCallback func(downloaded, total int64)

// Options control how a recording is downloaded.
type Options struct {
	OutputDir  string
	Session    string
	Log        Logger // Structured logger (compatible with charmbracelet/log)
	FFmpeg     FFmpegRunner
	OnProgress ProgressCallback // Called during MP4 download with progress
	Overwrite  bool             // If true, overwrite existing directories without prompting
}

// Result captures what was downloaded.
type Result struct {
	Title        string
	RootDir      string
	MP4Path      string
	ZipPath      string
	ExtractedDir string
	Warnings     []string
}

// ErrNotFound indicates the resource was not found.
var ErrNotFound = errors.New("resource not found")

// ErrInvalidZip indicates the downloaded file is not a valid ZIP.
var ErrInvalidZip = errors.New("invalid zip content")

// ErrAuthRequired indicates that authentication is required to access this recording.
// This typically means a session token is missing or invalid.
var ErrAuthRequired = errors.New("authentication required: this recording requires a valid session token")

// ErrDirectoryExists indicates the output directory already exists and contains files.
var ErrDirectoryExists = errors.New("output directory already exists")

// Downloader coordinates downloading recordings.
type Downloader struct {
	client HTTPClient
}

// New creates a Downloader.
func New(client HTTPClient) *Downloader {
	return &Downloader{client: client}
}

// Download grabs the MP4 and VTT assets for the provided recording URL.
func (d *Downloader) Download(ctx context.Context, rawURL string, opts Options) (Result, error) {
	logger := opts.Log

	info, err := parseRecordingURL(rawURL)
	if err != nil {
		return Result{}, err
	}
	log(logger, "parsed recording URL", "id", info.ID, "host", info.Hostname, "base", info.BaseURL)

	session := opts.Session
	if session == "" {
		if u, parseErr := url.Parse(rawURL); parseErr == nil {
			if qs := u.Query().Get("session"); qs != "" {
				session = qs
				log(logger, "session token taken from query param")
			}
		}
	}
	if session != "" {
		log(logger, "using session token", "length", len(session))
	}

	pageInfo, pageErr := d.fetchPageInfo(ctx, rawURL, session, logger)

	title := info.ID
	if pageErr == nil && pageInfo.Title != "" {
		title = sanitize(pageInfo.Title)
	} else {
		title = sanitize(title)
	}
	if pageErr != nil {
		log(logger, "fetch page info error", "error", pageErr)
		// Check if this is an authentication error
		if errors.Is(pageErr, ErrAuthRequired) {
			return Result{}, fmt.Errorf("%w\n\n"+
				"To access private recordings, you need to include a session token in the URL.\n"+
				"Example: https://your-domain.adobeconnect.com/recording-id/?session=YOUR_SESSION_TOKEN\n\n"+
				"To get a session token:\n"+
				"1. Log into Adobe Connect in your browser\n"+
				"2. Open the recording page\n"+
				"3. Copy the URL from your browser's address bar (it should contain ?session=...)\n"+
				"4. Use that complete URL with this tool", pageErr)
		}
	}
	log(logger, "resolved title", "title", title)

	rootDir := opts.OutputDir
	if rootDir == "" {
		rootDir = "."
	}
	rootDir = filepath.Join(rootDir, title)

	// Check if directory exists and has files
	if !opts.Overwrite {
		if entries, err := os.ReadDir(rootDir); err == nil && len(entries) > 0 {
			return Result{Title: title, RootDir: rootDir}, fmt.Errorf("%w: %s", ErrDirectoryExists, rootDir)
		}
	}

	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("create output dir: %w", err)
	}

	result := Result{
		Title:   title,
		RootDir: rootDir,
	}

	// Start downloading MP4 process

	cookies := mergeCookies(session, pageInfo.Cookies)
	log(logger, "prepared cookies for download", "count", len(cookies))
	referer := rawURL
	mp4Path := filepath.Join(rootDir, "recording.mp4")

	// Priority 1: Use video src from HTML (follows redirect to real MP4)
	if pageInfo.VideoSrc != "" {
		logInfo(logger, "downloading video", "url", pageInfo.VideoSrc)
		if err := d.downloadFile(ctx, pageInfo.VideoSrc, mp4Path, downloadOptions{
			Cookies:    cookies,
			Referer:    referer,
			Kind:       fileKindVideo,
			OnProgress: opts.OnProgress,
		}, logger); err != nil {
			log(logger, "video src download failed", "error", err)
		} else {
			result.MP4Path = mp4Path
			log(logger, "mp4 downloaded via video src", "path", mp4Path)
		}
	}

	if result.MP4Path == "" {
		result.Warnings = append(result.Warnings, "MP4 rendition not available")
	}

	// Download and extract ZIP for VTT, chat logs, and documents
	var lecturerName string
	var userMapping map[string]string
	var vttPath string

	zipURL := fmt.Sprintf("%s/output/%s.zip?download=zip", info.BaseURL, info.ID)
	zipPath := filepath.Join(rootDir, "raw.zip")
	logInfo(logger, "downloading recording data", "url", zipURL)
	if err := d.downloadFile(ctx, zipURL, zipPath, downloadOptions{
		Cookies: cookies,
		Referer: referer,
		Kind:    fileKindZip,
	}, logger); err != nil {
		if errors.Is(err, ErrNotFound) {
			result.Warnings = append(result.Warnings, "Raw recording ZIP not available")
			log(logger, "zip not available", "url", zipURL)
		} else if errors.Is(err, ErrInvalidZip) {
			result.Warnings = append(result.Warnings, "ZIP response was invalid")
			log(logger, "zip invalid", "url", zipURL)
		} else {
			log(logger, "zip download failed", "error", err)
		}
	} else {
		result.ZipPath = zipPath
		log(logger, "zip downloaded", "path", zipPath)

		// Extract ZIP
		extractDir := filepath.Join(rootDir, "raw")
		if err := extractZip(zipPath, extractDir); err != nil {
			log(logger, "zip extraction failed", "error", err)
		} else {
			result.ExtractedDir = extractDir
			log(logger, "zip extracted", "path", extractDir)

			// Find VTT file in ZIP (named *.vtt)
			vttFiles, _ := filepath.Glob(filepath.Join(extractDir, "*.vtt"))
			if len(vttFiles) > 0 {
				// Copy VTT to output directory
				vttPath = filepath.Join(rootDir, "captions.vtt")
				if data, err := os.ReadFile(vttFiles[0]); err == nil {
					if err := os.WriteFile(vttPath, data, 0o644); err == nil {
						log(logger, "vtt extracted from zip", "source", vttFiles[0])
					}
				}
			}

			// Extract lecturer name from transcriptstream.xml
			lecturerName = extractLecturerName(extractDir)
			if lecturerName != "" {
				log(logger, "lecturer name found", "name", lecturerName)
			}

			// Extract user mapping from indexstream.xml for deanonymization
			userMapping = extractUserMapping(extractDir)
			if len(userMapping) > 0 {
				log(logger, "user mapping extracted", "count", len(userMapping))
			}

			// Generate chat log from transcriptstream.xml
			chatLogPath := filepath.Join(rootDir, "chat_log.txt")
			if err := extractChatLog(extractDir, chatLogPath); err != nil {
				log(logger, "chat log extraction failed", "error", err)
			} else {
				log(logger, "chat log created", "path", chatLogPath)
			}

			// Extract document attachments list (with deduplication)
			docsPath := filepath.Join(rootDir, "documents.txt")
			docs := extractDocumentLinks(extractDir, info.Hostname)
			if len(docs) > 0 {
				if err := writeDocumentList(docsPath, docs); err != nil {
					log(logger, "document list write failed", "error", err)
				} else {
					log(logger, "document list created", "path", docsPath, "count", len(docs))
				}

				// Download the documents
				docsDir := filepath.Join(rootDir, "documents")
				downloaded := d.downloadDocuments(ctx, docs, docsDir, cookies, referer, logger)
				log(logger, "documents downloaded", "downloaded", downloaded, "total", len(docs))
			}
		}
	}

	// Fallback: Download VTT separately if not found in ZIP
	if vttPath == "" && result.MP4Path != "" && pageInfo.VTTPath != "" {
		vttURL := resolveVTTURL(info.BaseURL, pageInfo.VTTPath)
		vttPath = filepath.Join(rootDir, "captions.vtt")
		logInfo(logger, "downloading captions", "url", vttURL)
		if err := d.downloadFile(ctx, vttURL, vttPath, downloadOptions{
			Cookies: cookies,
			Referer: referer,
			Kind:    fileKindBinary,
		}, logger); err != nil {
			log(logger, "vtt download failed", "error", err)
			vttPath = "" // Mark as not available
		} else {
			log(logger, "vtt downloaded", "path", vttPath)
		}
	}

	// Process VTT if available
	if vttPath != "" {
		// Clean the VTT file (fix speaker markers, use real names from user mapping)
		cleanedVTTPath := filepath.Join(rootDir, "captions_cleaned.vtt")
		if err := cleanVTTFile(vttPath, cleanedVTTPath, lecturerName, userMapping); err != nil {
			log(logger, "vtt cleaning failed", "error", err)
		} else {
			// Replace original with cleaned version
			if err := os.Rename(cleanedVTTPath, vttPath); err != nil {
				log(logger, "rename cleaned vtt failed", "error", err)
			} else {
				log(logger, "vtt cleaned: speaker markers replaced with real names")
			}
		}

		// Embed VTT as mov_text subtitles if ffmpeg is available and we have an MP4
		if opts.FFmpeg != nil && result.MP4Path != "" {
			embeddedPath := filepath.Join(rootDir, "recording_with_subs.mp4")
			if err := embedSubtitles(ctx, opts.FFmpeg, result.MP4Path, vttPath, embeddedPath, logger); err != nil {
				log(logger, "subtitle embedding failed", "error", err)
				result.Warnings = append(result.Warnings, fmt.Sprintf("subtitle embedding failed: %v", err))
			} else {
				// Replace original MP4 with the one containing subtitles
				if err := os.Rename(embeddedPath, result.MP4Path); err != nil {
					log(logger, "rename embedded mp4 failed", "error", err)
				} else {
					log(logger, "subtitles embedded", "path", result.MP4Path)
				}
			}
		}

		// Create a readable transcript from the VTT
		transcriptPath := filepath.Join(rootDir, "transcript.txt")
		if err := vttToTranscript(vttPath, transcriptPath); err != nil {
			log(logger, "transcript creation failed", "error", err)
		} else {
			log(logger, "transcript created", "path", transcriptPath)
		}
	}

	if result.MP4Path == "" && result.ZipPath == "" {
		return result, errors.New("no assets could be downloaded (MP4 and ZIP unavailable)")
	}

	if err := writeMetadata(rootDir, info, result); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("write metadata: %v", err))
		log(logger, "metadata write warning", "error", err)
	}

	return result, nil
}

// fetchPageInfo fetches the recording page and extracts video URLs and VTT paths.
func (d *Downloader) fetchPageInfo(ctx context.Context, pageURL, session string, logger Logger) (pageInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return pageInfo{}, err
	}
	if session != "" {
		req.AddCookie(&http.Cookie{Name: "BREEZESESSION", Value: session})
	}

	log(logger, "fetching page", "url", pageURL)
	resp, err := d.client.Do(req)
	if err != nil {
		return pageInfo{}, err
	}
	defer resp.Body.Close()
	log(logger, "page response", "status", resp.StatusCode, "content-type", resp.Header.Get("Content-Type"))

	if resp.StatusCode == 500 || resp.StatusCode == 401 || resp.StatusCode == 403 {
		// These status codes typically indicate authentication issues
		return pageInfo{}, ErrAuthRequired
	}
	if resp.StatusCode >= 400 {
		return pageInfo{}, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20)) // limit to avoid large downloads
	if err != nil {
		return pageInfo{}, err
	}

	title, _ := parseHTMLTitle(bytes.NewReader(body))
	casURL := findCASRecordingURL(body)
	videoSrc, vttPath := parseVideoElement(body)
	jsVTT := findVTTFromJS(body)

	// Prefer casRecordingURL over video src tag
	if casURL != "" {
		videoSrc = casURL
	}
	// Prefer JS VTT if HTML track not found
	if vttPath == "" && jsVTT != "" {
		vttPath = jsVTT
	}

	if videoSrc != "" {
		log(logger, "video src discovered", "url", videoSrc)
	}
	if vttPath != "" {
		log(logger, "vtt track discovered", "path", vttPath)
	}
	if videoSrc == "" {
		log(logger, "no video url discovered in page")
	}

	return pageInfo{
		Title:    title,
		VideoSrc: videoSrc,
		VTTPath:  vttPath,
		Cookies:  resp.Cookies(),
	}, nil
}

// fileKind indicates the type of file being downloaded for validation purposes.
type fileKind int

const (
	fileKindBinary fileKind = iota
	fileKindZip
	fileKindVideo
)

// downloadOptions configures file download behavior.
type downloadOptions struct {
	Cookies    []*http.Cookie
	Referer    string
	Kind       fileKind
	OnProgress ProgressCallback
}

// downloadFile downloads a file from the given URL to the destination path.
// It handles video, binary, and ZIP files with appropriate validation.
func (d *Downloader) downloadFile(ctx context.Context, fileURL string, dest string, opts downloadOptions, logger Logger) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return err
	}

	reqOpts := requestOptions{
		Cookies: opts.Cookies,
		Referer: opts.Referer,
	}
	if opts.Kind == fileKindVideo || opts.Kind == fileKindBinary {
		reqOpts.AcceptType = "video"
	}
	applyRequestOptions(req, reqOpts)

	log(logger, "downloading file", "url", fileURL, "kind", opts.Kind)

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	log(logger, "download response", "url", fileURL, "status", resp.StatusCode, "content-type", resp.Header.Get("Content-Type"))

	// Handle error status codes
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("returned 403 (token may be expired or already used)")
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	// For video files, check Content-Type header early
	if opts.Kind == fileKindVideo {
		contentType := resp.Header.Get("Content-Type")
		if strings.Contains(contentType, "text/html") {
			return fmt.Errorf("returned HTML instead of video")
		}
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	file, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer file.Close()

	// Read initial bytes for validation
	var head [4096]byte
	n, readErr := io.ReadFull(resp.Body, head[:])
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
		return readErr
	}

	// Validate content based on file kind
	if isHTMLResponse(resp.Header, head[:n]) {
		log(logger, "html response detected", "url", fileURL)
		return ErrNotFound
	}

	if opts.Kind == fileKindZip && n >= 4 && !isZipSignature(head[:n]) {
		log(logger, "invalid zip signature", "url", fileURL)
		return ErrInvalidZip
	}

	// Write the head bytes we already read
	if _, err := file.Write(head[:n]); err != nil {
		return err
	}

	// Copy the rest, with optional progress reporting
	var reader io.Reader = resp.Body
	if opts.OnProgress != nil && resp.ContentLength > 0 {
		reader = &progressReader{
			reader:     resp.Body,
			total:      resp.ContentLength,
			downloaded: int64(n), // Already read head bytes
			onProgress: opts.OnProgress,
		}
		// Report initial progress
		opts.OnProgress(int64(n), resp.ContentLength)
	}

	written, err := io.Copy(file, reader)
	if err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	totalWritten := int64(n) + written
	logInfo(logger, "downloaded", "path", filepath.Base(dest), "bytes", totalWritten)

	// Video files must have minimum size
	if opts.Kind == fileKindVideo && totalWritten < 1024 {
		return fmt.Errorf("file too small (%d bytes), likely an error page", totalWritten)
	}

	return nil
}

// downloadDocuments downloads all documents to the specified directory.
func (d *Downloader) downloadDocuments(ctx context.Context, docs []DocumentInfo, destDir string, cookies []*http.Cookie, referer string, logger Logger) int {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		log(logger, "create documents dir failed", "error", err)
		return 0
	}

	downloaded := 0
	for i, doc := range docs {
		destPath := filepath.Join(destDir, sanitize(doc.Name))
		logInfo(logger, "downloading document", "num", fmt.Sprintf("%d/%d", i+1, len(docs)), "name", doc.Name)

		if err := d.downloadFile(ctx, doc.DownloadURL, destPath, downloadOptions{
			Cookies: cookies,
			Referer: referer,
			Kind:    fileKindBinary,
		}, logger); err != nil {
			log(logger, "document download failed", "name", doc.Name, "error", err)
			continue
		}
		downloaded++
	}

	return downloaded
}
