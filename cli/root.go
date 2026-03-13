// Package cli defines all Cobra commands for the nipper binary.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jescarri/open-nipper/cli/admin"
)

var (
	cfgFile   string
	logLevel  string
	logFormat string
	adminURL  string
)

// Root is the top-level cobra command.
var Root = &cobra.Command{
	Use:   "nipper",
	Short: "Open-Nipper gateway and admin CLI",
	Long:  "Open-Nipper: a multi-channel AI gateway and admin tool.",
}

func init() {
	Root.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "path to config file (default: ~/.open-nipper/config.yaml)")
	Root.PersistentFlags().StringVar(&logLevel, "log-level", "", "override log level (debug|info|warn|error)")
	Root.PersistentFlags().StringVar(&logFormat, "log-format", "", "override log format (json|text)")
	Root.PersistentFlags().StringVar(&adminURL, "admin-url", "http://127.0.0.1:18790", "admin API base URL")
	Root.PersistentFlags().StringP("format", "f", "table", "Output format for admin commands: table, json, or text")

	Root.AddCommand(serveCmd)
	Root.AddCommand(agentCmd)
	Root.AddCommand(admin.AdminCmd)
}

// Execute runs the root command and exits on error.
func Execute() {
	if err := Root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
