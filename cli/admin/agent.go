package admin

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Manage agents",
}

func init() {
	// provision
	provision := &cobra.Command{
		Use:   "provision",
		Short: "Provision a new agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			userID, _ := cmd.Flags().GetString("user")
			label, _ := cmd.Flags().GetString("label")
			if userID == "" || label == "" {
				return fmt.Errorf("--user and --label are required")
			}
			body := map[string]interface{}{
				"user_id": userID,
				"label":   label,
			}
			resp, err := doRequest(cmd, http.MethodPost, "/admin/agents", body)
			if err != nil {
				return err
			}
			result, _ := resp["result"].(map[string]interface{})
			agent, _ := result["agent"].(map[string]interface{})
			token, _ := result["authToken"].(string)
			note, _ := result["note"].(string)
			fmt.Println("Agent provisioned successfully.")
			if agent != nil {
				fmt.Printf("Agent ID:   %v\n", agent["id"])
			}
			fmt.Printf("Auth Token: %s\n\n%s\n", token, note)
			fmt.Printf("\nStart your agent with:\n  NIPPER_GATEWAY_URL=http://localhost:18789 \\\n  NIPPER_AUTH_TOKEN=%s \\\n  <your-agent-binary>\n", token)
			return nil
		},
	}
	provision.Flags().String("user", "", "User ID (required)")
	provision.Flags().String("label", "", "Agent label (required)")

	// list
	listAgents := &cobra.Command{
		Use:   "list",
		Short: "List agents",
		RunE: func(cmd *cobra.Command, _ []string) error {
			userID, _ := cmd.Flags().GetString("user")
			path := "/admin/agents"
			if userID != "" {
				path += "?user_id=" + userID
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
			headers := []string{"ID", "User ID", "Label", "Status", "Created"}
			rows := make([][]string, 0, len(slice))
			for _, v := range slice {
				m, _ := v.(map[string]interface{})
				if m == nil {
					continue
				}
				rows = append(rows, []string{
					str(m, "id"),
					str(m, "userId"),
					str(m, "label"),
					str(m, "status"),
					str(m, "createdAt"),
				})
			}
			RenderTable(headers, rows, 3) // status column
			return nil
		},
	}
	listAgents.Flags().String("user", "", "Filter by user ID")

	// get
	getAgent := &cobra.Command{
		Use:   "get <agentId>",
		Short: "Get agent details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := doRequest(cmd, http.MethodGet, "/admin/agents/"+args[0], nil)
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
			headers := []string{"ID", "User ID", "Label", "Status", "Created"}
			rows := [][]string{{
				str(m, "id"),
				str(m, "userId"),
				str(m, "label"),
				str(m, "status"),
				str(m, "createdAt"),
			}}
			RenderTable(headers, rows, 3)
			return nil
		},
	}

	// rotate-token
	rotateToken := &cobra.Command{
		Use:   "rotate-token <agentId>",
		Short: "Rotate an agent's auth token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := doRequest(cmd, http.MethodPost, "/admin/agents/"+args[0]+"/rotate", nil)
			if err != nil {
				return err
			}
			result, _ := resp["result"].(map[string]interface{})
			token, _ := result["authToken"].(string)
			note, _ := result["note"].(string)
			fmt.Printf("New Auth Token: %s\n\n%s\n", token, note)
			return nil
		},
	}

	// revoke
	revoke := &cobra.Command{
		Use:   "revoke <agentId>",
		Short: "Revoke an agent without deleting its record",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := doRequest(cmd, http.MethodPost, "/admin/agents/"+args[0]+"/revoke", nil)
			if err != nil {
				return err
			}
			printJSON(resp["result"])
			return nil
		},
	}

	// delete
	deleteAgent := &cobra.Command{
		Use:   "delete <agentId>",
		Short: "Deprovision and delete an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := doRequest(cmd, http.MethodDelete, "/admin/agents/"+args[0], nil)
			if err != nil {
				return err
			}
			printJSON(resp["result"])
			return nil
		},
	}

	// health — show agent health (queue status + heartbeats from POST /agents/health)
	agentHealth := &cobra.Command{
		Use:   "health",
		Short: "Show agents health (queue status and heartbeats)",
		Long:  "Calls GET /admin/health. Shows queue-based status (consumers, ready) and agents that reported via POST /agents/health (heartbeats, in-memory only).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := doRequest(cmd, http.MethodGet, "/admin/health", nil)
			if err != nil {
				return err
			}
			fullResult, _ := resp["result"].(map[string]interface{})
			agents, _ := fullResult["agents"].([]interface{})
			heartbeats, _ := fullResult["heartbeats"].([]interface{})
			hasAgents := len(agents) > 0
			hasHeartbeats := len(heartbeats) > 0
			if !hasAgents && !hasHeartbeats {
				fmt.Println("No agent health data (no queue status and no heartbeats from POST /agents/health).")
				return nil
			}
			switch GetFormat(cmd) {
			case FormatJSON:
				out := map[string]interface{}{}
				if hasAgents {
					out["agents"] = agents
				}
				if hasHeartbeats {
					out["heartbeats"] = heartbeats
				}
				printJSON(out)
				return nil
			case FormatText:
				if hasAgents {
					printTextSliceOfMaps(agents)
				}
				if hasHeartbeats {
					if hasAgents {
						fmt.Println()
					}
					printTextSliceOfMaps(heartbeats)
				}
				return nil
			}
			// table: show queue-based agents then heartbeats
			if hasAgents {
				fmt.Println("Agents (queue status)")
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
			if hasHeartbeats {
				if hasAgents {
					fmt.Println()
				}
				fmt.Println("Agents (heartbeat from POST /agents/health)")
				headers := []string{"Agent ID", "User ID", "Status", "Last seen"}
				rows := make([][]string, 0, len(heartbeats))
				for _, v := range heartbeats {
					m, _ := v.(map[string]interface{})
					if m == nil {
						continue
					}
					rows = append(rows, []string{
						str(m, "agent_id"),
						str(m, "user_id"),
						str(m, "status"),
						str(m, "last_seen"),
					})
				}
				RenderTable(headers, rows, 2) // status column with badges
			}
			return nil
		},
	}

	agentCmd.AddCommand(provision, listAgents, getAgent, rotateToken, revoke, deleteAgent, agentHealth)
}
