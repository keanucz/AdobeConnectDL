package version

import (
	"fmt"
	"runtime"
)

// These variables are set at build time using ldflags.
// Example:
//
//	go build -ldflags "-X github.com/keanucz/AdobeConnectDL/internal/version.Version=v1.0.0 \
//	  -X github.com/keanucz/AdobeConnectDL/internal/version.Commit=abc123 \
//	  -X github.com/keanucz/AdobeConnectDL/internal/version.Date=2025-01-01T00:00:00Z"
var (
	// Version is the semantic version (e.g., v1.0.0)
	Version = "dev"
	// Commit is the git commit SHA
	Commit = "unknown"
	// Date is the build date
	Date = "unknown"
)

// Info returns a formatted version string with all build information.
func Info() string {
	return fmt.Sprintf("Version:    %s\nCommit:     %s\nBuilt:      %s\nGo version: %s\nOS/Arch:    %s/%s",
		Version,
		Commit,
		Date,
		runtime.Version(),
		runtime.GOOS,
		runtime.GOARCH,
	)
}

// Short returns a short version string.
func Short() string {
	return fmt.Sprintf("%s (%s)", Version, Commit)
}
