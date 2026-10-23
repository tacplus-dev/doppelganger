package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// DefaultAnthropicModel is used when the daemon is given no model.
const DefaultAnthropicModel = "claude-opus-5-5"

// Anthropic answers through the Messages API directly: no tools, the repo in a
// cached system block, the answer constrained to the schema by structured
// outputs.
type Anthropic struct {
	client anthropic.Client
	model  string
}

func NewAnthropic(apiKey, model string) *Anthropic {
	if model == "" {
		model = DefaultAnthropicModel
	}
	return &Anthropic{client: anthropic.NewClient(option.WithAPIKey(apiKey)), model: model}
}

func (p *Anthropic) Complete(ctx context.Context, req Request) (json.RawMessage, error) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(req.Schema), &schema); err != nil {
		return nil, fmt.Errorf("answer schema: %w", err)
	}
	resp, err := p.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     p.model,
		MaxTokens: 16000,
		System: []anthropic.TextBlockParam{
			{Text: req.System},
			// The repo is the same on every call; cache it so only the turn is re-read.
			{Text: "# Repository\n\n" + req.Repo, CacheControl: anthropic.NewCacheControlEphemeralParam()},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(req.Turn)),
		},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: anthropic.OutputConfigEffortHigh,
			Format: anthropic.JSONOutputFormatParam{Schema: schema},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	switch resp.StopReason {
	case anthropic.StopReasonEndTurn:
	case anthropic.StopReasonRefusal:
		return nil, fmt.Errorf("anthropic refused: %s %s", resp.StopDetails.Category, resp.StopDetails.Explanation)
	default:
		return nil, fmt.Errorf("anthropic stopped early: %s", resp.StopReason)
	}
	var text strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	out := json.RawMessage(text.String())
	if !json.Valid(out) {
		return nil, errStructuredOutput
	}
	return out, nil
}
