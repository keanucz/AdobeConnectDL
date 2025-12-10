package downloader

import (
	"bufio"
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
	"sync"
)

// HTTPClient describes the subset of http.Client used by the downloader.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Logger interface for logging operations.
// Compatible with github.com/charmbracelet/log.Logger.
type Logger interface {
	Debug(msg any, keyvals ...any)
	Info(msg any, keyvals ...any)
	Warn(msg any, keyvals ...any)
	Error(msg any, keyvals ...any)
}

// ProgressCallback is called during file downloads with progress updates.
type ProgressCallback func(downloaded, total int64)

// SubtitleEmbedder is an interface for embedding subtitles into video files.
// This allows the downloader to optionally embed subtitles without importing mp4box directly.
type SubtitleEmbedder interface {
	EmbedSubtitles(ctx context.Context, mp4Path, vttPath, lang string, stdout, stderr io.Writer) error
}

// Options control how a recording is downloaded.
type Options struct {
	OutputDir  string
	Session    string
	Log        Logger           // Structured logger (compatible with charmbracelet/log)
	OnProgress ProgressCallback // Called during MP4 download with progress
	Overwrite  bool             // If true, overwrite existing directories without prompting
	MP4Box     SubtitleEmbedder // Optional: embed subtitles into MP4 using MP4Box
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
	pool   *DownloadPool // Optional shared download pool
}

// New creates a Downloader.
func New(client HTTPClient) *Downloader {
	return &Downloader{client: client}
}

// NewWithPool creates a Downloader that uses a shared download pool.
// The pool should be started before use and stopped when done.
func NewWithPool(client HTTPClient, pool *DownloadPool) *Downloader {
	return &Downloader{client: client, pool: pool}
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

	// Create initial cookies for ZIP download (session-based)
	initialCookies := mergeCookies(session, nil)

	// Use recording ID as initial title
	title := sanitize(info.ID)
	baseOutputDir := opts.OutputDir
	if baseOutputDir == "" {
		baseOutputDir = "."
	}

	// ZIP URL is known immediately from the recording ID
	zipURL := fmt.Sprintf("%s/output/%s.zip?download=zip", info.BaseURL, info.ID)

	// Start page info fetch and ZIP download concurrently
	type pageResult struct {
		info pageInfo
		err  error
	}
	pageCh := make(chan pageResult, 1)
	go func() {
		pi, perr := d.fetchPageInfo(ctx, rawURL, session, logger)
		pageCh <- pageResult{info: pi, err: perr}
	}()

	// Start ZIP download immediately to a temp location
	// We'll move it to the final location once we know the title
	tempZipPath := filepath.Join(baseOutputDir, fmt.Sprintf(".%s_temp.zip", info.ID))
	tempMP4Path := filepath.Join(baseOutputDir, fmt.Sprintf(".%s_temp.mp4", info.ID))
	var zipDownloadErr error
	var zipDownloadDone = make(chan struct{})

	if d.pool != nil {
		// Use shared download pool
		zipResult := d.pool.SubmitZip(ctx, zipURL, tempZipPath, rawURL, initialCookies)
		go func() {
			defer close(zipDownloadDone)
			logInfo(logger, "downloading recording data via pool", "url", zipURL)
			res := <-zipResult
			zipDownloadErr = res.Err
		}()
	} else {
		// Use direct goroutine
		go func() {
			defer close(zipDownloadDone)
			logInfo(logger, "downloading recording data", "url", zipURL)
			if err := d.downloadFile(ctx, zipURL, tempZipPath, downloadOptions{
				Cookies: initialCookies,
				Referer: rawURL,
				Kind:    fileKindZip,
			}, logger); err != nil {
				zipDownloadErr = err
			}
		}()
	}

	// Wait for page info to get the real title
	pageRes := <-pageCh
	pageInfo := pageRes.info
	pageErr := pageRes.err

	// Update title if we got a better one from the page
	if pageErr == nil && pageInfo.Title != "" {
		title = sanitize(pageInfo.Title)
	}
	if pageErr != nil {
		log(logger, "fetch page info error", "error", pageErr)
		// Check if this is an authentication error
		if errors.Is(pageErr, ErrAuthRequired) {
			// Clean up temp zip if it exists
			os.Remove(tempZipPath)
			<-zipDownloadDone
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

	rootDir := filepath.Join(baseOutputDir, title)

	// Check if directory exists and has files
	if !opts.Overwrite {
		if entries, err := os.ReadDir(rootDir); err == nil && len(entries) > 0 {
			// Clean up temp zip
			os.Remove(tempZipPath)
			<-zipDownloadDone
			return Result{Title: title, RootDir: rootDir}, fmt.Errorf("%w: %s", ErrDirectoryExists, rootDir)
		}
	}

	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		os.Remove(tempZipPath)
		<-zipDownloadDone
		return Result{}, fmt.Errorf("create output dir: %w", err)
	}

	result := Result{
		Title:   title,
		RootDir: rootDir,
	}

	// Prepare final paths
	cookies := mergeCookies(session, pageInfo.Cookies)
	log(logger, "prepared cookies for download", "count", len(cookies))
	referer := rawURL
	mp4Path := filepath.Join(rootDir, "recording.mp4")
	zipPath := filepath.Join(rootDir, "raw.zip")

	// Start MP4 download (now that we have the video URL from page info) to a temp path
	var mp4ResultCh <-chan DownloadResult
	if pageInfo.VideoSrc != "" {
		startMP4 := func(dest string) <-chan DownloadResult {
			if d.pool != nil {
				logInfo(logger, "downloading video via pool", "url", pageInfo.VideoSrc)
				return d.pool.SubmitMP4(ctx, pageInfo.VideoSrc, dest, referer, cookies, opts.OnProgress)
			}

			resultCh := make(chan DownloadResult, 1)
			go func() {
				logInfo(logger, "downloading video", "url", pageInfo.VideoSrc)
				if err := d.downloadFile(ctx, pageInfo.VideoSrc, dest, downloadOptions{
					Cookies:    cookies,
					Referer:    referer,
					Kind:       fileKindVideo,
					OnProgress: opts.OnProgress,
				}, logger); err != nil {
					log(logger, "video src download failed", "error", err)
					resultCh <- DownloadResult{Err: err}
				} else {
					resultCh <- DownloadResult{Path: dest}
				}
				close(resultCh)
			}()
			return resultCh
		}

		mp4ResultCh = startMP4(tempMP4Path)
	}

	// Wait for ZIP download to complete
	<-zipDownloadDone

	// Handle ZIP result - move from temp location to final location
	var zipErr error
	if zipDownloadErr != nil {
		if errors.Is(zipDownloadErr, ErrNotFound) {
			result.Warnings = append(result.Warnings, "Raw recording ZIP not available")
			log(logger, "zip not available", "url", zipURL)
		} else if errors.Is(zipDownloadErr, ErrInvalidZip) {
			result.Warnings = append(result.Warnings, "ZIP response was invalid")
			log(logger, "zip invalid", "url", zipURL)
		} else {
			log(logger, "zip download failed", "error", zipDownloadErr)
		}
		zipErr = zipDownloadErr
		os.Remove(tempZipPath)
	} else {
		// Move temp zip to final location
		if err := os.Rename(tempZipPath, zipPath); err != nil {
			// If rename fails (cross-device), try copy
			if copyErr := copyFile(zipPath, tempZipPath); copyErr != nil {
				log(logger, "zip move/copy failed", "error", copyErr)
				zipErr = copyErr
			} else {
				os.Remove(tempZipPath)
				result.ZipPath = zipPath
				log(logger, "zip downloaded", "path", zipPath)
			}
		} else {
			result.ZipPath = zipPath
			log(logger, "zip downloaded", "path", zipPath)
		}
	}

	// Start ZIP extraction immediately (in parallel with MP4 download)
	// This is key for performance - don't wait for MP4 to finish before extracting
	var extractDir string
	var extractDone = make(chan struct{})
	var extractErr error
	var lecturerName string
	var userMapping map[string]string
	var vttPath string
	var docs []DocumentInfo

	if zipErr == nil && result.ZipPath != "" {
		extractDir = filepath.Join(rootDir, "raw")
		go func() {
			defer close(extractDone)
			logInfo(logger, "extracting zip", "path", zipPath)
			if err := extractZip(zipPath, extractDir); err != nil {
				logError(logger, "zip extraction failed", "path", zipPath, "error", err)
				extractErr = err
				return
			}
			result.ExtractedDir = extractDir
			logInfo(logger, "zip extracted", "path", extractDir)

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

			// Extract lecturer name and user mapping (needed for VTT cleaning)
			lecturerName = extractLecturerName(extractDir)
			if lecturerName != "" {
				log(logger, "lecturer name found", "name", lecturerName)
			}
			userMapping = extractUserMapping(extractDir)
			if len(userMapping) > 0 {
				log(logger, "user mapping extracted", "count", len(userMapping))
			}

			// Extract document links
			docs = extractDocumentLinks(extractDir, info.Hostname)

			// Start document downloads immediately (don't wait for MP4)
			if len(docs) > 0 {
				docsPath := filepath.Join(rootDir, "documents.txt")
				if err := writeDocumentList(docsPath, docs); err != nil {
					log(logger, "document list write failed", "error", err)
				} else {
					log(logger, "document list created", "path", docsPath, "count", len(docs))
				}
				docsDir := filepath.Join(rootDir, "documents")
				logInfo(logger, "downloading documents via pool", "count", len(docs))
				downloaded := d.downloadDocuments(ctx, docs, docsDir, cookies, referer, logger)
				log(logger, "documents downloaded", "downloaded", downloaded, "total", len(docs))
			}

			// Generate chat log
			chatLogPath := filepath.Join(rootDir, "chat_log.txt")
			if err := extractChatLog(extractDir, chatLogPath); err != nil {
				log(logger, "chat log extraction failed", "error", err)
			} else {
				log(logger, "chat log created", "path", chatLogPath)
			}
		}()
	} else {
		close(extractDone)
	}

	// Wait for MP4 download to complete and move it into place
	var mp4DownloadErr error
	var mp4DownloadedPath string
	if mp4ResultCh != nil {
		mp4Res := <-mp4ResultCh
		mp4DownloadErr = mp4Res.Err
		mp4DownloadedPath = mp4Res.Path
		if mp4DownloadErr != nil {
			log(logger, "video src download failed", "error", mp4DownloadErr)
			os.Remove(tempMP4Path)
		}
	}

	if mp4DownloadErr == nil && mp4DownloadedPath != "" {
		if err := os.Rename(mp4DownloadedPath, mp4Path); err != nil {
			if copyErr := copyFile(mp4Path, mp4DownloadedPath); copyErr != nil {
				log(logger, "mp4 move/copy failed", "error", copyErr)
			} else {
				os.Remove(mp4DownloadedPath)
				result.MP4Path = mp4Path
				log(logger, "mp4 downloaded via video src", "path", mp4Path)
			}
		} else {
			result.MP4Path = mp4Path
			log(logger, "mp4 downloaded via video src", "path", mp4Path)
		}
	} else if mp4DownloadedPath != "" {
		os.Remove(mp4DownloadedPath)
	} else if mp4DownloadErr != nil && tempMP4Path != "" {
		os.Remove(tempMP4Path)
	}

	// Check MP4 result
	if result.MP4Path == "" {
		result.Warnings = append(result.Warnings, "MP4 rendition not available")
	}

	// Wait for extraction and document downloads to complete
	<-extractDone

	// Process VTT after both extraction and MP4 are done (VTT embedding needs MP4)
	if extractErr == nil && vttPath != "" {
		d.processVTT(ctx, vttPath, result.MP4Path, rootDir, lecturerName, userMapping, opts.MP4Box, logger)
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
			// Process the downloaded VTT
			d.processVTT(ctx, vttPath, result.MP4Path, rootDir, lecturerName, userMapping, opts.MP4Box, logger)
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

// processVTT handles VTT cleaning, transcript creation, and subtitle embedding.
func (d *Downloader) processVTT(
	ctx context.Context,
	vttPath, mp4Path, rootDir string,
	lecturerName string,
	userMapping map[string]string,
	embedder SubtitleEmbedder,
	logger Logger,
) {
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

	// Create a readable transcript from the VTT
	transcriptPath := filepath.Join(rootDir, "transcript.txt")
	if err := vttToTranscript(vttPath, transcriptPath); err != nil {
		log(logger, "transcript creation failed", "error", err)
	} else {
		log(logger, "transcript created", "path", transcriptPath)
	}

	// Embed subtitles into MP4 if embedder is available and MP4 exists
	if embedder != nil && mp4Path != "" {
		logInfo(logger, "embedding subtitles", "path", vttPath)
		if err := embedder.EmbedSubtitles(ctx, mp4Path, vttPath, "en", nil, nil); err != nil {
			logWarn(logger, "failed to embed subtitles", "error", err)
		} else {
			logInfo(logger, "subtitles embedded successfully")
		}
	}
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

	if resp.StatusCode == http.StatusInternalServerError || resp.StatusCode == http.StatusUnauthorized ||
		resp.StatusCode == http.StatusForbidden {
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
func (d *Downloader) downloadFile(
	ctx context.Context,
	fileURL string,
	dest string,
	opts downloadOptions,
	logger Logger,
) error {
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

	log(
		logger,
		"download response",
		"url",
		fileURL,
		"status",
		resp.StatusCode,
		"content-type",
		resp.Header.Get("Content-Type"),
	)

	// Handle error status codes
	if resp.StatusCode == http.StatusForbidden {
		return errors.New("returned 403 (token may be expired or already used)")
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
			return errors.New("returned HTML instead of video")
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

	// Use buffered writer for better I/O performance (64KB buffer)
	// This reduces the number of syscalls when writing to disk
	bufferedFile := bufio.NewWriterSize(file, 64*1024)
	defer bufferedFile.Flush()

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
	if _, err := bufferedFile.Write(head[:n]); err != nil {
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

	written, err := io.Copy(bufferedFile, reader)
	if err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	// Ensure all buffered data is written
	if err := bufferedFile.Flush(); err != nil {
		return fmt.Errorf("flush buffer: %w", err)
	}

	totalWritten := int64(n) + written
	logInfo(logger, "downloaded", "path", filepath.Base(dest), "bytes", totalWritten)

	// Video files must have minimum size
	if opts.Kind == fileKindVideo && totalWritten < 1024 {
		return fmt.Errorf("file too small (%d bytes), likely an error page", totalWritten)
	}

	return nil
}

// DefaultConcurrency is the optimal number of concurrent downloads (benchmark-proven).
const DefaultConcurrency = 12

// minInt returns the smaller of two ints.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// downloadJob represents a single document download task.
type downloadJob struct {
	Index     int
	Doc       DocumentInfo
	DestDir   string
	Cookies   []*http.Cookie
	Referer   string
	TotalDocs int
}

// downloadDocuments downloads all documents to the specified directory using a worker pool.
// This is more efficient than spawning a goroutine per document as it:
// 1. Reuses goroutines instead of creating new ones for each document
// 2. Reduces goroutine scheduling overhead
// 3. Provides natural backpressure through the jobs channel
func (d *Downloader) downloadDocuments(
	ctx context.Context,
	docs []DocumentInfo,
	destDir string,
	cookies []*http.Cookie,
	referer string,
	logger Logger,
) int {
	if len(docs) == 0 {
		return 0
	}

	// Use shared pool if available
	if d.pool != nil {
		logInfo(logger, "downloading documents via pool", "count", len(docs))
		return d.pool.WaitForDocuments(ctx, docs, destDir, cookies, referer)
	}

	// Fall back to local worker pool
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		log(logger, "create documents dir failed", "error", err)
		return 0
	}

	// Use min(DefaultConcurrency, len(docs)) workers to avoid idle workers
	numWorkers := minInt(DefaultConcurrency, len(docs))

	// Create buffered jobs channel - buffer size equals docs to allow non-blocking sends
	jobs := make(chan downloadJob, len(docs))

	// Results channel for counting successful downloads
	results := make(chan bool, len(docs))

	// Start worker pool
	var wg sync.WaitGroup
	for range make([]struct{}, numWorkers) {
		wg.Add(1)
		wg.Go(func() {
			for job := range jobs {
				// Check context cancellation
				if ctx.Err() != nil {
					results <- false
					continue
				}

				destPath := filepath.Join(job.DestDir, sanitize(job.Doc.Name))
				numStr := fmt.Sprintf("%d/%d", job.Index+1, job.TotalDocs)
				logInfo(logger, "downloading document", "num", numStr, "name", job.Doc.Name)

				if err := d.downloadFile(ctx, job.Doc.DownloadURL, destPath, downloadOptions{
					Cookies: job.Cookies,
					Referer: job.Referer,
					Kind:    fileKindBinary,
				}, logger); err != nil {
					log(logger, "document download failed", "name", job.Doc.Name, "error", err)
					results <- false
					continue
				}
				results <- true
			}
		})
	}

	// Send all jobs to workers (non-blocking due to buffered channel)
	for i, doc := range docs {
		jobs <- downloadJob{
			Index:     i,
			Doc:       doc,
			DestDir:   destDir,
			Cookies:   cookies,
			Referer:   referer,
			TotalDocs: len(docs),
		}
	}
	close(jobs)

	// Wait for all workers to finish
	wg.Wait()
	close(results)

	// Count successful downloads
	var downloaded int
	for success := range results {
		if success {
			downloaded++
		}
	}

	return downloaded
}
