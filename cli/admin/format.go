package admin

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/aquasecurity/table"
	"github.com/spf13/cobra"
)

// ANSI color codes for terminal output.
const (
	ColorReset  = "\033[0m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorRed    = "\033[31m"
	ColorCyan   = "\033[36m"
	ColorDim    = "\033[2m"
)

// Format types for --format flag.
const (
	FormatTable = "table"
	FormatJSON  = "json"
	FormatText  = "text"
)

// GetFormat returns the output format from the root --format flag (used by admin commands).
func GetFormat(cmd *cobra.Command) string {
	root := cmd.Root()
	if root == nil {
		return FormatTable
	}
	f, _ := root.PersistentFlags().GetString("format")
	if f == "" {
		return FormatTable
	}
	f = strings.ToLower(strings.TrimSpace(f))
	switch f {
	case FormatTable, FormatJSON, FormatText:
		return f
	default:
		return FormatTable
	}
}

// StatusBadge returns a plain symbol + status for table cells (no ANSI, so column width is correct).
// Uses single-codepoint symbols that render in Powerline and most terminal fonts:
//   ✓ = healthy, ok, enabled, processing, idle, etc.
//   ⚠ = degraded, warning, disabled
//   ✗ = error, offline, revoked, failed, deleted
func StatusBadge(s string) string {
	lower := strings.ToLower(strings.TrimSpace(s))
	switch {
	case lower == "":
		return s
	case lower == "healthy" || lower == "ok" || lower == "enabled" || lower == "processing" || lower == "idle" || lower == "registered" || lower == "provisioned" || lower == "created":
		return "✓ " + s
	case lower == "degraded" || lower == "warning" || lower == "disabled":
		return "⚠ " + s
	case lower == "unhealthy" || lower == "error" || lower == "offline" || lower == "revoked" || lower == "failed" || lower == "deleted":
		return "✗ " + s
	default:
		return "· " + s
	}
}

// BoolBadge returns a plain symbol for table cells (no ANSI).
func BoolBadge(b bool) string {
	if b {
		return "✓ yes"
	}
	return "✗ no"
}

// RenderTable renders a table using aquasecurity/table with headers and rows.
// statusColumnIndex is the 0-based column index to run through StatusBadge; -1 to skip.
func RenderTable(headers []string, rows [][]string, statusColumnIndex int) {
	t := table.New(os.Stdout)
	t.SetRowLines(false)
	t.SetHeaders(headers...)
	for _, row := range rows {
		out := make([]string, len(row))
		copy(out, row)
		if statusColumnIndex >= 0 && statusColumnIndex < len(out) && out[statusColumnIndex] != "" {
			out[statusColumnIndex] = StatusBadge(out[statusColumnIndex])
		}
		t.AddRow(out...)
	}
	t.Render()
}

// printTextMap prints a flat map as key: value lines.
func printTextMap(m map[string]interface{}) {
	for k, v := range m {
		fmt.Printf("%s: %v\n", k, v)
	}
}

// printTextSliceOfMaps prints a slice of maps as key: value blocks separated by newlines.
func printTextSliceOfMaps(slice []interface{}) {
	for i, v := range slice {
		if m, ok := v.(map[string]interface{}); ok {
			if i > 0 {
				fmt.Println()
			}
			printTextMap(m)
		}
	}
}

// str extracts a string from a map, or returns empty.
func str(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok && v != nil {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprint(v)
	}
	return ""
}

// num extracts a number from a map for display.
func num(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok && v != nil {
		switch n := v.(type) {
		case float64:
			return strconv.FormatInt(int64(n), 10)
		case int:
			return strconv.Itoa(n)
		case int64:
			return strconv.FormatInt(n, 10)
		default:
			return fmt.Sprint(v)
		}
	}
	return "0"
}

// boolStr returns "yes"/"no" from a map bool.
func boolStr(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok && v != nil {
		if b, ok := v.(bool); ok {
			if b {
				return "yes"
			}
			return "no"
		}
	}
	return "no"
}
