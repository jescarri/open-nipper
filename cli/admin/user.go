package admin

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

var userCmd = &cobra.Command{
	Use:   "user",
	Short: "Manage users",
}

func init() {
	// add
	addUser := &cobra.Command{
		Use:   "add",
		Short: "Create a new user",
		RunE: func(cmd *cobra.Command, _ []string) error {
			name, _ := cmd.Flags().GetString("name")
			model, _ := cmd.Flags().GetString("model")
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			body := map[string]interface{}{
				"name": name,
			}
			if model != "" {
				body["defaultModel"] = model
			}
			resp, err := doRequest(cmd, http.MethodPost, "/admin/users", body)
			if err != nil {
				return err
			}
			printJSON(resp["result"])
			return nil
		},
	}
	addUser.Flags().String("name", "", "Display name (required)")
	addUser.Flags().String("model", "", "Default model override")

	// list
	listUsers := &cobra.Command{
		Use:   "list",
		Short: "List all users",
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := doRequest(cmd, http.MethodGet, "/admin/users", nil)
			if err != nil {
				return err
			}
			result := resp["result"]
			switch GetFormat(cmd) {
			case FormatJSON:
				printJSON(result)
				return nil
			case FormatText:
				if slice, ok := result.([]interface{}); ok {
					printTextSliceOfMaps(slice)
				}
				return nil
			}
			// table
			slice, _ := result.([]interface{})
			headers := []string{"ID", "Name", "Enabled", "Model", "Created"}
			rows := make([][]string, 0, len(slice))
			for _, v := range slice {
				m, _ := v.(map[string]interface{})
				if m == nil {
					continue
				}
				rows = append(rows, []string{
					str(m, "id"),
					str(m, "name"),
					boolStr(m, "enabled"),
					str(m, "defaultModel"),
					str(m, "createdAt"),
				})
			}
			RenderTable(headers, rows, -1)
			return nil
		},
	}

	// get
	getUser := &cobra.Command{
		Use:   "get <userId>",
		Short: "Get a user by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := doRequest(cmd, http.MethodGet, "/admin/users/"+args[0], nil)
			if err != nil {
				return err
			}
			result := resp["result"]
			switch GetFormat(cmd) {
			case FormatJSON:
				printJSON(result)
				return nil
			case FormatText:
				if m, ok := result.(map[string]interface{}); ok {
					printTextMap(m)
				}
				return nil
			}
			m, _ := result.(map[string]interface{})
			if m == nil {
				return nil
			}
			headers := []string{"ID", "Name", "Enabled", "Model", "Created"}
			rows := [][]string{{
				str(m, "id"),
				str(m, "name"),
				boolStr(m, "enabled"),
				str(m, "defaultModel"),
				str(m, "createdAt"),
			}}
			RenderTable(headers, rows, -1)
			return nil
		},
	}

	// update
	updateUser := &cobra.Command{
		Use:   "update <userId>",
		Short: "Update a user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]interface{}{}
			if name, _ := cmd.Flags().GetString("name"); name != "" {
				body["name"] = name
			}
			if model, _ := cmd.Flags().GetString("model"); model != "" {
				body["defaultModel"] = model
			}
			if enable, _ := cmd.Flags().GetBool("enable"); enable {
				v := true
				body["enabled"] = v
			}
			if disable, _ := cmd.Flags().GetBool("disable"); disable {
				v := false
				body["enabled"] = v
			}
			if len(body) == 0 {
				return fmt.Errorf("provide at least one field to update")
			}
			resp, err := doRequest(cmd, http.MethodPut, "/admin/users/"+args[0], body)
			if err != nil {
				return err
			}
			printJSON(resp["result"])
			return nil
		},
	}
	updateUser.Flags().String("name", "", "New display name")
	updateUser.Flags().String("model", "", "New default model")
	updateUser.Flags().Bool("enable", false, "Enable user")
	updateUser.Flags().Bool("disable", false, "Disable user")

	// delete
	deleteUser := &cobra.Command{
		Use:   "delete <userId>",
		Short: "Delete a user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := doRequest(cmd, http.MethodDelete, "/admin/users/"+args[0], nil)
			if err != nil {
				return err
			}
			printJSON(resp["result"])
			return nil
		},
	}

	// add-channel — adds identity + allowlist in one step for an existing user.
	addChannel := &cobra.Command{
		Use:   "add-channel <userId>",
		Short: "Add a channel identity and allowlist entry in one step",
		Long: `Combines "identity add" and "allow" into a single command.
For each --channel/--identity pair, creates the identity mapping and grants
the allowlist permission. Supports multiple pairs.

Example:
  nipper admin user add-channel usr_019539a1... \
    --channel whatsapp --identity "1234567890@s.whatsapp.net" \
    --channel slack    --identity "U07ABC123"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			userID := args[0]
			channels, _ := cmd.Flags().GetStringArray("channel")
			identities, _ := cmd.Flags().GetStringArray("identity")

			if len(channels) == 0 || len(identities) == 0 {
				return fmt.Errorf("at least one --channel and --identity pair is required")
			}
			if len(channels) != len(identities) {
				return fmt.Errorf("each --channel must have a matching --identity (%d channels, %d identities)", len(channels), len(identities))
			}

			for i, ch := range channels {
				id := identities[i]

				// Add identity.
				identityBody := map[string]interface{}{
					"channel_type":     ch,
					"channel_identity": id,
				}
				if _, err := doRequest(cmd, http.MethodPost, "/admin/users/"+userID+"/identities", identityBody); err != nil {
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
					return fmt.Errorf("adding allowlist for %s: %w", ch, err)
				}
				fmt.Printf("✓ Allowlist granted:  %s\n", ch)
			}

			return nil
		},
	}
	addChannel.Flags().StringArray("channel", nil, "Channel type (repeatable, paired with --identity)")
	addChannel.Flags().StringArray("identity", nil, "Channel-specific identifier (repeatable, paired with --channel)")

	userCmd.AddCommand(addUser, listUsers, getUser, updateUser, deleteUser, addChannel)
}
