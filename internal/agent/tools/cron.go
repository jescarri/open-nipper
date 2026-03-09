package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

const cronTimeout = 15 * time.Second

// cronUserIDKey is the context key for the session user ID (cron API scope).
type cronUserIDKey struct{}

// CronContextWithUserID returns a context that carries the given user ID for cron API calls.
// The gateway uses this to scope list/add/remove to the session user when present.
func CronContextWithUserID(ctx context.Context, userID string) context.Context {
	if userID == "" {
		return ctx
	}
	return context.WithValue(ctx, cronUserIDKey{}, userID)
}

func cronUserIDFromContext(ctx context.Context) string {
	if v := ctx.Value(cronUserIDKey{}); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// CronJobItem is a single cron job (matches gateway response shape).
type CronJobItem struct {
	ID       string `json:"id"`
	Schedule string `json:"schedule"`
	UserID   string `json:"user_id"`
	Prompt   string `json:"prompt"`
}

// CronListParams is the input for cron_list_jobs (no parameters).
type CronListParams struct{}

// CronListResult is the result of cron_list_jobs.
type CronListResult struct {
	Jobs  []CronJobItem `json:"jobs"`
	Count int           `json:"count"`
}

// CronAddParams is the input for cron_add_job.
type CronAddParams struct {
	ID       string `json:"id" jsonschema:"description=Unique identifier for this job (e.g. daily-summary). Must be unique for this user.,required"`
	Schedule string `json:"schedule" jsonschema:"description=Six-field cron expression with seconds, e.g. '0 0 9 * * *' for 9:00 daily. Format: second minute hour day month weekday.,required"`
	Prompt   string `json:"prompt" jsonschema:"description=The prompt (message) sent to the agent when the job runs. Cron jobs are prompts only — natural language instructions. Responses are automatically delivered to the user's registered channels.,required"`
}

// CronAddResult is the result of cron_add_job.
type CronAddResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// CronRemoveParams is the input for cron_remove_job.
type CronRemoveParams struct {
	ID string `json:"id" jsonschema:"description=The job id to remove (use cron_list_jobs to see existing ids).,required"`
}

// CronRemoveResult is the result of cron_remove_job.
type CronRemoveResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// cronClient calls the gateway's /agents/me/cron/jobs API with Bearer auth.
type cronClient struct {
	baseURL   string
	authToken string
	userID    string // optional; when set, sent as X-Nipper-User-Id to scope operations to session user
	client    *http.Client
}

func newCronClient(ctx context.Context) (*cronClient, error) {
	baseURL := os.Getenv("NIPPER_GATEWAY_URL")
	authToken := os.Getenv("NIPPER_AUTH_TOKEN")
	if baseURL == "" {
		return nil, fmt.Errorf("NIPPER_GATEWAY_URL is not set")
	}
	if authToken == "" {
		return nil, fmt.Errorf("NIPPER_AUTH_TOKEN is not set")
	}
	return &cronClient{
		baseURL:   baseURL,
		authToken: authToken,
		userID:    cronUserIDFromContext(ctx),
		client:    &http.Client{Timeout: cronTimeout},
	}, nil
}

func (c *cronClient) setCronHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.authToken)
	if c.userID != "" {
		req.Header.Set("X-Nipper-User-Id", c.userID)
	}
}

type gatewayCronListResponse struct {
	OK     bool          `json:"ok"`
	Result []CronJobItem `json:"result"`
	Error  string        `json:"error"`
}

type gatewayCronAddResponse struct {
	OK     bool         `json:"ok"`
	Result *CronJobItem  `json:"result"`
	Error  string        `json:"error"`
}

// ExecCronListJobs lists the current user's cron jobs.
func ExecCronListJobs(ctx context.Context, _ CronListParams) (*CronListResult, error) {
	c, err := newCronClient(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/agents/me/cron/jobs", nil)
	if err != nil {
		return nil, err
	}
	c.setCronHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	// Decode only the first JSON value so trailing bytes (e.g. duplicate write or proxy) don't break parsing.
	dec := json.NewDecoder(bytes.NewReader(respBody))
	var body gatewayCronListResponse
	if err := dec.Decode(&body); err != nil {
		// Some gateways or proxies return a bare number; treat as empty list so the agent can continue.
		var num json.Number
		if json.NewDecoder(bytes.NewReader(respBody)).Decode(&num) == nil {
			return &CronListResult{Jobs: nil, Count: 0}, nil
		}
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if !body.OK {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("gateway auth failed: %s", body.Error)
		}
		return nil, fmt.Errorf("gateway returned: %s", body.Error)
	}
	return &CronListResult{Jobs: body.Result, Count: len(body.Result)}, nil
}

// ExecCronAddJob adds a cron job. Prompt is required (cron jobs are prompts only).
func ExecCronAddJob(ctx context.Context, params CronAddParams) (*CronAddResult, error) {
	if params.ID == "" || params.Schedule == "" || params.Prompt == "" {
		return &CronAddResult{Success: false, Message: "id, schedule, and prompt are required"}, nil
	}
	c, err := newCronClient(ctx)
	if err != nil {
		return nil, err
	}
	body := map[string]interface{}{
		"id":       params.ID,
		"schedule": params.Schedule,
		"prompt":   params.Prompt,
	}
	bodyBytes, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/agents/me/cron/jobs", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	c.setCronHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	// Decode only the first JSON value so trailing bytes (e.g. duplicate write or proxy) don't break parsing.
	dec := json.NewDecoder(bytes.NewReader(respBody))
	var res gatewayCronAddResponse
	if err := dec.Decode(&res); err != nil {
		// Some gateways or proxies return a bare number (e.g. 201 or row id); treat as success.
		var num json.Number
		if json.NewDecoder(bytes.NewReader(respBody)).Decode(&num) == nil {
			return &CronAddResult{Success: true, Message: fmt.Sprintf("Cron job %q added. Schedule: %s", params.ID, params.Schedule)}, nil
		}
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if res.OK {
		return &CronAddResult{Success: true, Message: fmt.Sprintf("Cron job %q added. Schedule: %s", params.ID, params.Schedule)}, nil
	}
	return &CronAddResult{Success: false, Message: res.Error}, nil
}

// ExecCronRemoveJob removes a cron job by id.
func ExecCronRemoveJob(ctx context.Context, params CronRemoveParams) (*CronRemoveResult, error) {
	if params.ID == "" {
		return &CronRemoveResult{Success: false, Message: "id is required"}, nil
	}
	c, err := newCronClient(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/agents/me/cron/jobs/"+url.PathEscape(params.ID), nil)
	if err != nil {
		return nil, err
	}
	c.setCronHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return &CronRemoveResult{Success: true, Message: fmt.Sprintf("Cron job %q removed.", params.ID)}, nil
	}
	respBody, _ := io.ReadAll(resp.Body)
	var errBody struct {
		Error string `json:"error"`
	}
	if json.NewDecoder(bytes.NewReader(respBody)).Decode(&errBody) == nil && errBody.Error != "" {
		return &CronRemoveResult{Success: false, Message: errBody.Error}, nil
	}
	return &CronRemoveResult{Success: false, Message: "job not found or not owned by user"}, nil
}
