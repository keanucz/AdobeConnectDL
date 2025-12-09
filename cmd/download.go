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
	"time"

	"github.com/keanucz/AdobeConnectDL/internal/downloader"
	"github.com/keanucz/AdobeConnectDL/internal/ffmpegexec"
	"github.com/keanucz/AdobeConnectDL/internal/version"
	"github.com/spf13/cobra"
)

var urlFileFlag string
var overwriteFlag bool

func init() {
	rootCmd.AddCommand(downloadCmd)
	downloadCmd.Flags().StringVarP(&urlFileFlag, "file", "f", "", "Path to a text file containing URLs (one per line)")
	downloadCmd.Flags().BoolVarP(&overwriteFlag, "overwrite", "y", false, "Overwrite existing directories without prompting")
}

// formatBytes converts bytes to human readable format
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
  adobeconnectdl download -f urls.txt https://example.com/additional`,
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
			return fmt.Errorf("no URLs provided. Specify URLs as arguments or use --file/-f")
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

		client := &http.Client{Timeout: 60 * time.Second}
		dl := downloader.New(client)

		// Try to locate ffmpeg for subtitle embedding
		var ff downloader.FFmpegRunner
		if runner, err := ffmpegexec.New(""); err == nil {
			ff = runner
			Logger.Info("ffmpeg located", "path", runner.Path())
		} else {
			Logger.Warn("ffmpeg not available, subtitles will not be embedded")
		}

		// Track results
		var successful, failed int
		var failedURLs []string

		for i, rawURL := range urls {
			Logger.Info(fmt.Sprintf("processing recording %d/%d", i+1, len(urls)), "url", rawURL)

			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)

			// Progress tracking for MP4 download
			var progressMu sync.Mutex
			var lastPercent int
			onProgress := func(downloaded, total int64) {
				progressMu.Lock()
				defer progressMu.Unlock()

				if total <= 0 {
					return
				}

				percent := int(downloaded * 100 / total)
				if percent != lastPercent && percent%5 == 0 {
					lastPercent = percent
					Logger.Info("downloading video",
						"progress", fmt.Sprintf("%d%%", percent),
						"downloaded", formatBytes(downloaded),
						"total", formatBytes(total))
				}
			}

			opts := downloader.Options{
				OutputDir:  outputDirFlag,
				Session:    sessionFlag,
				Log:        Logger,
				FFmpeg:     ff,
				OnProgress: onProgress,
				Overwrite:  overwriteFlag,
			}

			result, err := dl.Download(ctx, rawURL, opts)

			// Handle directory exists error with prompt
			if errors.Is(err, downloader.ErrDirectoryExists) {
				fmt.Fprintf(cmd.OutOrStdout(), "\033[1;33m⚠  Directory already exists with files.\033[0m Overwrite? [y/N]: ")
				var response string
				fmt.Scanln(&response)
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

			Logger.Info("download complete",
				"title", result.Title,
				"location", result.RootDir)

			if result.MP4Path != "" {
				Logger.Info("video saved", "path", result.MP4Path)
			}
			if result.ZipPath != "" {
				Logger.Debug("zip saved", "path", result.ZipPath)
			}
			if result.ExtractedDir != "" {
				Logger.Debug("assets extracted", "path", result.ExtractedDir)
			}
			for _, w := range result.Warnings {
				Logger.Warn(w)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "\n\033[32m✓\033[0m Saved recording \"%s\" to %s\n", result.Title, result.RootDir)
			successful++
		}

		// Print summary
		fmt.Fprintf(cmd.OutOrStdout(), "\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		successColor := "\033[32m" // green
		failColor := "\033[0m"     // default (no color if 0)
		if failed > 0 {
			failColor = "\033[31m" // red
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Download Summary: %s%d %s\033[0m, %s%d %s\033[0m\n",
			successColor, successful, "successful", failColor, failed, "failed")
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

// readURLsFromFile reads URLs from a text file, one per line
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

// deduplicateURLs removes duplicate URLs while preserving order
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
