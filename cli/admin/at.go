package admin

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

var atCmd = &cobra.Command{
	Use:   "at",
	Short: "View and manage one-shot scheduled jobs",
}

func init() {
	// list
	listAll := &cobra.Command{
		Use:   "list",
		Short: "List at jobs (all users, or filter by --user)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			userID, _ := cmd.Flags().GetString("user")
			path := "/admin/at/jobs"
			if userID != "" {
				path = "/admin/users/" + userID + "/at/jobs"
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
			headers := []string{"ID", "User ID", "Run At", "Prompt", "Notify channels"}
			rows := make([][]string, 0, len(slice))
			for _, v := range slice {
				m, _ := v.(map[string]interface{})
				if m == nil {
					continue
				}
				prompt := str(m, "prompt")
				if len(prompt) > 40 {
					prompt = prompt[:37] + "..."
				}
				channels := str(m, "notify_channels")
				if ch, ok := m["notify_channels"]; ok {
					if arr, ok := ch.([]interface{}); ok && len(arr) > 0 {
						channels = fmt.Sprintf("%v", arr)
					}
				}
				rows = append(rows, []string{
					str(m, "id"),
					str(m, "user_id"),
					str(m, "run_at"),
					prompt,
					channels,
				})
			}
			RenderTable(headers, rows, -1)
			return nil
		},
	}
	listAll.Flags().String("user", "", "Filter by user ID (optional)")

	// add
	addJob := &cobra.Command{
		Use:   "add <userId>",
		Short: "Schedule a one-shot job for a user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			userID := args[0]
			id, _ := cmd.Flags().GetString("id")
			runAt, _ := cmd.Flags().GetString("at")
			prompt, _ := cmd.Flags().GetString("prompt")
			notify, _ := cmd.Flags().GetStringArray("notify")

			if id == "" || runAt == "" || prompt == "" {
				return fmt.Errorf("--id, --at, and --prompt are required")
			}

			body := map[string]interface{}{
				"user_id": userID,
				"id":      id,
				"run_at":  runAt,
				"prompt":  prompt,
			}
			if len(notify) > 0 {
				body["notify_channels"] = notify
			}
			resp, err := doRequest(cmd, http.MethodPost, "/admin/at/jobs", body)
			if err != nil {
				return err
			}
			printJSON(resp["result"])
			return nil
		},
	}
	addJob.Flags().String("id", "", "Job ID (required)")
	addJob.Flags().String("at", "", "Run at time in RFC3339 format (required)")
	addJob.Flags().String("prompt", "", "Prompt to send to agent (required)")
	addJob.Flags().StringArray("notify", nil, "Notify channels (e.g. whatsapp:123@s.whatsapp.net)")

	// remove
	removeJob := &cobra.Command{
		Use:   "remove <userId>",
		Short: "Cancel a pending at job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			userID := args[0]
			id, _ := cmd.Flags().GetString("id")
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			path := fmt.Sprintf("/admin/at/jobs/%s/%s", userID, id)
			resp, err := doRequest(cmd, http.MethodDelete, path, nil)
			if err != nil {
				return err
			}
			printJSON(resp["result"])
			return nil
		},
	}
	removeJob.Flags().String("id", "", "Job ID (required)")

	atCmd.AddCommand(listAll, addJob, removeJob)
}
