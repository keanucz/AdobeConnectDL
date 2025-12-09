package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/keanucz/AdobeConnectDL/internal/version"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version information",
	Long:  "Print detailed version information including version, commit, build date, and Go version.",
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Println(version.Info())
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
