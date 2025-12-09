//go:build embed_ffmpeg

package ffmpegexec

import (
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

//go:embed binaries/amd64/linux/bin/ffmpeg
var ffmpegLinuxAMD64 []byte

//go:embed binaries/arm64/macos/ffmpeg
var ffmpegDarwinARM64 []byte

//go:embed binaries/amd64/windows/bin/ffmpeg.exe
var ffmpegWindowsAMD64 []byte

var errNoEmbedded = errors.New("embedded ffmpeg not available for platform")

var embedded = map[string]struct {
	data []byte
	name string
}{
	"linux/amd64":   {data: ffmpegLinuxAMD64, name: "ffmpeg"},
	"darwin/arm64":  {data: ffmpegDarwinARM64, name: "ffmpeg"},
	"windows/amd64": {data: ffmpegWindowsAMD64, name: "ffmpeg.exe"},
}

var (
	extractedPaths = map[string]string{}
	extractMu      sync.Mutex
)

func extractEmbedded(goos, arch string) (string, error) {
	key := goos + "/" + arch
	entry, ok := embedded[key]
	if !ok || len(entry.data) == 0 {
		return "", errNoEmbedded
	}

	extractMu.Lock()
	if p, ok := extractedPaths[key]; ok {
		extractMu.Unlock()
		return p, nil
	}
	extractMu.Unlock()

	dir, err := os.MkdirTemp("", "adobeconnectdl-ffmpeg-*")
	if err != nil {
		return "", err
	}

	path := filepath.Join(dir, entry.name)
	if err := os.WriteFile(path, entry.data, 0o755); err != nil {
		return "", err
	}

	extractMu.Lock()
	extractedPaths[key] = path
	extractMu.Unlock()

	return path, nil
}
