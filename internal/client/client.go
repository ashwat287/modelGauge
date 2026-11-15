// Package client is the authenticated HTTP client shared by the CLI and agent.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nerdylucifer/modelgauge/pkg/protocol"
)

type Client struct {
	BaseURL    string
	AdminToken string
	HTTPClient *http.Client
}

func (c *Client) DoJSON(ctx context.Context, method, path, tokenKind string, input, output any) (int, error) {
	return c.doJSON(ctx, method, path, tokenKind, input, output)
}
func (c *Client) doJSON(ctx context.Context, method, path, tokenKind string, input, output any) (int, error) {
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
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	switch tokenKind {
	case "admin":
		request.Header.Set("X-ModelGauge-Admin-Token", c.AdminToken)
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	if c.HTTPClient != nil {
		copied := *c.HTTPClient
		httpClient = &copied
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
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

// StreamJSON keeps the request open until its foreground operation finishes.
func (c *Client) StreamJSON(ctx context.Context, path string, input, output any, progress io.Writer) error {
	defer func() {
		if ctx.Err() != nil {
			stop, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			if _, err := c.DoJSON(stop, "POST", "/v1/cancel", "admin", nil, nil); err != nil && progress != nil {
				fmt.Fprintln(progress, "Could not confirm the worker stopped:", err)
			}
		}
	}()
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-ModelGauge-Admin-Token", c.AdminToken)
	httpClient := &http.Client{}
	if c.HTTPClient != nil {
		copy := *c.HTTPClient
		httpClient = &copy
		httpClient.Timeout = 0
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("controller returned %s: %s", response.Status, strings.TrimSpace(string(data)))
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<20))
	last := ""
	for {
		var event protocol.Event
		if err := decoder.Decode(&event); err != nil {
			if err == io.EOF {
				return fmt.Errorf("operation ended without a final result; rerun from the beginning")
			}
			return err
		}
		if event.Log != "" && event.Log != last {
			if progress != nil {
				if _, err := fmt.Fprintln(progress, event.Log); err != nil {
					return err
				}
			}
			last = event.Log
		}
		if event.Error != "" {
			return fmt.Errorf("operation failed: %s", event.Error)
		}
		if event.Result != nil {
			return json.Unmarshal(event.Result, output)
		}
	}
}
