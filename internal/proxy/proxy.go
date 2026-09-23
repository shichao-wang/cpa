// Package proxy talks to a CPA (CLIProxyAPI) or any Anthropic/OpenAI
// compatible gateway: enough to list models and check reachability.
package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Model is one entry from GET /v1/models.
type Model struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
	OwnedBy     string `json:"owned_by,omitempty"`
	Created     int64  `json:"created,omitempty"`
}

// Label is what to show a human: the display name when the gateway provides
// one, otherwise the raw id.
func (m Model) Label() string {
	if m.DisplayName != "" && m.DisplayName != m.ID {
		return fmt.Sprintf("%s (%s)", m.ID, m.DisplayName)
	}
	return m.ID
}

// Client is a minimal gateway client.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// New builds a client with a sane timeout.
func New(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 10 * time.Second},
	}
}

// modelsURL appends /v1/models without doubling an existing /v1 suffix.
func (c *Client) modelsURL() string {
	base := c.BaseURL
	if base == "" {
		base = "http://127.0.0.1:8317"
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/models"
	}
	return base + "/v1/models"
}

// ListModels fetches the model catalogue.
func (c *Client) ListModels(ctx context.Context) ([]Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.modelsURL(), nil)
	if err != nil {
		return nil, err
	}
	// Gateways differ on which header they read: Anthropic-style endpoints read
	// x-api-key, OpenAI-compatible ones read the bearer token.
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
		req.Header.Set("x-api-key", c.APIKey)
	}
	// No anthropic-version on purpose. Some gateways treat that header as a switch
	// between two model-id spaces, answering with aliased ids (and display names)
	// instead of the names the upstream really uses. The aliases are opaque and
	// make family matching useless, and both spaces route identically, so the
	// unversioned catalogue is the better one to choose from.
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d: %s", c.modelsURL(), resp.StatusCode, firstLine(body))
	}

	var payload struct {
		Data []Model `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%s: unexpected response: %w", c.modelsURL(), err)
	}
	models := payload.Data
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

// UpdateBaseURL returns a copy pointed at another endpoint.
func (c *Client) UpdateBaseURL(base string) *Client {
	clone := *c
	clone.BaseURL = strings.TrimRight(strings.TrimSpace(base), "/")
	return &clone
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
