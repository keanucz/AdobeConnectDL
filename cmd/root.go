package cmd

import (
	"fmt"
	"os"

	"github.com/charmbracelet/log"
	"github.com/keanucz/AdobeConnectDL/internal/version"
	"github.com/spf13/cobra"
)

var (
	outputDirFlag string
	sessionFlag   string
	verboseFlag   bool
)

// Logger is the global logger instance
var Logger *log.Logger

var rootCmd = &cobra.Command{
	Use:     "adobeconnectdl",
	Short:   "Download Adobe Connect recordings and assets",
	Long:    fmt.Sprintf("adobeconnectdl %s\n\nDownload Adobe Connect recordings and assets.", version.Short()),
	Version: version.Version,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// Initialize logger based on verbose flag
		Logger = log.NewWithOptions(os.Stderr, log.Options{
			ReportTimestamp: verboseFlag,
			Level:           log.InfoLevel,
		})
		if verboseFlag {
			Logger.SetLevel(log.DebugLevel)
		}
	},
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	// Set custom version template to show full version info
	rootCmd.SetVersionTemplate(fmt.Sprintf("adobeconnectdl %s\n", version.Short()))

	rootCmd.PersistentFlags().StringVarP(&outputDirFlag, "output", "o", "", "Output directory (defaults to current working directory)")
	rootCmd.PersistentFlags().StringVar(&sessionFlag, "session", "", "BREEZESESSION token to access private recordings")
	rootCmd.PersistentFlags().BoolVarP(&verboseFlag, "verbose", "v", false, "Enable verbose debug output")
}
