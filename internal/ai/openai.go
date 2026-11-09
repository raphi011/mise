package ai

import (
	"context"
	"errors"
	"net/http"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

// openAIModel talks to an OpenAI-compatible chat completions endpoint
// (OpenAI itself, other hosted servers, or Ollama).
type openAIModel struct {
	chat  openai.ChatCompletionService
	model string
	// legacyMaxTokens: OpenAI wants max_completion_tokens; Ollama and most
	// other servers read max_tokens.
	legacyMaxTokens bool
}

func newOpenAI(c Connection, hc *http.Client) *openAIModel {
	// The service is built from explicit options only: openai.NewClient
	// would add OPENAI_* variables from the server's environment.
	opts := []option.RequestOption{
		option.WithHTTPClient(hc),
		option.WithBaseURL(c.Endpoint + "/"),
	}
	if c.APIKey != "" {
		opts = append(opts, option.WithAPIKey(c.APIKey))
	}
	return &openAIModel{
		chat:            openai.NewChatCompletionService(opts...),
		model:           c.Model,
		legacyMaxTokens: c.Provider == ProviderOllama,
	}
}

func (m *openAIModel) Test(ctx context.Context) error {
	_, err := m.Chat(ctx, testRequest)
	return err
}

func (m *openAIModel) Chat(ctx context.Context, req Request) (Response, error) {
	params := openai.ChatCompletionNewParams{
		Model:    m.model,
		Messages: m.messages(req),
	}
	if m.legacyMaxTokens {
		params.MaxTokens = openai.Int(int64(req.MaxTokens))
	} else {
		params.MaxCompletionTokens = openai.Int(int64(req.MaxTokens))
	}
	for _, t := range req.Tools {
		params.Tools = append(params.Tools, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        t.Name,
			Description: openai.String(t.Description),
			Parameters:  shared.FunctionParameters(t.schema()),
			Strict:      openai.Bool(true),
		}))
	}
	if len(params.Tools) > 0 {
		params.ToolChoice = openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: openai.String("auto")}
	}
	res, err := m.chat.New(ctx, params)
	if err != nil {
		var apiErr *openai.Error
		if errors.As(err, &apiErr) {
			return Response{}, providerError(ctx, err, apiErr.StatusCode, apiErr.Message)
		}
		return Response{}, providerError(ctx, err, 0, "")
	}
	if len(res.Choices) == 0 {
		return Response{}, &ProviderError{Message: "the response has no choices"}
	}
	choice := res.Choices[0]
	out := Response{
		Message: Message{Role: RoleAssistant, Text: choice.Message.Content, raw: choice.Message.ToParam()},
		Usage:   Usage{InputTokens: int(res.Usage.PromptTokens), OutputTokens: int(res.Usage.CompletionTokens)},
	}
	for _, c := range choice.Message.ToolCalls {
		if c.Type == "function" {
			out.Message.ToolCalls = append(out.Message.ToolCalls, ToolCall{ID: c.ID, Name: c.Function.Name, Input: []byte(c.Function.Arguments)})
		}
	}
	switch {
	case choice.Message.Refusal != "" || choice.FinishReason == "content_filter":
		out.Stop = StopRefusal
	case choice.FinishReason == "length":
		out.Stop = StopMaxTokens
	case len(out.Message.ToolCalls) > 0:
		// Some servers answer tool calls with finish_reason "stop".
		out.Stop = StopToolUse
	default:
		out.Stop = StopEnd
	}
	return out, nil
}

func (m *openAIModel) messages(req Request) []openai.ChatCompletionMessageParamUnion {
	var out []openai.ChatCompletionMessageParamUnion
	if req.System != "" {
		out = append(out, openai.SystemMessage(req.System))
	}
	for _, msg := range req.Messages {
		if raw, ok := msg.raw.(openai.ChatCompletionMessageParamUnion); ok {
			out = append(out, raw)
			continue
		}
		if msg.Role == RoleAssistant {
			a := openai.ChatCompletionAssistantMessageParam{}
			if msg.Text != "" {
				a.Content.OfString = openai.String(msg.Text)
			}
			for _, c := range msg.ToolCalls {
				a.ToolCalls = append(a.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
					ID:       c.ID,
					Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{Name: c.Name, Arguments: string(c.Input)},
				}})
			}
			out = append(out, openai.ChatCompletionMessageParamUnion{OfAssistant: &a})
			continue
		}
		for _, r := range msg.ToolResults {
			content := r.Content
			if r.IsError {
				content = "Error: " + content
			}
			out = append(out, openai.ToolMessage(content, r.CallID))
		}
		if msg.Text != "" {
			out = append(out, openai.UserMessage(msg.Text))
		}
	}
	return out
}
