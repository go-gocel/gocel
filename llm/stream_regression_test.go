package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// sseServer streams the given lines as an SSE body and records the request
// payload of the first call.
type sseServer struct {
	mu      sync.Mutex
	body    string
	lines   []string
	status  int
	handler func(w http.ResponseWriter, r *http.Request)
}

func newSSEServer(lines []string) (*sseServer, *httptest.Server) {
	s := &sseServer{lines: lines, status: http.StatusOK}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.body = string(b)
		s.mu.Unlock()
		if s.handler != nil {
			s.handler(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(s.status)
		for _, l := range s.lines {
			io.WriteString(w, l+"\n\n")
		}
	}))
	return s, srv
}

func (s *sseServer) requestBody() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.body
}

// drain reads every message until EOF, returning them plus whether any
// carried usage in Meta.
func drain(t *testing.T, r kernel.StreamReader) ([]*types.Message, bool) {
	t.Helper()
	var msgs []*types.Message
	usageSeen := false
	for {
		m, err := r.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		msgs = append(msgs, m)
		if m != nil && m.Meta != nil {
			if _, ok := m.Meta["usage"]; ok {
				usageSeen = true
			}
		}
	}
	return msgs, usageSeen
}

func sseUsageChunk() string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{},
		"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30},
	})
	return "data: " + string(b)
}

// TestOpenAIStream_RequestsUsageAndDeliversFinalUsage (C1): stream mode
// must request stream_options.include_usage and the final usage-only chunk
// must reach the consumer instead of being dropped.
func TestOpenAIStream_RequestsUsageAndDeliversFinalUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(b, &req); err != nil {
			t.Errorf("request is not JSON: %v", err)
		}
		so, ok := req["stream_options"].(map[string]any)
		if !ok || so["include_usage"] != true {
			t.Errorf("request stream_options = %v, want include_usage:true", req["stream_options"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		io.WriteString(w, sseUsageChunk()+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	m := NewOpenAIModel("m", "k", &OpenAIConfig{BaseURL: srv.URL, HTTPClient: srv.Client()})
	r, err := m.Stream(context.Background(), []*types.Message{types.NewUserMessage("x")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	msgs, usageSeen := drain(t, r)
	if !usageSeen {
		t.Fatalf("final usage lost; messages = %+v", msgs)
	}
}

func intPtr(n int) *int          { return &n }
func stringPtr(s string) *string { return &s }

// TestOpenAIStream_SparseToolCallIndices (C2): parallel tool calls chunked
// with non-contiguous indices must all survive, sorted by index.
func TestOpenAIStream_SparseToolCallIndices(t *testing.T) {
	write := func(w io.Writer, v any) {
		b, _ := json.Marshal(v)
		io.WriteString(w, "data: "+string(b)+"\n\n")
	}
	tc := func(idx int, id, name, args string) openaiToolCall {
		return openaiToolCall{
			Index:    intPtr(idx),
			ID:       id,
			Type:     "function",
			Function: openaiToolCallFunction{Name: name, Arguments: args},
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		write(w, openaiStreamChunk{Choices: []openaiStreamChoice{{Delta: openaiMessage{ToolCalls: []openaiToolCall{tc(1, "c1", "b", `{"x":"`)}}}}})
		write(w, openaiStreamChunk{Choices: []openaiStreamChoice{{Delta: openaiMessage{ToolCalls: []openaiToolCall{tc(1, "", "", `1"}`)}}}}})
		write(w, openaiStreamChunk{Choices: []openaiStreamChoice{{Delta: openaiMessage{ToolCalls: []openaiToolCall{tc(0, "c0", "a", `{}`)}}}}})
		write(w, openaiStreamChunk{Choices: []openaiStreamChoice{{FinishReason: stringPtr("tool_calls")}}})
	}))
	defer srv.Close()

	m := NewOpenAIModel("m", "k", &OpenAIConfig{BaseURL: srv.URL, HTTPClient: srv.Client()})
	r, err := m.Stream(context.Background(), []*types.Message{types.NewUserMessage("x")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	msgs, _ := drain(t, r)
	var toolCalls []types.ToolCall
	for _, m := range msgs {
		toolCalls = append(toolCalls, m.ToolCalls...)
	}
	if len(toolCalls) != 2 {
		t.Fatalf("tool calls = %+v, want 2 (sparse indices must not be dropped)", toolCalls)
	}
	if toolCalls[0].Function.Name != "a" || toolCalls[1].Function.Name != "b" {
		t.Fatalf("tool call order = %s, %s; want a then b (sorted by index)", toolCalls[0].Function.Name, toolCalls[1].Function.Name)
	}
	if toolCalls[1].Function.Arguments != `{"x":"1"}` {
		t.Fatalf("chunked arguments = %q, want concatenated", toolCalls[1].Function.Arguments)
	}
}

// TestDeepSeekStream_SortedToolCalls (C2): the deepseek reader used map
// iteration order — tool calls must come out index-sorted.
func TestDeepSeekStream_SortedToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"b\",\"arguments\":\"{}\"}}]}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c0\",\"type\":\"function\",\"function\":{\"name\":\"a\",\"arguments\":\"{}\"}}]}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"finish_reason\":\"tool_calls\"}]}\n\n")
	}))
	defer srv.Close()

	m := NewDeepSeekModel("m", "k", &DeepSeekConfig{BaseURL: srv.URL, HTTPClient: srv.Client()})
	r, err := m.Stream(context.Background(), []*types.Message{types.NewUserMessage("x")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	msgs, _ := drain(t, r)
	var names []string
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			names = append(names, tc.Function.Name)
		}
	}
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("tool call order = %v, want [a b]", names)
	}
}

// TestMoonshotStream_DeliversFinalUsage (C1): moonshot must request
// include_usage and deliver the final usage-only chunk.
func TestMoonshotStream_DeliversFinalUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(b, &req)
		so, _ := req["stream_options"].(map[string]any)
		if so["include_usage"] != true {
			t.Errorf("moonshot request stream_options = %v, want include_usage:true", so)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		io.WriteString(w, sseUsageChunk()+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	m := NewMoonshotModel("m", "k", &MoonshotConfig{BaseURL: srv.URL, HTTPClient: srv.Client()})
	r, err := m.Stream(context.Background(), []*types.Message{types.NewUserMessage("x")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	_, usageSeen := drain(t, r)
	if !usageSeen {
		t.Fatal("moonshot final usage lost")
	}
}

// TestAnthropicGenerate_NilUsage (C3): a response without usage must not
// panic — the usage return is simply nil.
func TestAnthropicGenerate_NilUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"m","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":null}`)
	}))
	defer srv.Close()

	m := NewAnthropicModel("m", "k", &AnthropicConfig{BaseURL: srv.URL, HTTPClient: srv.Client()})
	msg, usage, err := m.Generate(context.Background(), []*types.Message{types.NewUserMessage("x")})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if msg == nil || msg.Content != "ok" {
		t.Fatalf("msg = %+v, want ok", msg)
	}
	if usage != nil {
		t.Fatalf("usage = %+v, want nil (absent usage must not panic)", usage)
	}
}

// TestBaidu_DirectAccessTokenMode (C4): an access-token-only model must use
// the token as-is — never fall into the AK/SK refresh path.
func TestBaidu_DirectAccessTokenMode(t *testing.T) {
	m := &BaiduModel{accessToken: "direct-tok", directToken: true, baseURL: "https://aip.baidubce.com"}
	tok, err := m.getAccessToken(context.Background())
	if err != nil {
		t.Fatalf("direct token mode must not require AK/SK: %v", err)
	}
	if tok != "direct-tok" {
		t.Fatalf("token = %q, want direct-tok", tok)
	}
}
