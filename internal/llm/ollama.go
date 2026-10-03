package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultOllamaURL is where a locally installed Ollama listens.
const DefaultOllamaURL = "http://localhost:11434"

// maxOllamaBody bounds what is read from Ollama in one response.
const maxOllamaBody = 8 << 20

// ErrTruncated means the model stopped because it hit a length limit, so its answer is incomplete.
var ErrTruncated = errors.New("ollama: response was cut off at the context or length limit")

// Ollama talks to an Ollama server.
type Ollama struct {
	HTTP        *http.Client
	BaseURL     string
	Model       string
	KeepAlive   string
	NumCtx      int
	Temperature float64
}

// NormalizeURL turns a configured value or OLLAMA_HOST into a base URL: it adds a scheme when
// missing, drops a trailing slash and falls back to the default when empty.
func NormalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultOllamaURL
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	return strings.TrimRight(raw, "/")
}

// URLFromEnv returns the base URL from a setting, then OLLAMA_HOST, then the default.
func URLFromEnv(configured string) string {
	if strings.TrimSpace(configured) != "" {
		return NormalizeURL(configured)
	}
	return NormalizeURL(os.Getenv("OLLAMA_HOST"))
}

// NewOllama creates a client with a request timeout. Zero values select the defaults.
func NewOllama(baseURL, model string, numCtx int, keepAlive string, timeout time.Duration) *Ollama {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &Ollama{
		BaseURL:     NormalizeURL(baseURL),
		Model:       model,
		NumCtx:      numCtx,
		KeepAlive:   keepAlive,
		Temperature: 0.2,
		HTTP:        &http.Client{Timeout: timeout},
	}
}

// Name implements Completer.
func (o *Ollama) Name() string { return "ollama:" + o.Model }

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Options   map[string]any `json:"options,omitempty"`
	Format    map[string]any `json:"format,omitempty"`
	Model     string         `json:"model"`
	KeepAlive string         `json:"keep_alive,omitempty"`
	Messages  []chatMessage  `json:"messages"`
	Stream    bool           `json:"stream"`
}

type chatResponse struct {
	Error      string      `json:"error"`
	DoneReason string      `json:"done_reason"`
	Message    chatMessage `json:"message"`
}

// Complete implements Completer using /api/chat.
func (o *Ollama) Complete(ctx context.Context, req Request) (string, error) {
	if o.Model == "" {
		return "", errors.New("ollama: no model configured")
	}
	var msgs []chatMessage
	if req.System != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: req.System})
	}
	msgs = append(msgs, chatMessage{Role: "user", Content: req.Prompt})

	options := map[string]any{"temperature": o.Temperature}
	if o.NumCtx > 0 {
		options["num_ctx"] = o.NumCtx
	}
	body := chatRequest{Model: o.Model, Messages: msgs, Stream: false, Options: options, KeepAlive: o.KeepAlive, Format: req.Schema}

	var resp chatResponse
	if err := o.do(ctx, http.MethodPost, "/api/chat", body, &resp); err != nil {
		return "", err
	}
	if resp.Error != "" {
		return "", fmt.Errorf("ollama: %s", resp.Error)
	}
	if resp.DoneReason == "length" {
		return "", ErrTruncated
	}
	if strings.TrimSpace(resp.Message.Content) == "" {
		return "", errors.New("ollama: empty response")
	}
	return resp.Message.Content, nil
}

// ModelInfo describes an installed or running model.
type ModelInfo struct {
	Name          string `json:"name"`
	ParameterSize string `json:"parameter_size,omitempty"`
	Quantization  string `json:"quantization,omitempty"`
	Family        string `json:"family,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
	Size          int64  `json:"size"`
}

type modelList struct {
	Models []struct {
		Name      string `json:"name"`
		ExpiresAt string `json:"expires_at"`
		Details   struct {
			ParameterSize string `json:"parameter_size"`
			Quantization  string `json:"quantization_level"`
			Family        string `json:"family"`
		} `json:"details"`
		Size int64 `json:"size"`
	} `json:"models"`
}

func (o *Ollama) models(ctx context.Context, path string) ([]ModelInfo, error) {
	var list modelList
	if err := o.do(ctx, http.MethodGet, path, nil, &list); err != nil {
		return nil, err
	}
	out := make([]ModelInfo, 0, len(list.Models))
	for _, m := range list.Models {
		out = append(out, ModelInfo{
			Name: m.Name, Size: m.Size, ExpiresAt: m.ExpiresAt,
			ParameterSize: m.Details.ParameterSize, Quantization: m.Details.Quantization, Family: m.Details.Family,
		})
	}
	return out, nil
}

// Installed lists the models that have been pulled (/api/tags).
func (o *Ollama) Installed(ctx context.Context) ([]ModelInfo, error) {
	return o.models(ctx, "/api/tags")
}

// Loaded lists the models currently held in memory (/api/ps).
func (o *Ollama) Loaded(ctx context.Context) ([]ModelInfo, error) {
	return o.models(ctx, "/api/ps")
}

// Version returns the server version, which also tells that the server is up.
func (o *Ollama) Version(ctx context.Context) (string, error) {
	var v struct {
		Version string `json:"version"`
	}
	if err := o.do(ctx, http.MethodGet, "/api/version", nil, &v); err != nil {
		return "", err
	}
	return v.Version, nil
}

// HasModel reports whether a model is installed. A name without a tag matches the ":latest" tag.
func HasModel(installed []ModelInfo, name string) bool {
	if name == "" {
		return false
	}
	want := name
	if !strings.Contains(want, ":") {
		want += ":latest"
	}
	for _, m := range installed {
		if m.Name == want || m.Name == name {
			return true
		}
	}
	return false
}

func (o *Ollama) do(ctx context.Context, method, path string, in, out any) error {
	var rd io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, o.BaseURL+path, rd)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := o.HTTP
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ollama unreachable at %s: %w", o.BaseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxOllamaBody))
	if err != nil {
		return fmt.Errorf("ollama: reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return fmt.Errorf("ollama: %s (HTTP %d)", e.Error, resp.StatusCode)
		}
		return fmt.Errorf("ollama: HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("ollama: unexpected response: %w", err)
	}
	return nil
}
