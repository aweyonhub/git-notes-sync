package ai

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

// complete dispatches to the configured backend (api | command).
func (g *Generator) complete(system, user string) (string, error) {
	switch g.cfg.Type {
	case "api":
		return g.completeAPI(system, user)
	case "command":
		return g.completeCommand(system, user)
	default:
		return "", fmt.Errorf("unknown ai.type %q", g.cfg.Type)
	}
}

func (g *Generator) timeout() time.Duration {
	if g.cfg.TimeoutSec <= 0 {
		return 60 * time.Second
	}
	return time.Duration(g.cfg.TimeoutSec) * time.Second
}

// completeAPI calls an OpenAI-compatible /chat/completions endpoint.
func (g *Generator) completeAPI(system, user string) (string, error) {
	base := strings.TrimRight(g.cfg.BaseURL, "/")
	if base == "" {
		return "", errors.New("ai.base_url is empty")
	}
	key := os.Getenv(g.cfg.APIKeyEnv)
	if key == "" {
		return "", fmt.Errorf("environment %s is not set", g.cfg.APIKeyEnv)
	}
	payload := map[string]any{
		"model": g.cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature": 0.2,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ai api returned %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 {
		return "", errors.New("ai api returned no choices")
	}
	return cleanOutput(parsed.Choices[0].Message.Content), nil
}

// cleanOutput trims AI output and strips a single outer ``` fence pair.
func cleanOutput(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s[3:], "```"); i >= 0 {
			s = s[3+i+3:]
		} else {
			s = s[3:]
		}
	}
	return strings.TrimSpace(s)
}
