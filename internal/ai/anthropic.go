package ai

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// anthropicModel talks to the Anthropic Messages API: adaptive thinking at
// the model's default effort, strict tools with tool_choice auto (current
// models reject forced tool use), and the server-side refusal fallback.
type anthropicModel struct {
	client anthropic.Client
	model  string
}

func newAnthropic(c Connection, baseURL string, hc *http.Client) *anthropicModel {
	opts := []option.RequestOption{
		// The household's key only: never ANTHROPIC_API_KEY, profiles or a
		// base URL from the server's environment.
		option.WithoutEnvironmentDefaults(),
		option.WithAPIKey(c.APIKey),
		option.WithHTTPClient(hc),
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &anthropicModel{client: anthropic.NewClient(opts...), model: c.Model}
}

func (m *anthropicModel) Test(ctx context.Context) error {
	_, err := m.Chat(ctx, testRequest)
	return err
}

func (m *anthropicModel) Chat(ctx context.Context, req Request) (Response, error) {
	params := anthropic.BetaMessageNewParams{
		Model:      m.model,
		MaxTokens:  int64(req.MaxTokens),
		Messages:   m.messages(req.Messages),
		Thinking:   anthropic.BetaThinkingConfigParamUnion{OfAdaptive: &anthropic.BetaThinkingConfigAdaptiveParam{}},
		ToolChoice: anthropic.BetaToolChoiceUnionParam{OfAuto: &anthropic.BetaToolChoiceAutoParam{}},
		Fallbacks:  anthropic.BetaFallbacksParamOfDefault(),
		Betas:      []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
	}
	if req.System != "" {
		params.System = []anthropic.BetaTextBlockParam{{Text: req.System}}
	}
	for _, t := range req.Tools {
		s := t.schema()
		params.Tools = append(params.Tools, anthropic.BetaToolUnionParam{OfTool: &anthropic.BetaToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			Strict:      anthropic.Bool(true),
			InputSchema: anthropic.BetaToolInputSchemaParam{
				Properties:  s["properties"],
				Required:    s["required"].([]string),
				ExtraFields: map[string]any{"additionalProperties": false},
			},
		}})
	}
	if len(params.Tools) == 0 {
		params.ToolChoice = anthropic.BetaToolChoiceUnionParam{}
	}
	msg, err := m.client.Beta.Messages.New(ctx, params)
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return Response{}, providerError(ctx, err, apiErr.StatusCode, apiErr.Message)
		}
		return Response{}, providerError(ctx, err, 0, "")
	}
	out := Response{
		Message: Message{Role: RoleAssistant, raw: msg.ToParam()},
		Usage:   Usage{InputTokens: int(msg.Usage.InputTokens), OutputTokens: int(msg.Usage.OutputTokens)},
	}
	var text []string
	for _, b := range msg.Content {
		switch b.Type {
		case "text":
			text = append(text, b.Text)
		case "tool_use":
			out.Message.ToolCalls = append(out.Message.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Input: b.Input})
		}
	}
	out.Message.Text = strings.Join(text, "\n")
	switch msg.StopReason {
	case anthropic.BetaStopReasonToolUse:
		out.Stop = StopToolUse
	case anthropic.BetaStopReasonMaxTokens:
		out.Stop = StopMaxTokens
	case anthropic.BetaStopReasonRefusal:
		out.Stop = StopRefusal
	default:
		out.Stop = StopEnd
	}
	return out, nil
}

// messages converts the conversation. Assistant turns that came from this
// client are replayed unchanged, thinking blocks included.
func (m *anthropicModel) messages(in []Message) []anthropic.BetaMessageParam {
	out := make([]anthropic.BetaMessageParam, 0, len(in))
	for _, msg := range in {
		if raw, ok := msg.raw.(anthropic.BetaMessageParam); ok {
			out = append(out, raw)
			continue
		}
		var blocks []anthropic.BetaContentBlockParamUnion
		for _, r := range msg.ToolResults {
			blocks = append(blocks, anthropic.NewBetaToolResultBlock(r.CallID, r.Content, r.IsError))
		}
		if msg.Text != "" {
			blocks = append(blocks, anthropic.NewBetaTextBlock(msg.Text))
		}
		for _, c := range msg.ToolCalls {
			blocks = append(blocks, anthropic.NewBetaToolUseBlock(c.ID, c.Input, c.Name))
		}
		if msg.Role == RoleAssistant {
			out = append(out, anthropic.BetaMessageParam{OfAssistant: &anthropic.BetaAssistantMessageParam{Content: blocks}})
		} else {
			out = append(out, anthropic.NewBetaUserMessage(blocks...))
		}
	}
	return out
}
