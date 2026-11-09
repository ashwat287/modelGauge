// Package client is the authenticated HTTP client shared by the CLI and agent.
package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	BaseURL    string
	AgentToken string
	AdminToken string
	HTTPClient *http.Client
}

func (c *Client) DoJSON(ctx context.Context, method, path, tokenKind string, input, output any) (int, error) {
	return c.doJSON(ctx, method, path, tokenKind, input, output, "")
}
func (c *Client) doJSON(ctx context.Context, method, path, tokenKind string, input, output any, requestID string) (int, error) {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if err != nil {
		return 0, err
	}
	if requestID != "" {
		request.Header.Set("X-ModelGauge-Request-ID", requestID)
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	switch tokenKind {
	case "agent":
		request.Header.Set("X-ModelGauge-Token", c.AgentToken)
	case "admin":
		request.Header.Set("X-ModelGauge-Admin-Token", c.AdminToken)
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent {
		return response.StatusCode, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return response.StatusCode, fmt.Errorf("controller returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	if output != nil {
		if err := json.NewDecoder(response.Body).Decode(output); err != nil {
			return response.StatusCode, err
		}
	}
	return response.StatusCode, nil
}

// Submission is the public receipt for an accepted benchmark or model install.
type Submission struct {
	PlanID string `json:"plan_id,omitempty"`
	JobID  string `json:"job_id,omitempty"`
	Status string `json:"status"`
}

// SubmitJSON is intentionally separate from general HTTP transport: job owners
// supply the retry ID from their existing plan/campaign or pending install.
func (c *Client) SubmitJSON(ctx context.Context, path, requestID string, input any, output *Submission) (int, error) {
	if requestID == "" {
		return 0, fmt.Errorf("submission requires a retry identity")
	}
	var health struct {
		SubmissionProtocol string `json:"submission_protocol"`
	}
	if _, err := c.DoJSON(ctx, "GET", "/healthz", "", nil, &health); err != nil {
		return 0, fmt.Errorf("check controller submission support: %w", err)
	}
	if health.SubmissionProtocol != "idempotent-v1" {
		return 0, fmt.Errorf("controller does not support safe submission retries; update it before submitting")
	}
	sum := sha256.Sum256([]byte(requestID + "\n" + path))
	return c.doJSON(ctx, "POST", path, "admin", input, output, fmt.Sprintf("%x", sum))
}
