package admin

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

var identityCmd = &cobra.Command{
	Use:   "identity",
	Short: "Manage user channel identities",
}

func init() {
	// add
	addIdentity := &cobra.Command{
		Use:   "add <userId>",
		Short: "Add a channel identity to a user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			channel, _ := cmd.Flags().GetString("channel")
			identity, _ := cmd.Flags().GetString("identity")
			if channel == "" || identity == "" {
				return fmt.Errorf("--channel and --identity are required")
			}
			body := map[string]interface{}{
				"channel_type":     channel,
				"channel_identity": identity,
			}
			resp, err := doRequest(cmd, http.MethodPost, "/admin/users/"+args[0]+"/identities", body)
			if err != nil {
				return err
			}
			printJSON(resp["result"])
			return nil
		},
	}
	addIdentity.Flags().String("channel", "", "Channel type (e.g. whatsapp, slack)")
	addIdentity.Flags().String("identity", "", "Channel-specific identifier")

	// list
	listIdentities := &cobra.Command{
		Use:   "list <userId>",
		Short: "List identities for a user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := doRequest(cmd, http.MethodGet, "/admin/users/"+args[0]+"/identities", nil)
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
			headers := []string{"ID", "User ID", "Channel", "Identity", "Verified"}
			rows := make([][]string, 0, len(slice))
			for _, v := range slice {
				m, _ := v.(map[string]interface{})
				if m == nil {
					continue
				}
				rows = append(rows, []string{
					num(m, "id"),
					str(m, "userId"),
					str(m, "channelType"),
					str(m, "channelIdentity"),
					boolStr(m, "verified"),
				})
			}
			RenderTable(headers, rows, -1)
			return nil
		},
	}

	// remove
	removeIdentity := &cobra.Command{
		Use:   "remove <userId>",
		Short: "Remove an identity by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, _ := cmd.Flags().GetString("id")
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			resp, err := doRequest(cmd, http.MethodDelete, "/admin/users/"+args[0]+"/identities/"+id, nil)
			if err != nil {
				return err
			}
			printJSON(resp["result"])
			return nil
		},
	}
	removeIdentity.Flags().String("id", "", "Identity row ID")

	identityCmd.AddCommand(addIdentity, listIdentities, removeIdentity)
}
