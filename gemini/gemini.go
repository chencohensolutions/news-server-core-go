// Package gemini calls the Gemini API's generateContent with structured output (a JSON schema), on the free tier: a
// key from Google AI Studio in a project without billing. It never retries: a 429 is ErrRateLimited, and the caller
// waits for its next run.
package gemini

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

// DefaultModel is the free tier's model the plan picked.
const DefaultModel = "gemini-3.5-flash-lite"

// DefaultBaseURL is the Gemini API.
const DefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// maxOutputTokens is the most an answer may be: high, so a batch of stories is never cut by the default.
const maxOutputTokens = 32_768

// ErrRateLimited is a 429: the free tier's per-minute or per-day limit.
var ErrRateLimited = errors.New("gemini: rate limited (429)")

// ErrUnavailable is a 500 or 503: the model is busy ("high demand"); trying again a little later usually works.
var ErrUnavailable = errors.New("gemini: unavailable")

// Client is one API key and model.
type Client struct {
	Key     string
	Model   string // DefaultModel when ""
	BaseURL string // DefaultBaseURL when ""
	HTTP    *http.Client
}

// ReadKey reads the API key from path (a file with only the key), so it is never in config.json or the repo.
func ReadKey(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return key, nil
}

// Request is one generateContent call: the instructions, the text, and the JSON schema the answer follows.
type Request struct {
	System string
	Prompt string
	Schema any
}

// Usage is what a call cost, in tokens.
type Usage struct {
	PromptTokens   int `json:"promptTokenCount"`
	AnswerTokens   int `json:"candidatesTokenCount"`
	ThoughtsTokens int `json:"thoughtsTokenCount"`
	// CachedTokens are the part of PromptTokens Gemini read from its cache (implicit caching): cheaper, and on the
	// free tier still counted in the limits.
	CachedTokens int `json:"cachedContentTokenCount"`
	TotalTokens  int `json:"totalTokenCount"`
}

type part struct {
	Text    string `json:"text"`
	Thought bool   `json:"thought,omitempty"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type response struct {
	Candidates []struct {
		Content      content `json:"content"`
		FinishReason string  `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata Usage `json:"usageMetadata"`
}

// Generate sends req and returns the answer's JSON (the schema's shape) and its usage.
func (c *Client) Generate(ctx context.Context, req Request) (json.RawMessage, Usage, error) {
	body, err := json.Marshal(map[string]any{
		"systemInstruction": content{Parts: []part{{Text: req.System}}},
		"contents":          []content{{Role: "user", Parts: []part{{Text: req.Prompt}}}},
		"generationConfig": map[string]any{
			"responseMimeType":   "application/json",
			"responseJsonSchema": req.Schema,
			"thinkingConfig":     map[string]any{"thinkingLevel": "low"},
			"maxOutputTokens":    maxOutputTokens,
		},
	})
	if err != nil {
		return nil, Usage{}, err
	}
	model, base := c.Model, c.BaseURL
	if model == "" {
		model = DefaultModel
	}
	if base == "" {
		base = DefaultBaseURL
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/models/"+model+":generateContent", bytes.NewReader(body))
	if err != nil {
		return nil, Usage{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", c.Key)
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Minute}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, Usage{}, fmt.Errorf("gemini: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, Usage{}, fmt.Errorf("gemini: %w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, Usage{}, ErrRateLimited
	}
	if resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusInternalServerError {
		return nil, Usage{}, fmt.Errorf("%w: %s: %s", ErrUnavailable, resp.Status, apiError(data))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, Usage{}, fmt.Errorf("gemini: %s: %s", resp.Status, apiError(data))
	}
	var answer response
	if err := json.Unmarshal(data, &answer); err != nil {
		return nil, Usage{}, fmt.Errorf("gemini: reading the answer: %w", err)
	}
	if answer.PromptFeedback.BlockReason != "" {
		return nil, answer.UsageMetadata, fmt.Errorf("gemini: the request was blocked (%s)", answer.PromptFeedback.BlockReason)
	}
	if len(answer.Candidates) == 0 {
		return nil, answer.UsageMetadata, errors.New("gemini: no answer")
	}
	candidate := answer.Candidates[0]
	var text strings.Builder
	for _, p := range candidate.Content.Parts {
		if !p.Thought {
			text.WriteString(p.Text)
		}
	}
	if candidate.FinishReason != "" && candidate.FinishReason != "STOP" {
		return nil, answer.UsageMetadata, fmt.Errorf("gemini: the answer stopped early (%s)", candidate.FinishReason)
	}
	if !json.Valid([]byte(text.String())) {
		return nil, answer.UsageMetadata, errors.New("gemini: the answer is not JSON")
	}
	return json.RawMessage(text.String()), answer.UsageMetadata, nil
}

// apiError is the message of an API error body, or the body.
func apiError(data []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return strings.TrimSpace(string(data[:min(len(data), 300)]))
}
