package downloader

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// JobType indicates the type of download job for prioritization and logging.
type JobType int

const (
	JobTypeDocument JobType = iota
	JobTypeZip
	JobTypeMP4
	JobTypeVTT
	JobTypeExtract // ZIP extraction job
)

func (jt JobType) String() string {
	switch jt {
	case JobTypeDocument:
		return "document"
	case JobTypeZip:
		return "zip"
	case JobTypeMP4:
		return "mp4"
	case JobTypeVTT:
		return "vtt"
	case JobTypeExtract:
		return "extract"
	default:
		return "unknown"
	}
}

// DownloadJob represents a single download task in the global pool.
type DownloadJob struct {
	// Job identification
	Type        JobType
	Name        string // Human-readable name for logging
	RecordingID string // Which recording this belongs to

	// Download parameters (for download jobs)
	URL      string
	DestPath string
	Cookies  []*http.Cookie
	Referer  string
	Kind     fileKind

	// Extraction parameters (for extract jobs)
	SourcePath string // ZIP file to extract
	ExtractDir string // Directory to extract to

	// Callbacks
	OnProgress ProgressCallback
	OnComplete func(err error) // Called when download completes

	// Context for cancellation
	Ctx context.Context
}

// DownloadPool manages a shared pool of workers for concurrent downloads.
// It provides a global queue that all recordings can submit jobs to,
// ensuring optimal bandwidth utilization across multiple recordings.
type DownloadPool struct {
	client     HTTPClient
	numWorkers int
	jobs       chan DownloadJob
	wg         sync.WaitGroup
	started    atomic.Bool
	stopped    atomic.Bool
	logger     Logger

	// Stats
	completed atomic.Int64
	failed    atomic.Int64
}

// PoolConfig configures the download pool.
type PoolConfig struct {
	NumWorkers int    // Number of concurrent download workers (default: 12)
	QueueSize  int    // Size of the job queue buffer (default: 1000)
	Logger     Logger // Optional logger
}

// DefaultPoolConfig returns sensible defaults for the pool.
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		NumWorkers: 12,
		QueueSize:  1000,
	}
}

// NewDownloadPool creates a new download pool with the given HTTP client and config.
func NewDownloadPool(client HTTPClient, config PoolConfig) *DownloadPool {
	if config.NumWorkers <= 0 {
		config.NumWorkers = 12
	}
	if config.QueueSize <= 0 {
		config.QueueSize = 1000
	}

	return &DownloadPool{
		client:     client,
		numWorkers: config.NumWorkers,
		jobs:       make(chan DownloadJob, config.QueueSize),
		logger:     config.Logger,
	}
}

// Start launches the worker goroutines. Must be called before submitting jobs.
func (p *DownloadPool) Start() {
	if p.started.Swap(true) {
		return // Already started
	}

	for i := range make([]struct{}, p.numWorkers) {
		p.wg.Add(1)
		go p.worker(i)
	}

	if p.logger != nil {
		p.logger.Info("download pool started", "workers", p.numWorkers)
	}
}

// Stop gracefully shuts down the pool, waiting for all pending jobs to complete.
func (p *DownloadPool) Stop() {
	if p.stopped.Swap(true) {
		return // Already stopped
	}

	close(p.jobs)
	p.wg.Wait()

	if p.logger != nil {
		p.logger.Info("download pool stopped", "completed", p.completed.Load(), "failed", p.failed.Load())
	}
}

// Submit adds a download job to the queue. Non-blocking if queue has space.
// Returns false if the pool is stopped or queue is full.
func (p *DownloadPool) Submit(job DownloadJob) bool {
	if p.stopped.Load() {
		return false
	}

	select {
	case p.jobs <- job:
		return true
	default:
		// Queue is full - this shouldn't happen with proper sizing
		if p.logger != nil {
			p.logger.Warn("download pool queue full, blocking", "job", job.Name)
		}
		// Block and wait for space
		p.jobs <- job
		return true
	}
}

// SubmitAndWait submits a job and waits for it to complete, returning any error.
func (p *DownloadPool) SubmitAndWait(job DownloadJob) error {
	done := make(chan error, 1)
	originalOnComplete := job.OnComplete

	job.OnComplete = func(err error) {
		if originalOnComplete != nil {
			originalOnComplete(err)
		}
		done <- err
	}

	if !p.Submit(job) {
		return errors.New("failed to submit job: pool stopped")
	}

	return <-done
}

// Stats returns the current pool statistics.
func (p *DownloadPool) Stats() (completed, failed int64) {
	return p.completed.Load(), p.failed.Load()
}

// worker is the main loop for a download worker.
func (p *DownloadPool) worker(_ int) {
	defer p.wg.Done()

	for job := range p.jobs {
		p.processJob(job)
	}
}

// processJob handles a single download job.
func (p *DownloadPool) processJob(job DownloadJob) {
	// Check context cancellation before starting
	if job.Ctx != nil && job.Ctx.Err() != nil {
		if p.logger != nil {
			p.logger.Warn("job cancelled", "type", job.Type.String(), "name", job.Name, "error", job.Ctx.Err())
		}
		if job.OnComplete != nil {
			job.OnComplete(job.Ctx.Err())
		}
		p.failed.Add(1)
		return
	}

	ctx := job.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	var err error

	// Handle extraction jobs differently from download jobs
	if job.Type == JobTypeExtract {
		if p.logger != nil {
			p.logger.Debug("starting extraction", "name", job.Name, "source", job.SourcePath)
		}
		err = extractZip(job.SourcePath, job.ExtractDir)
	} else {
		// Log the start for download jobs
		if p.logger != nil {
			p.logger.Debug("starting download", "type", job.Type.String(), "name", job.Name, "url", truncateURL(job.URL))
		}

		// Create a temporary downloader instance to use downloadFile
		d := &Downloader{client: p.client}

		err = d.downloadFile(ctx, job.URL, job.DestPath, downloadOptions{
			Cookies:    job.Cookies,
			Referer:    job.Referer,
			Kind:       job.Kind,
			OnProgress: job.OnProgress,
		}, p.logger)
	}

	if err != nil {
		p.failed.Add(1)
		if p.logger != nil {
			if job.Type == JobTypeExtract {
				p.logger.Warn("extraction failed", "name", job.Name, "source", job.SourcePath, "error", err)
			} else {
				p.logger.Warn("download failed", "type", job.Type.String(), "name", job.Name, "url", truncateURL(job.URL), "error", err)
			}
		}
	} else {
		p.completed.Add(1)
		if p.logger != nil {
			p.logger.Debug("download complete", "type", job.Type.String(), "name", job.Name)
		}
	}

	if job.OnComplete != nil {
		job.OnComplete(err)
	}
}

// truncateURL shortens a URL for logging purposes.
func truncateURL(u string) string {
	if len(u) <= 80 {
		return u
	}
	return u[:77] + "..."
}

// DownloadResult holds the result of a download operation.
type DownloadResult struct {
	Path string
	Err  error
}

// SubmitMP4 is a convenience method for submitting an MP4 download job.
func (p *DownloadPool) SubmitMP4(
	ctx context.Context,
	url string,
	destPath string,
	referer string,
	cookies []*http.Cookie,
	onProgress ProgressCallback,
) <-chan DownloadResult {
	result := make(chan DownloadResult, 1)

	job := DownloadJob{
		Type:       JobTypeMP4,
		Name:       filepath.Base(destPath),
		URL:        url,
		DestPath:   destPath,
		Cookies:    cookies,
		Referer:    referer,
		Kind:       fileKindVideo,
		OnProgress: onProgress,
		Ctx:        ctx,
		OnComplete: func(err error) {
			if err != nil {
				result <- DownloadResult{Err: err}
			} else {
				result <- DownloadResult{Path: destPath}
			}
			close(result)
		},
	}

	p.Submit(job)
	return result
}

// SubmitZip is a convenience method for submitting a ZIP download job.
func (p *DownloadPool) SubmitZip(
	ctx context.Context,
	url string,
	destPath string,
	referer string,
	cookies []*http.Cookie,
) <-chan DownloadResult {
	result := make(chan DownloadResult, 1)

	job := DownloadJob{
		Type:     JobTypeZip,
		Name:     filepath.Base(destPath),
		URL:      url,
		DestPath: destPath,
		Cookies:  cookies,
		Referer:  referer,
		Kind:     fileKindZip,
		Ctx:      ctx,
		OnComplete: func(err error) {
			if err != nil {
				result <- DownloadResult{Err: err}
			} else {
				result <- DownloadResult{Path: destPath}
			}
			close(result)
		},
	}

	p.Submit(job)
	return result
}

// SubmitVTT is a convenience method for submitting a VTT download job.
func (p *DownloadPool) SubmitVTT(
	ctx context.Context,
	url string,
	destPath string,
	referer string,
	cookies []*http.Cookie,
) <-chan DownloadResult {
	result := make(chan DownloadResult, 1)

	job := DownloadJob{
		Type:     JobTypeVTT,
		Name:     filepath.Base(destPath),
		URL:      url,
		DestPath: destPath,
		Cookies:  cookies,
		Referer:  referer,
		Kind:     fileKindBinary,
		Ctx:      ctx,
		OnComplete: func(err error) {
			if err != nil {
				result <- DownloadResult{Err: err}
			} else {
				result <- DownloadResult{Path: destPath}
			}
			close(result)
		},
	}

	p.Submit(job)
	return result
}

// SubmitExtract is a convenience method for submitting a ZIP extraction job.
func (p *DownloadPool) SubmitExtract(ctx context.Context, zipPath, extractDir, name string) <-chan DownloadResult {
	result := make(chan DownloadResult, 1)

	job := DownloadJob{
		Type:       JobTypeExtract,
		Name:       name,
		SourcePath: zipPath,
		ExtractDir: extractDir,
		Ctx:        ctx,
		OnComplete: func(err error) {
			if err != nil {
				result <- DownloadResult{Err: err}
			} else {
				result <- DownloadResult{Path: extractDir}
			}
			close(result)
		},
	}

	p.Submit(job)
	return result
}

// SubmitDocuments submits multiple document downloads and returns a channel
// that receives results as they complete.
func (p *DownloadPool) SubmitDocuments(
	ctx context.Context,
	docs []DocumentInfo,
	destDir string,
	cookies []*http.Cookie,
	referer string,
) <-chan DownloadResult {
	results := make(chan DownloadResult, len(docs))

	if len(docs) == 0 {
		close(results)
		return results
	}

	// Ensure destination directory exists
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		results <- DownloadResult{Err: fmt.Errorf("create documents dir: %w", err)}
		close(results)
		return results
	}

	var pending sync.WaitGroup
	pending.Add(len(docs))

	for _, doc := range docs {
		destPath := filepath.Join(destDir, sanitize(doc.Name))
		// Capture destPath in the closure properly
		capturedDestPath := destPath

		job := DownloadJob{
			Type:     JobTypeDocument,
			Name:     doc.Name,
			URL:      doc.DownloadURL,
			DestPath: destPath,
			Cookies:  cookies,
			Referer:  referer,
			Kind:     fileKindBinary,
			Ctx:      ctx,
			OnComplete: func(err error) {
				if err != nil {
					results <- DownloadResult{Err: err}
				} else {
					results <- DownloadResult{Path: capturedDestPath}
				}
				pending.Done()
			},
		}

		p.Submit(job)
	}

	// Close results channel when all documents are done
	go func() {
		pending.Wait()
		close(results)
	}()

	return results
}

// WaitForDocuments is a convenience method that submits documents and waits for all to complete.
// Returns the count of successfully downloaded documents.
func (p *DownloadPool) WaitForDocuments(
	ctx context.Context,
	docs []DocumentInfo,
	destDir string,
	cookies []*http.Cookie,
	referer string,
) int {
	results := p.SubmitDocuments(ctx, docs, destDir, cookies, referer)

	var downloaded int
	for result := range results {
		if result.Err == nil {
			downloaded++
		}
	}
	return downloaded
}
