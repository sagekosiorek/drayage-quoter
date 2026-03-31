package rates

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const anthropicAPI = "https://api.anthropic.com/v1/messages"
const anthropicVersion = "2023-06-01"

// LLMCorrector is the Stage 2 interface for LLM-assisted rate correction.
type LLMCorrector interface {
	CorrectRates(rawEmail string, items []RateItem) ([]RateItem, error)
}

// NoopCorrector passes items through unchanged (default when no LLM is configured).
type NoopCorrector struct{}

func (NoopCorrector) CorrectRates(_ string, items []RateItem) ([]RateItem, error) {
	return items, nil
}

// ClaudeCorrector calls the Anthropic Messages API to correct and supplement
// Stage 1 regex-extracted rate items. Falls back to the input items on any error
// so the parse pipeline is never broken by an LLM failure.
type ClaudeCorrector struct {
	APIKey string
	Model  string       // e.g. "claude-haiku-4-5-20251001"
	Client *http.Client // nil → uses http.DefaultClient
}

var claudeSystemPrompt = strings.TrimSpace(`
You are a freight rate parser for drayage (port trucking) carrier emails.

You receive a raw carrier email and a partial list of rates extracted by a regex parser.
Your job: return the complete, corrected list of all applicable rates from the email.

Canonical charge types and their required units:
  linehaul ($), fuel (%), chassis ($/day), chassis_min (days),
  detention ($/hour), detention_free (hours), storage ($/day),
  yard_pull ($), chassis_split ($), mount ($), lift ($),
  redelivery ($), dry_run ($), toll ($), triaxle ($/day),
  extreme_overweight ($), regular_overweight ($), reefer ($),
  genset ($/day), hazmat ($), stop_off ($), layover ($),
  drop ($), scale ($), congestion ($/hour), congestion_free (hours), gate ($)

Rules:
- Use the exact charge_type names and unit strings listed above.
- Include a charge only if the email contains an actual value for it.
- Set "source": "regex" for items you are keeping unchanged from the regex output.
- Set "source": "llm" for items you corrected or newly extracted.
- Amounts must be positive numbers.
- fuel is always a percentage (e.g. 20, not 0.20).

Respond with a JSON array only — no explanation, no markdown fences:
[{"charge_type":"linehaul","amount":850.00,"unit":"$","source":"regex"}, ...]
`)

// CorrectRates sends the raw email and regex-extracted items to Claude and returns
// a corrected/supplemented list. On any error the original items are returned so
// downstream stages can still proceed.
func (c *ClaudeCorrector) CorrectRates(rawEmail string, items []RateItem) ([]RateItem, error) {
	regexJSON, err := json.Marshal(items)
	if err != nil {
		return items, fmt.Errorf("claude corrector: marshal regex items: %w", err)
	}

	userMsg := fmt.Sprintf("EMAIL:\n%s\n\nREGEX EXTRACTED:\n%s", rawEmail, string(regexJSON))

	payload := map[string]any{
		"model":       c.Model,
		"max_tokens":  1024,
		"temperature": 0,
		"system":      claudeSystemPrompt,
		"messages": []map[string]string{
			{"role": "user", "content": userMsg},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return items, fmt.Errorf("claude corrector: marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, anthropicAPI, bytes.NewReader(body))
	if err != nil {
		return items, fmt.Errorf("claude corrector: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	client := c.Client
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return items, fmt.Errorf("claude corrector: http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return items, fmt.Errorf("claude corrector: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return items, fmt.Errorf("claude corrector: api error %d: %s", resp.StatusCode, string(respBody))
	}

	// Unwrap Anthropic response envelope.
	var envelope struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return items, fmt.Errorf("claude corrector: unmarshal response envelope: %w", err)
	}
	if len(envelope.Content) == 0 {
		return items, fmt.Errorf("claude corrector: empty content in response")
	}

	text := strings.TrimSpace(envelope.Content[0].Text)

	// Strip markdown fences if the model wrapped output despite instructions.
	if strings.HasPrefix(text, "```") {
		if i := strings.Index(text, "\n"); i != -1 {
			text = text[i+1:]
		}
		text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	}

	// Parse the JSON array the model returned.
	var llmItems []struct {
		ChargeType string  `json:"charge_type"`
		Amount     float64 `json:"amount"`
		Unit       string  `json:"unit"`
		Source     string  `json:"source"`
	}
	if err := json.Unmarshal([]byte(text), &llmItems); err != nil {
		return items, fmt.Errorf("claude corrector: parse items JSON %q: %w", text, err)
	}

	out := make([]RateItem, 0, len(llmItems))
	for _, li := range llmItems {
		src := li.Source
		if src != "regex" && src != "llm" {
			src = "llm"
		}
		out = append(out, RateItem{
			ChargeType: li.ChargeType,
			Amount:     li.Amount,
			Unit:       li.Unit,
			Source:     src,
		})
	}
	return out, nil
}
