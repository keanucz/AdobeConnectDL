//go:build !embed_ffmpeg

package ffmpegexec

import "errors"

var errNoEmbedded = errors.New("embedded ffmpeg not available (build without embed_ffmpeg tag)")

// extractEmbedded is a stub that always returns an error when built without
// the embed_ffmpeg build tag. This allows the package to compile without
// requiring LFS files to be present.
func extractEmbedded(_, _ string) (string, error) {
	return "", errNoEmbedded
}
