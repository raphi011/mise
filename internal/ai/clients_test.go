package ai_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/raphi011/mise/internal/ai"
)

// replay serves recorded responses in order and keeps the requests.
type replay struct {
	t         *testing.T
	mu        sync.Mutex
	responses []recorded
	requests  []replayed
}

type recorded struct {
	status int
	body   string
}

type replayed struct {
	path   string
	header http.Header
	body   map[string]any
}

func newReplay(t *testing.T, responses ...recorded) (*replay, *httptest.Server) {
	t.Helper()
	r := &replay{t: t, responses: responses}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		raw, _ := io.ReadAll(req.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body: %v\n%s", err, raw)
		}
		r.requests = append(r.requests, replayed{path: req.URL.Path, header: req.Header.Clone(), body: body})
		if len(r.responses) == 0 {
			t.Errorf("unexpected request %s", req.URL.Path)
			http.Error(w, "no more responses", http.StatusTeapot)
			return
		}
		res := r.responses[0]
		r.responses = r.responses[1:]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(res.status)
		_, _ = io.WriteString(w, res.body)
	}))
	t.Cleanup(srv.Close)
	return r, srv
}

// get walks a decoded JSON value by keys and indexes.
func get(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			a, _ := v.([]any)
			if k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}

var lookupTool = ai.Tool{
	Name:        "search_items",
	Description: "Look up grocery items.",
	Properties:  map[string]any{"query": map[string]any{"type": "string"}},
	Required:    []string{"query"},
}

// Responses in the shape the Messages API returns them.
const (
	anthropicToolTurn = `{"id":"msg_01","type":"message","role":"assistant","model":"claude-opus-5-5",
 "content":[
  {"type":"thinking","thinking":"","signature":"sig-abc"},
  {"type":"text","text":"Looking up tofu."},
  {"type":"tool_use","id":"toolu_01","name":"search_items","input":{"query":"tofu"}}],
 "stop_reason":"tool_use","stop_sequence":null,"stop_details":null,
 "usage":{"input_tokens":812,"output_tokens":64}}`
	anthropicEndTurn = `{"id":"msg_02","type":"message","role":"assistant","model":"claude-opus-5-5",
 "content":[{"type":"text","text":"Done."}],
 "stop_reason":"end_turn","stop_sequence":null,"stop_details":null,
 "usage":{"input_tokens":900,"output_tokens":5}}`
	anthropicRefusal = `{"id":"msg_03","type":"message","role":"assistant","model":"claude-opus-5-5",
 "content":[],"stop_reason":"refusal","stop_sequence":null,
 "stop_details":{"type":"refusal","category":"cyber","explanation":"Declined."},
 "usage":{"input_tokens":10,"output_tokens":0}}`
	anthropicAuthError = `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`
)

func TestAnthropicClient(t *testing.T) {
	// The server's environment must not leak into a household's requests.
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-from-env")
	t.Setenv("ANTHROPIC_BASE_URL", "http://env.invalid")
	rp, srv := newReplay(t,
		recorded{200, anthropicToolTurn}, recorded{200, anthropicEndTurn},
		recorded{200, anthropicRefusal}, recorded{401, anthropicAuthError})
	m, err := ai.Clients{AnthropicURL: srv.URL}.New(ai.Connection{Provider: ai.ProviderAnthropic, Model: "claude-opus-5-5", APIKey: "sk-ant-household"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	req := ai.Request{System: "Import recipes.", Messages: []ai.Message{{Role: ai.RoleUser, Text: "Page text"}},
		Tools: []ai.Tool{lookupTool}, MaxTokens: 16000}
	res, err := m.Chat(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != ai.StopToolUse || res.Message.Text != "Looking up tofu." || res.Usage != (ai.Usage{InputTokens: 812, OutputTokens: 64}) {
		t.Errorf("response %+v", res)
	}
	if len(res.Message.ToolCalls) != 1 || res.Message.ToolCalls[0].ID != "toolu_01" || res.Message.ToolCalls[0].Name != "search_items" ||
		string(res.Message.ToolCalls[0].Input) != `{"query":"tofu"}` {
		t.Errorf("tool calls %+v", res.Message.ToolCalls)
	}

	req.Messages = append(req.Messages, res.Message, ai.Message{Role: ai.RoleUser,
		ToolResults: []ai.ToolResult{{CallID: "toolu_01", Content: `[{"id":"1","name":"Tofu"}]`}}})
	res, err = m.Chat(ctx, req)
	if err != nil || res.Stop != ai.StopEnd || res.Message.Text != "Done." {
		t.Errorf("second turn %+v, %v", res, err)
	}
	if res, err := m.Chat(ctx, req); err != nil || res.Stop != ai.StopRefusal {
		t.Errorf("refusal %+v, %v", res, err)
	}
	_, err = m.Chat(ctx, req)
	var pe *ai.ProviderError
	if !errors.As(err, &pe) || pe.Status != 401 || pe.Message != "invalid x-api-key" {
		t.Errorf("auth error %v", err)
	}

	first := rp.requests[0]
	if first.path != "/v1/messages" || first.header.Get("Authorization") != "Bearer sk-ant-household" {
		t.Errorf("request to %s with auth %q", first.path, first.header.Get("Authorization"))
	}
	if beta := first.header.Get("Anthropic-Beta"); !strings.Contains(beta, "server-side-fallback-2026-07-01") {
		t.Errorf("beta header %q", beta)
	}
	b := first.body
	for name, want := range map[string]any{
		"model":       "claude-opus-5-5",
		"max_tokens":  float64(16000),
		"fallbacks":   "default",
		"thinking":    map[string]any{"type": "adaptive"},
		"tool_choice": map[string]any{"type": "auto"},
	} {
		if got, _ := json.Marshal(b[name]); string(got) != mustJSON(want) {
			t.Errorf("%s = %s, want %s", name, got, mustJSON(want))
		}
	}
	if get(b, "system", 0, "text") != "Import recipes." || get(b, "messages", 0, "content", 0, "text") != "Page text" {
		t.Errorf("system or user turn: %v", b)
	}
	tool := get(b, "tools", 0)
	if get(tool, "strict") != true || get(tool, "input_schema", "additionalProperties") != false ||
		get(tool, "input_schema", "required", 0) != "query" {
		t.Errorf("tool %v", tool)
	}
	// The assistant turn is replayed with its thinking block, then the
	// tool result.
	second := rp.requests[1].body
	if get(second, "messages", 1, "content", 0, "type") != "thinking" || get(second, "messages", 1, "content", 0, "signature") != "sig-abc" ||
		get(second, "messages", 1, "content", 2, "id") != "toolu_01" {
		t.Errorf("assistant replay %v", get(second, "messages", 1))
	}
	if get(second, "messages", 2, "content", 0, "type") != "tool_result" || get(second, "messages", 2, "content", 0, "tool_use_id") != "toolu_01" {
		t.Errorf("tool result %v", get(second, "messages", 2))
	}
}

// Responses in the shape chat completions return them.
const (
	openAIToolTurn = `{"id":"chatcmpl-1","object":"chat.completion","created":1762700000,"model":"gpt-x",
 "choices":[{"index":0,"finish_reason":"tool_calls","logprobs":null,"message":{"role":"assistant","content":null,"refusal":null,
  "tool_calls":[{"id":"call_1","type":"function","function":{"name":"search_items","arguments":"{\"query\":\"tofu\"}"}}]}}],
 "usage":{"prompt_tokens":700,"completion_tokens":20,"total_tokens":720}}`
	openAIEndTurn = `{"id":"chatcmpl-2","object":"chat.completion","created":1762700001,"model":"gpt-x",
 "choices":[{"index":0,"finish_reason":"stop","logprobs":null,"message":{"role":"assistant","content":"Done.","refusal":null}}],
 "usage":{"prompt_tokens":750,"completion_tokens":2,"total_tokens":752}}`
	openAIRefusal = `{"id":"chatcmpl-3","object":"chat.completion","created":1762700002,"model":"gpt-x",
 "choices":[{"index":0,"finish_reason":"stop","logprobs":null,"message":{"role":"assistant","content":null,"refusal":"I can't help with that."}}],
 "usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	// Ollama answers a model without tool support like this.
	ollamaNoTools = `{"error":{"message":"registry.ollama.ai/library/gemma:2b does not support tools","type":"api_error","param":null,"code":null}}`
)

func TestOpenAIClient(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-from-env")
	t.Setenv("OPENAI_ORG_ID", "org-from-env")
	rp, srv := newReplay(t, recorded{200, openAIToolTurn}, recorded{200, openAIEndTurn}, recorded{200, openAIRefusal})
	m, err := ai.Clients{}.New(ai.Connection{Provider: ai.ProviderOpenAI, Endpoint: srv.URL + "/v1", Model: "gpt-x", APIKey: "sk-household"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	req := ai.Request{System: "Import recipes.", Messages: []ai.Message{{Role: ai.RoleUser, Text: "Page text"}},
		Tools: []ai.Tool{lookupTool}, MaxTokens: 16000}
	res, err := m.Chat(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != ai.StopToolUse || len(res.Message.ToolCalls) != 1 || res.Message.ToolCalls[0].ID != "call_1" ||
		string(res.Message.ToolCalls[0].Input) != `{"query":"tofu"}` || res.Usage != (ai.Usage{InputTokens: 700, OutputTokens: 20}) {
		t.Errorf("response %+v", res)
	}
	req.Messages = append(req.Messages, res.Message, ai.Message{Role: ai.RoleUser,
		ToolResults: []ai.ToolResult{{CallID: "call_1", Content: "[]"}}})
	if res, err := m.Chat(ctx, req); err != nil || res.Stop != ai.StopEnd || res.Message.Text != "Done." {
		t.Errorf("second turn %+v, %v", res, err)
	}
	if res, err := m.Chat(ctx, req); err != nil || res.Stop != ai.StopRefusal {
		t.Errorf("refusal %+v, %v", res, err)
	}

	first := rp.requests[0]
	if first.path != "/v1/chat/completions" || first.header.Get("Authorization") != "Bearer sk-household" ||
		first.header.Get("Openai-Organization") != "" {
		t.Errorf("request to %s, auth %q, org %q", first.path, first.header.Get("Authorization"), first.header.Get("Openai-Organization"))
	}
	b := first.body
	if b["model"] != "gpt-x" || b["max_completion_tokens"] != float64(16000) || b["max_tokens"] != nil || b["tool_choice"] != "auto" {
		t.Errorf("request %v", b)
	}
	if get(b, "messages", 0, "role") != "system" || get(b, "messages", 1, "role") != "user" {
		t.Errorf("messages %v", b["messages"])
	}
	fn := get(b, "tools", 0, "function")
	if get(fn, "strict") != true || get(fn, "parameters", "additionalProperties") != false {
		t.Errorf("tool %v", fn)
	}
	second := rp.requests[1].body
	if get(second, "messages", 2, "tool_calls", 0, "id") != "call_1" || get(second, "messages", 3, "role") != "tool" ||
		get(second, "messages", 3, "tool_call_id") != "call_1" {
		t.Errorf("replay %v", second["messages"])
	}
}

func TestOllamaClient(t *testing.T) {
	rp, srv := newReplay(t, recorded{200, openAIEndTurn}, recorded{400, ollamaNoTools})
	m, err := ai.Clients{}.New(ai.Connection{Provider: ai.ProviderOllama, Endpoint: srv.URL + "/v1", Model: "llama"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Test(t.Context()); err != nil {
		t.Fatal(err)
	}
	b := rp.requests[0]
	if b.header.Get("Authorization") != "" || b.body["max_tokens"] != float64(256) || b.body["max_completion_tokens"] != nil ||
		get(b.body, "tools", 0, "function", "name") != "noop" {
		t.Errorf("test request %v, auth %q", b.body, b.header.Get("Authorization"))
	}
	err = m.Test(t.Context())
	var pe *ai.ProviderError
	if !errors.As(err, &pe) || pe.Status != 400 || !strings.Contains(pe.Message, "does not support tools") {
		t.Errorf("model without tools: %v", err)
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
