package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// AtJobItem is a single at job (matches gateway response shape).
type AtJobItem struct {
	ID     string `json:"id"`
	RunAt  string `json:"run_at"`
	UserID string `json:"user_id"`
	Prompt string `json:"prompt"`
}

// AtListParams is the input for at_list_jobs (no parameters).
type AtListParams struct{}

// AtListResult is the result of at_list_jobs.
type AtListResult struct {
	Jobs  []AtJobItem `json:"jobs"`
	Count int         `json:"count"`
}

// AtAddParams is the input for at_add_job.
type AtAddParams struct {
	ID     string `json:"id" jsonschema:"description=Unique identifier for this job (e.g. reminder-10pm). Must be unique for this user.,required"`
	RunAt  string `json:"run_at" jsonschema:"description=RFC 3339 timestamp when the job should fire (e.g. 2026-03-08T22:00:00-08:00). Must be in the future. Use get_datetime to find the current time and timezone.,required"`
	Prompt string `json:"prompt" jsonschema:"description=The prompt (message) sent to the agent when the job fires. AT jobs are prompts only — natural language instructions. Responses are automatically delivered to the user's registered channels.,required"`
}

// AtAddResult is the result of at_add_job.
type AtAddResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// AtRemoveParams is the input for at_remove_job.
type AtRemoveParams struct {
	ID string `json:"id" jsonschema:"description=The job id to remove (use at_list_jobs to see existing ids).,required"`
}

// AtRemoveResult is the result of at_remove_job.
type AtRemoveResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// atClient calls the gateway's /agents/me/at/jobs API with Bearer auth.
type atClient struct {
	baseURL   string
	authToken string
	userID    string
	client    *http.Client
}

func newAtClient(ctx context.Context) (*atClient, error) {
	// Reuse the same env vars and timeout as the cron client.
	cc, err := newCronClient(ctx)
	if err != nil {
		return nil, err
	}
	return &atClient{
		baseURL:   cc.baseURL,
		authToken: cc.authToken,
		userID:    cc.userID,
		client:    cc.client,
	}, nil
}

func (c *atClient) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.authToken)
	if c.userID != "" {
		req.Header.Set("X-Nipper-User-Id", c.userID)
	}
}

type gatewayAtListResponse struct {
	OK     bool        `json:"ok"`
	Result []AtJobItem `json:"result"`
	Error  string      `json:"error"`
}

type gatewayAtAddResponse struct {
	OK     bool       `json:"ok"`
	Result *AtJobItem `json:"result"`
	Error  string     `json:"error"`
}

// ExecAtListJobs lists the current user's pending at jobs.
func ExecAtListJobs(ctx context.Context, _ AtListParams) (*AtListResult, error) {
	c, err := newAtClient(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/agents/me/at/jobs", nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(respBody))
	var body gatewayAtListResponse
	if err := dec.Decode(&body); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if !body.OK {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("gateway auth failed: %s", body.Error)
		}
		return nil, fmt.Errorf("gateway returned: %s", body.Error)
	}
	return &AtListResult{Jobs: body.Result, Count: len(body.Result)}, nil
}

// ExecAtAddJob adds a one-shot at job. Prompt is required (at jobs are prompts only).
func ExecAtAddJob(ctx context.Context, params AtAddParams) (*AtAddResult, error) {
	if params.ID == "" || params.RunAt == "" || params.Prompt == "" {
		return &AtAddResult{Success: false, Message: "id, run_at, and prompt are required"}, nil
	}
	c, err := newAtClient(ctx)
	if err != nil {
		return nil, err
	}
	body := map[string]interface{}{
		"id":     params.ID,
		"run_at": params.RunAt,
		"prompt": params.Prompt,
	}
	bodyBytes, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/agents/me/at/jobs", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
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
	dec := json.NewDecoder(bytes.NewReader(respBody))
	var res gatewayAtAddResponse
	if err := dec.Decode(&res); err != nil {
		var num json.Number
		if json.NewDecoder(bytes.NewReader(respBody)).Decode(&num) == nil {
			return &AtAddResult{Success: true, Message: fmt.Sprintf("AT job %q scheduled for %s", params.ID, params.RunAt)}, nil
		}
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if res.OK {
		return &AtAddResult{Success: true, Message: fmt.Sprintf("AT job %q scheduled for %s", params.ID, params.RunAt)}, nil
	}
	return &AtAddResult{Success: false, Message: res.Error}, nil
}

// ExecAtRemoveJob removes a pending at job by id.
func ExecAtRemoveJob(ctx context.Context, params AtRemoveParams) (*AtRemoveResult, error) {
	if params.ID == "" {
		return &AtRemoveResult{Success: false, Message: "id is required"}, nil
	}
	c, err := newAtClient(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/agents/me/at/jobs/"+url.PathEscape(params.ID), nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return &AtRemoveResult{Success: true, Message: fmt.Sprintf("AT job %q removed.", params.ID)}, nil
	}
	respBody, _ := io.ReadAll(resp.Body)
	var errBody struct {
		Error string `json:"error"`
	}
	if json.NewDecoder(bytes.NewReader(respBody)).Decode(&errBody) == nil && errBody.Error != "" {
		return &AtRemoveResult{Success: false, Message: errBody.Error}, nil
	}
	return &AtRemoveResult{Success: false, Message: "job not found or not owned by user"}, nil
}
