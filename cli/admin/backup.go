package admin

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Trigger a SQLite hot backup",
	RunE: func(cmd *cobra.Command, _ []string) error {
		resp, err := doRequest(cmd, http.MethodPost, "/admin/backup", nil)
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
		if m != nil {
			headers := []string{"Path", "Timestamp"}
			rows := [][]string{{str(m, "path"), str(m, "timestamp")}}
			RenderTable(headers, rows, -1)
		}
		return nil
	},
}

var healthCmd = &cobra.Command{
	Use:   "health",
	Short: "Show gateway health status",
	RunE: func(cmd *cobra.Command, _ []string) error {
		resp, err := doRequest(cmd, http.MethodGet, "/admin/health", nil)
		if err != nil {
			return err
		}
		result, _ := resp["result"].(map[string]interface{})
		switch GetFormat(cmd) {
		case FormatJSON:
			printJSON(result)
			return nil
		case FormatText:
			printTextMap(result)
			return nil
		}
		// table: overall status + components table + agents table
		status := str(result, "status")
		fmt.Println(ColorCyan + "🏥 Gateway Health" + ColorReset)
		fmt.Printf("Overall: %s\n\n", StatusBadge(status))
		components, _ := result["components"].(map[string]interface{})
		if components != nil {
			fmt.Println(ColorCyan + "📦 Components" + ColorReset)
			headers := []string{"Component", "Status"}
			rows := make([][]string, 0, len(components))
			for k, v := range components {
				sub, _ := v.(map[string]interface{})
				st := "unknown"
				if sub != nil {
					st, _ = sub["status"].(string)
				}
				rows = append(rows, []string{k, st})
			}
			RenderTable(headers, rows, 1)
			fmt.Println()
		}
		agents, _ := result["agents"].([]interface{})
		if len(agents) > 0 {
			fmt.Println(ColorCyan + "🤖 Agents (queue health)" + ColorReset)
			headers := []string{"User ID", "Queue", "Consumers", "Ready", "Status"}
			rows := make([][]string, 0, len(agents))
			for _, v := range agents {
				m, _ := v.(map[string]interface{})
				if m == nil {
					continue
				}
				rows = append(rows, []string{
					str(m, "user_id"),
					str(m, "queue"),
					num(m, "consumer_count"),
					num(m, "messages_ready"),
					str(m, "status"),
				})
			}
			RenderTable(headers, rows, 4)
		}
		return nil
	},
}

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Query the admin audit log",
	RunE: func(cmd *cobra.Command, _ []string) error {
		since, _ := cmd.Flags().GetString("since")
		until, _ := cmd.Flags().GetString("until")
		action, _ := cmd.Flags().GetString("action")
		userID, _ := cmd.Flags().GetString("user")

		path := "/admin/audit?"
		if since != "" {
			path += "since=" + since + "&"
		}
		if until != "" {
			path += "until=" + until + "&"
		}
		if action != "" {
			path += "action=" + action + "&"
		}
		if userID != "" {
			path += "user_id=" + userID + "&"
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
		headers := []string{"Timestamp", "Action", "Actor", "Target User", "Details", "IP"}
		rows := make([][]string, 0, len(slice))
		for _, v := range slice {
			m, _ := v.(map[string]interface{})
			if m == nil {
				continue
			}
			rows = append(rows, []string{
				str(m, "timestamp"),
				str(m, "action"),
				str(m, "actor"),
				str(m, "targetUserId"),
				str(m, "details"),
				str(m, "ipAddress"),
			})
		}
		RenderTable(headers, rows, -1)
		return nil
	},
}

func init() {
	auditCmd.Flags().String("since", "", "Start time (RFC3339)")
	auditCmd.Flags().String("until", "", "End time (RFC3339)")
	auditCmd.Flags().String("action", "", "Filter by action")
	auditCmd.Flags().String("user", "", "Filter by user ID")
}
