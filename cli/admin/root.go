// Package admin contains Cobra subcommands for nipper admin.
package admin

import "github.com/spf13/cobra"

// AdminCmd is the parent "admin" command.
var AdminCmd = &cobra.Command{
	Use:   "admin",
	Short: "Manage users, agents, identities, allowlist, and policies",
}

func init() {
	AdminCmd.AddCommand(userCmd)
	AdminCmd.AddCommand(identityCmd)
	AdminCmd.AddCommand(allowCmd)
	AdminCmd.AddCommand(denyCmd)
	AdminCmd.AddCommand(allowlistCmd)
	AdminCmd.AddCommand(agentCmd)
	AdminCmd.AddCommand(cronCmd)
	AdminCmd.AddCommand(backupCmd)
	AdminCmd.AddCommand(healthCmd)
	AdminCmd.AddCommand(auditCmd)
	AdminCmd.AddCommand(bootstrapCmd)
	AdminCmd.AddCommand(atCmd)
}
