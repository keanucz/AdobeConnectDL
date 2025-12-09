package cmd

import (
	"fmt"

	"github.com/keanucz/AdobeConnectDL/internal/version"
	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version information",
	Long:  "Print detailed version information including version, commit, build date, and Go version.",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(version.Info())
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
