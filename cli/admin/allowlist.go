package admin

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

var allowCmd = &cobra.Command{
	Use:   "allow <userId>",
	Short: "Add an allowlist entry for a user",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		channel, _ := cmd.Flags().GetString("channel")
		if channel == "" {
			return fmt.Errorf("--channel is required")
		}
		body := map[string]interface{}{
			"user_id":      args[0],
			"channel_type": channel,
			"enabled":      true,
		}
		resp, err := doRequest(cmd, http.MethodPost, "/admin/allowlist", body)
		if err != nil {
			return err
		}
		printJSON(resp["result"])
		return nil
	},
}

var denyCmd = &cobra.Command{
	Use:   "deny <userId>",
	Short: "Disable an allowlist entry for a user",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		channel, _ := cmd.Flags().GetString("channel")
		if channel == "" {
			return fmt.Errorf("--channel is required")
		}
		body := map[string]interface{}{
			"enabled": false,
		}
		path := fmt.Sprintf("/admin/allowlist/%s/%s", args[0], channel)
		resp, err := doRequest(cmd, http.MethodPut, path, body)
		if err != nil {
			return err
		}
		printJSON(resp["result"])
		return nil
	},
}

var allowlistCmd = &cobra.Command{
	Use:   "allowlist",
	Short: "View and manage the allowlist",
}

func init() {
	allowCmd.Flags().String("channel", "", "Channel type (e.g. whatsapp, slack, *)")
	denyCmd.Flags().String("channel", "", "Channel type")

	showAllowlist := &cobra.Command{
		Use:   "show",
		Short: "Show all allowlist entries",
		RunE: func(cmd *cobra.Command, _ []string) error {
			channel, _ := cmd.Flags().GetString("channel")
			path := "/admin/allowlist"
			if channel != "" {
				path = "/admin/allowlist/" + channel
			}
			resp, err := doRequest(cmd, http.MethodGet, path, nil)
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
			slice, _ := result.([]interface{})
			headers := []string{"ID", "User ID", "Channel", "Enabled", "Created By"}
			rows := make([][]string, 0, len(slice))
			for _, v := range slice {
				m, _ := v.(map[string]interface{})
				if m == nil {
					continue
				}
				en, _ := m["enabled"].(bool)
				rows = append(rows, []string{
					num(m, "id"),
					str(m, "userId"),
					str(m, "channelType"),
					BoolBadge(en),
					str(m, "createdBy"),
				})
			}
			RenderTable(headers, rows, -1)
			return nil
		},
	}
	showAllowlist.Flags().String("channel", "", "Filter by channel type")

	removeAllowlist := &cobra.Command{
		Use:   "remove <userId>",
		Short: "Remove an allowlist entry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			channel, _ := cmd.Flags().GetString("channel")
			if channel == "" {
				return fmt.Errorf("--channel is required")
			}
			path := fmt.Sprintf("/admin/allowlist/%s/%s", args[0], channel)
			resp, err := doRequest(cmd, http.MethodDelete, path, nil)
			if err != nil {
				return err
			}
			printJSON(resp["result"])
			return nil
		},
	}
	removeAllowlist.Flags().String("channel", "", "Channel type")

	allowlistCmd.AddCommand(showAllowlist, removeAllowlist)
}
