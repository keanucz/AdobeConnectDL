package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"

	"github.com/keanucz/AdobeConnectDL/internal/downloader"
	"github.com/keanucz/AdobeConnectDL/internal/mp4box"
	"github.com/keanucz/AdobeConnectDL/internal/version"
)

var (
	outputDirFlag string
	sessionFlag   string
	urlFileFlag   string
	overwriteFlag bool
)

// makeProgressCallback creates a progress callback that logs at 10% intervals.
// The recordingID identifies which recording is being downloaded.
func makeProgressCallback(recordingID string, logger interface {
	Info(msg any, keyvals ...any)
}) downloader.ProgressCallback {
	var lastPercent int64 = -1
	return func(downloaded, total int64) {
		if total <= 0 {
			return
		}
		percent := (downloaded * 100) / total
		// Log at every 10% increment
		if percent/10 > lastPercent/10 {
			lastPercent = percent
			downloadedMB := float64(downloaded) / (1024 * 1024)
			totalMB := float64(total) / (1024 * 1024)
			msg := fmt.Sprintf("video download: %d%% (%.1f/%.1f MB)", percent, downloadedMB, totalMB)
			logger.Info(msg, "recording", recordingID)
		}
	}
}

func init() {
	rootCmd.AddCommand(downloadCmd)

	// Download-specific flags
	downloadCmd.Flags().StringVarP(
		&outputDirFlag,
		"output",
		"o",
		"",
		"Output directory (defaults to current working directory)",
	)
	downloadCmd.Flags().StringVar(
		&sessionFlag,
		"session",
		"",
		"BREEZESESSION token to access private recordings",
	)
	downloadCmd.Flags().StringVarP(&urlFileFlag, "file", "f", "", "Path to a text file containing URLs (one per line)")
	downloadCmd.Flags().BoolVarP(
		&overwriteFlag,
		"overwrite",
		"y",
		false,
		"Overwrite existing directories without prompting",
	)
}

// formatBytes converts bytes to human readable format.
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

var downloadCmd = &cobra.Command{
	Use:   "download [recording-urls...]",
	Short: "Download one or more Adobe Connect recordings",
	Long: `Download one or more Adobe Connect recordings.

URLs can be provided:
  - As command line arguments (space-separated)
  - From a text file using --file/-f (one URL per line)
  - Or both combined

Examples:
  adobeconnectdl download https://example.com/recording1
  adobeconnectdl download https://example.com/recording1 https://example.com/recording2
  adobeconnectdl download -f urls.txt
  adobeconnectdl download -y https://example.com/recording1  # overwrite existing`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Collect all URLs from args and file
		urls := make([]string, 0)

		// Add URLs from command line arguments
		urls = append(urls, args...)

		// Add URLs from file if specified
		if urlFileFlag != "" {
			fileURLs, err := readURLsFromFile(urlFileFlag)
			if err != nil {
				return fmt.Errorf("failed to read URLs from file: %w", err)
			}
			urls = append(urls, fileURLs...)
		}

		// Validate we have at least one URL
		if len(urls) == 0 {
			return errors.New("no URLs provided. Specify URLs as arguments or use --file/-f")
		}

		// Remove duplicates while preserving order
		urls = deduplicateURLs(urls)

		// Display version banner
		fmt.Println()
		fmt.Println("╭──────────────────────────────────────╮")
		fmt.Printf("│  🎬 adobeconnectdl %-18s│\n", version.Version)
		fmt.Println("╰──────────────────────────────────────╯")
		fmt.Println()

		Logger.Info("starting batch download", "count", len(urls))

		// Try to locate MP4Box for subtitle embedding
		var mp4boxRunner *mp4box.Runner
		if runner, err := mp4box.New(""); err == nil {
			mp4boxRunner = runner
			Logger.Info("MP4Box located", "path", runner.Path())
		} else {
			Logger.Warn("MP4Box not available, subtitles will not be embedded")
		}

		// Track results
		var successful, failed int
		var failedURLs []string
		var totalBytes int64
		batchStartTime := time.Now()

		// Determine output directory
		outputDir := outputDirFlag
		if outputDir == "" {
			var err error
			outputDir, err = os.Getwd()
			if err != nil {
				return fmt.Errorf("failed to get current directory: %w", err)
			}
		}

		// Create HTTP client with optimized transport for concurrent downloads
		// - MaxIdleConns: Total maximum idle connections across all hosts
		// - MaxIdleConnsPerHost: Maximum idle connections per host (important for concurrent downloads from same server)
		// - MaxConnsPerHost: Maximum total connections per host (0 = unlimited)
		// - IdleConnTimeout: How long idle connections remain in the pool
		// Higher values improve performance for concurrent downloads from the same Adobe Connect server
		transport := &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20, // Allow more concurrent connections to same host
			MaxConnsPerHost:     0,  // No limit on total connections per host
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true, // Disable for large binary files (already compressed)
		}
		client := &http.Client{
			Timeout:   0, // No timeout for large downloads
			Transport: transport,
		}

		// Create shared download pool for all recordings
		// This allows MP4, ZIP, and document downloads to share workers across recordings
		poolConfig := downloader.PoolConfig{
			NumWorkers: 12,   // Match maxConcurrentRecordings for optimal throughput
			QueueSize:  1000, // Large queue to handle bursts
			Logger:     Logger,
		}
		pool := downloader.NewDownloadPool(client, poolConfig)
		pool.Start()
		defer pool.Stop()

		dl := downloader.NewWithPool(client, pool)

		// Process URLs - concurrent if overwrite flag is set, sequential otherwise (for prompts)
		if len(urls) > 1 && overwriteFlag {
			// Concurrent processing of multiple URLs with limited concurrency
			// Limit to 12 concurrent recordings (ZIPs, MP4s, documents all downloading concurrently)
			const maxConcurrentRecordings = 12
			Logger.Info("concurrent download mode", "recordings", len(urls), "workers", maxConcurrentRecordings)

			var wg sync.WaitGroup
			var successCount, failCount int32
			var failedMu sync.Mutex
			sem := make(chan struct{}, maxConcurrentRecordings)

			for i, rawURL := range urls {
				wg.Add(1)
				go func(idx int, url string) {
					defer wg.Done()

					// Acquire semaphore
					sem <- struct{}{}
					defer func() { <-sem }()

					Logger.Info(fmt.Sprintf("processing recording %d/%d", idx+1, len(urls)), "url", url)

					// Create a short ID for progress logging
					recordingID := fmt.Sprintf("%d/%d", idx+1, len(urls))

					opts := downloader.Options{
						OutputDir:  outputDir,
						Session:    sessionFlag,
						Log:        Logger,
						Overwrite:  true,
						MP4Box:     mp4boxRunner, // Subtitle embedding handled inside Download()
						OnProgress: makeProgressCallback(recordingID, Logger),
					}

					ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
					defer cancel()

					result, err := dl.Download(ctx, url, opts)
					if err != nil {
						Logger.Error("failed to download recording", "url", url, "error", err)
						atomic.AddInt32(&failCount, 1)
						failedMu.Lock()
						failedURLs = append(failedURLs, url)
						failedMu.Unlock()
						return
					}

					Logger.Info("download complete", "title", result.Title, "location", result.RootDir)
					for _, w := range result.Warnings {
						Logger.Warn(w)
					}

					msg := fmt.Sprintf("\033[32m✓\033[0m Saved recording \"%s\" to %s\n", result.Title, result.RootDir)
					fmt.Fprint(cmd.OutOrStdout(), msg)
					atomic.AddInt32(&successCount, 1)
				}(i, rawURL)
			}

			wg.Wait()
			successful = int(successCount)
			failed = int(failCount)
		} else {
			// Sequential processing (single URL or no overwrite flag)
			for i, rawURL := range urls {
				Logger.Info(fmt.Sprintf("processing recording %d/%d", i+1, len(urls)), "url", rawURL)

				// Create a short ID for progress logging
				recordingID := fmt.Sprintf("%d/%d", i+1, len(urls))

				opts := downloader.Options{
					OutputDir:  outputDir,
					Session:    sessionFlag,
					Log:        Logger,
					Overwrite:  overwriteFlag,
					MP4Box:     mp4boxRunner, // Subtitle embedding handled inside Download()
					OnProgress: makeProgressCallback(recordingID, Logger),
				}

				ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
				result, err := dl.Download(ctx, rawURL, opts)

				// Handle directory exists error with prompt
				if errors.Is(err, downloader.ErrDirectoryExists) && !overwriteFlag {
					fmt.Fprintf(cmd.OutOrStdout(), "\033[1;33m⚠  Directory already exists.\033[0m Overwrite? [y/N]: ")
					reader := bufio.NewReader(os.Stdin)
					response, _ := reader.ReadString('\n')
					response = strings.ToLower(strings.TrimSpace(response))
					if response == "y" || response == "yes" {
						opts.Overwrite = true
						result, err = dl.Download(ctx, rawURL, opts)
					} else {
						Logger.Info("skipping recording", "url", rawURL)
						cancel()
						continue
					}
				}

				cancel()

				if err != nil {
					Logger.Error("failed to download recording", "url", rawURL, "error", err)
					failed++
					failedURLs = append(failedURLs, rawURL)
					continue
				}

				Logger.Info("download complete", "title", result.Title, "location", result.RootDir)

				if result.MP4Path != "" {
					Logger.Info("video saved", "path", result.MP4Path)
				}
				if result.ZipPath != "" {
					Logger.Debug("zip saved", "path", result.ZipPath)
				}
				for _, w := range result.Warnings {
					Logger.Warn(w)
				}

				msg := fmt.Sprintf("\n\033[32m✓\033[0m Saved recording \"%s\" to %s\n", result.Title, result.RootDir)
				fmt.Fprint(cmd.OutOrStdout(), msg)
				successful++
			}
		}

		batchDuration := time.Since(batchStartTime)

		// Print summary
		fmt.Fprintf(cmd.OutOrStdout(), "\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		successColor := "\033[32m" // green
		failColor := "\033[0m"     // default (no color if 0)
		if failed > 0 {
			failColor = "\033[31m" // red
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Download Summary: %s%d %s\033[0m, %s%d %s\033[0m\n",
			successColor, successful, "successful", failColor, failed, "failed")

		// Always show detailed stats
		fmt.Fprintf(cmd.OutOrStdout(), "\n📊 Download Statistics:\n")
		fmt.Fprintf(cmd.OutOrStdout(), "  Total batch time: %s\n", batchDuration.Round(time.Millisecond))
		if totalBytes > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "  Total data: %s\n", formatBytes(totalBytes))
			avgSpeed := float64(totalBytes) / batchDuration.Seconds() / 1024 / 1024
			fmt.Fprintf(cmd.OutOrStdout(), "  Average speed: %.2f MB/s\n", avgSpeed)
		}

		if len(failedURLs) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "\nFailed URLs:\n")
			for _, u := range failedURLs {
				fmt.Fprintf(cmd.OutOrStdout(), "  \033[31m✗\033[0m %s\n", u)
			}
			return fmt.Errorf("%d download(s) failed", failed)
		}

		return nil
	},
}

// readURLsFromFile reads URLs from a text file, one per line.
func readURLsFromFile(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var urls []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		urls = append(urls, line)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return urls, nil
}

// deduplicateURLs removes duplicate URLs while preserving order.
func deduplicateURLs(urls []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(urls))
	for _, url := range urls {
		if !seen[url] {
			seen[url] = true
			result = append(result, url)
		}
	}
	return result
}
