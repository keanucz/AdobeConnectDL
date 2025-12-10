//go:build !embed_mp4box

package mp4box

import "errors"

var errNoEmbedded = errors.New("embedded MP4Box not available (build without embed_mp4box tag)")

// extractEmbedded is a stub that always returns an error when built without
// the embed_mp4box build tag. This allows the package to compile without
// requiring binaries to be present.
func extractEmbedded(_, _ string) (string, error) {
	return "", errNoEmbedded
}
