package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/spf13/cobra"
)

// httpClient is the shared HTTP client for admin API calls.
var httpClient = &http.Client{Timeout: 15 * time.Second}

// adminURL returns the base admin URL from the root persistent flag.
func adminURL(cmd *cobra.Command) string {
	root := cmd.Root()
	url, _ := root.PersistentFlags().GetString("admin-url")
	if url == "" {
		url = "http://127.0.0.1:18790"
	}
	return url
}

// doRequest performs an HTTP request to the admin API and decodes the response.
func doRequest(cmd *cobra.Command, method, path string, body interface{}) (map[string]interface{}, error) {
	base := adminURL(cmd)
	url := base + path

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshalling request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("admin API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		// Response was not a JSON object (e.g. bare number, array, or non-JSON). Hint at --admin-url.
		preview := string(respBody)
		if len(preview) > 80 {
			preview = preview[:77] + "..."
		}
		return nil, fmt.Errorf("decoding response: %w (response body: %q; ensure --admin-url points to the admin server, e.g. http://127.0.0.1:18790)", err, preview)
	}

	if ok, _ := result["ok"].(bool); !ok {
		errMsg, _ := result["error"].(string)
		return nil, fmt.Errorf("API error: %s", errMsg)
	}

	return result, nil
}

// printJSON pretty-prints any value as JSON.
func printJSON(v interface{}) {
	data, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(data))
}
