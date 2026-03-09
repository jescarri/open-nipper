package admin

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "Create a user, add channel identities, allowlist entries, and provision an agent in one step",
	Long: `Bootstrap performs the full onboarding flow in a single command:
  1. Creates a new user
  2. Adds channel identity mappings (--channel + --identity pairs)
  3. Grants allowlist permissions for each channel
  4. Provisions an agent and returns the auth token

Example:
  nipper admin bootstrap \
    --name "Alice" \
    --channel whatsapp --identity "1234567890@s.whatsapp.net" \
    --channel slack    --identity "U07ABC123" \
    --agent-label "prod-01"

On failure, any partially created resources are rolled back (user delete cascades to all child records).`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		name, _ := cmd.Flags().GetString("name")
		channels, _ := cmd.Flags().GetStringArray("channel")
		identities, _ := cmd.Flags().GetStringArray("identity")
		agentLabel, _ := cmd.Flags().GetString("agent-label")
		model, _ := cmd.Flags().GetString("model")

		if name == "" {
			return fmt.Errorf("--name is required")
		}
		if agentLabel == "" {
			return fmt.Errorf("--agent-label is required")
		}
		if len(channels) == 0 || len(identities) == 0 {
			return fmt.Errorf("at least one --channel and --identity pair is required")
		}
		if len(channels) != len(identities) {
			return fmt.Errorf("each --channel must have a matching --identity (%d channels, %d identities)", len(channels), len(identities))
		}

		// Step 1: Create user.
		userBody := map[string]interface{}{"name": name}
		if model != "" {
			userBody["defaultModel"] = model
		}
		userResp, err := doRequest(cmd, http.MethodPost, "/admin/users", userBody)
		if err != nil {
			return fmt.Errorf("creating user: %w", err)
		}
		user, _ := userResp["result"].(map[string]interface{})
		userID, _ := user["id"].(string)
		fmt.Printf("✓ User created:      %s (%s)\n", userID, name)

		// rollback deletes the user (cascades to identities, allowlist, agents).
		rollback := func() {
			_, _ = doRequest(cmd, http.MethodDelete, "/admin/users/"+userID, nil)
			fmt.Printf("\n✗ Rolled back: user %s deleted\n", userID)
		}

		// Step 2 & 3: Add identities and allowlist entries.
		for i, ch := range channels {
			id := identities[i]

			// Add identity.
			identityBody := map[string]interface{}{
				"channel_type":     ch,
				"channel_identity": id,
			}
			if _, err := doRequest(cmd, http.MethodPost, "/admin/users/"+userID+"/identities", identityBody); err != nil {
				rollback()
				return fmt.Errorf("adding identity %s/%s: %w", ch, id, err)
			}
			fmt.Printf("✓ Identity added:    %s → %s\n", ch, id)

			// Add allowlist.
			allowBody := map[string]interface{}{
				"user_id":      userID,
				"channel_type": ch,
				"enabled":      true,
			}
			if _, err := doRequest(cmd, http.MethodPost, "/admin/allowlist", allowBody); err != nil {
				rollback()
				return fmt.Errorf("adding allowlist for %s: %w", ch, err)
			}
			fmt.Printf("✓ Allowlist granted:  %s\n", ch)
		}

		// Step 4: Provision agent.
		agentBody := map[string]interface{}{
			"user_id": userID,
			"label":   agentLabel,
		}
		agentResp, err := doRequest(cmd, http.MethodPost, "/admin/agents", agentBody)
		if err != nil {
			rollback()
			return fmt.Errorf("provisioning agent: %w", err)
		}
		result, _ := agentResp["result"].(map[string]interface{})
		agent, _ := result["agent"].(map[string]interface{})
		token, _ := result["authToken"].(string)

		agentID := ""
		if agent != nil {
			agentID, _ = agent["id"].(string)
		}
		fmt.Printf("✓ Agent provisioned: %s\n", agentID)

		fmt.Printf("\n┌─────────────────────────────────────────────────────────────────┐\n")
		fmt.Printf("│  Auth Token (save now — shown only once):                       │\n")
		fmt.Printf("│  %s  │\n", token)
		fmt.Printf("└─────────────────────────────────────────────────────────────────┘\n")
		fmt.Printf("\nTo start the agent:\n")
		fmt.Printf("  export NIPPER_GATEWAY_URL=\"http://localhost:18789\"\n")
		fmt.Printf("  export NIPPER_AUTH_TOKEN=\"%s\"\n", token)

		return nil
	},
}

func init() {
	bootstrapCmd.Flags().String("name", "", "User display name (required)")
	bootstrapCmd.Flags().StringArray("channel", nil, "Channel type (repeatable, paired with --identity)")
	bootstrapCmd.Flags().StringArray("identity", nil, "Channel-specific identifier (repeatable, paired with --channel)")
	bootstrapCmd.Flags().String("agent-label", "", "Agent label (required)")
	bootstrapCmd.Flags().String("model", "", "Default model override")
}
