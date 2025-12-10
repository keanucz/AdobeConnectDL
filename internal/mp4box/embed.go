//go:build embed_mp4box

package mp4box

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/bodgit/sevenzip"
)

// Embedded 7z-compressed MP4Box binaries for each platform.
// 7z ultra compression provides ~67% size reduction.

//go:embed binaries/compressed/mp4box_linux_amd64.7z
var mp4boxLinuxAMD64 []byte

//go:embed binaries/compressed/mp4box_darwin_amd64.7z
var mp4boxDarwinAMD64 []byte

//go:embed binaries/compressed/mp4box_darwin_arm64.7z
var mp4boxDarwinARM64 []byte

//go:embed binaries/compressed/mp4box_windows_amd64.7z
var mp4boxWindowsAMD64 []byte

var errNoEmbedded = errors.New("embedded MP4Box not available for platform")

var embedded = map[string]struct {
	data []byte
	name string
}{
	"linux/amd64":   {data: mp4boxLinuxAMD64, name: "MP4Box"},
	"darwin/amd64":  {data: mp4boxDarwinAMD64, name: "MP4Box"},
	"darwin/arm64":  {data: mp4boxDarwinARM64, name: "MP4Box"},
	"windows/amd64": {data: mp4boxWindowsAMD64, name: "MP4Box.exe"},
}

var (
	extractedPaths = map[string]string{}
	extractMu      sync.Mutex
	cacheDir       string
)

func init() {
	// Use a consistent cache directory so we don't re-extract on every run
	userCache, err := os.UserCacheDir()
	if err != nil {
		userCache = os.TempDir()
	}
	cacheDir = filepath.Join(userCache, "adobeconnectdl", "mp4box")
}

func extractEmbedded(goos, arch string) (string, error) {
	key := goos + "/" + arch
	entry, ok := embedded[key]
	if !ok || len(entry.data) == 0 {
		return "", errNoEmbedded
	}

	extractMu.Lock()
	defer extractMu.Unlock()

	// Check if already extracted in this session
	if p, ok := extractedPaths[key]; ok {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	// Check if already in cache
	cachedPath := filepath.Join(cacheDir, goos+"_"+arch, entry.name)
	if _, err := os.Stat(cachedPath); err == nil {
		extractedPaths[key] = cachedPath
		return cachedPath, nil
	}

	// Extract from 7z archive
	if err := os.MkdirAll(filepath.Dir(cachedPath), 0o755); err != nil {
		return "", fmt.Errorf("create cache dir: %w", err)
	}

	// Open the 7z archive from embedded bytes
	reader := bytes.NewReader(entry.data)
	archive, err := sevenzip.NewReader(reader, int64(len(entry.data)))
	if err != nil {
		return "", fmt.Errorf("open 7z archive: %w", err)
	}

	// Find and extract the MP4Box binary
	for _, file := range archive.File {
		// The file is stored with its path, we want the actual binary
		if filepath.Base(file.Name) != entry.name {
			continue
		}

		rc, err := file.Open()
		if err != nil {
			return "", fmt.Errorf("open file in archive: %w", err)
		}

		outFile, err := os.OpenFile(cachedPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			rc.Close()
			return "", fmt.Errorf("create output file: %w", err)
		}

		_, err = io.Copy(outFile, rc)
		rc.Close()
		outFile.Close()

		if err != nil {
			os.Remove(cachedPath)
			return "", fmt.Errorf("extract file: %w", err)
		}

		extractedPaths[key] = cachedPath
		return cachedPath, nil
	}

	return "", fmt.Errorf("MP4Box binary not found in archive")
}
