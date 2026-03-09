package admin

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

var cronCmd = &cobra.Command{
	Use:   "cron",
	Short: "View cron jobs (scheduled prompts)",
}

func init() {
	// list all
	listAll := &cobra.Command{
		Use:   "list",
		Short: "List cron jobs (all users, or filter by --user)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			userID, _ := cmd.Flags().GetString("user")
			path := "/admin/cron/jobs"
			if userID != "" {
				path = "/admin/users/" + userID + "/cron/jobs"
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
			headers := []string{"ID", "User ID", "Schedule", "Prompt", "Notify channels"}
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
					str(m, "schedule"),
					prompt,
					channels,
				})
			}
			RenderTable(headers, rows, -1)
			return nil
		},
	}
	listAll.Flags().String("user", "", "Filter by user ID (optional)")
	cronCmd.AddCommand(listAll)
}
