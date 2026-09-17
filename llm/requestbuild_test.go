package llm

import (
	"context"
	"testing"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

func genCfg() *kernel.GenConfig {
	c := &kernel.GenConfig{}
	c.Apply([]kernel.GenOption{
		kernel.WithTemperature(0.7),
		kernel.WithMaxTokens(512),
		kernel.WithTopP(0.9),
	})
	return c
}

// TestGeminiBuildRequest_SystemInstructionAndGenConfig (D1): system
// messages fold into the system instruction; generation options land in
// GenerationConfig.
func TestGeminiBuildRequest_SystemInstructionAndGenConfig(t *testing.T) {
	m := NewGeminiModel("gemini-2.0-flash", "k", nil)
	cfg := genCfg()
	req := m.buildRequest([]*types.Message{
		types.NewSystemMessage("be brief"),
		types.NewUserMessage("hi"),
	}, cfg, false)
	if req.SystemInstruction == nil || req.SystemInstruction.Parts[0].Text != "be brief" {
		t.Fatalf("system instruction = %+v, want the system message", req.SystemInstruction)
	}
	if len(req.Contents) != 1 {
		t.Fatalf("contents = %d, want the user message only", len(req.Contents))
	}
	if req.GenerationConfig.Temperature != 0.7 || req.GenerationConfig.MaxOutputTokens != 512 || req.GenerationConfig.TopP != 0.9 {
		t.Fatalf("generation config = %+v", req.GenerationConfig)
	}
}

// TestHunyuanBuildRequest_OptionsAndSeed (D1): enhancement flags, seed
// meta and tools map into the request.
func TestHunyuanBuildRequest_OptionsAndSeed(t *testing.T) {
	m := NewHunyuanModel("hunyuan-turbo", "k", &HunyuanConfig{EnableEnhancement: true})
	cfg := genCfg()
	cfg.Meta = map[string]any{"seed": 42}
	cfg.Tools = []*kernel.ToolInfo{{Name: "t", Description: "d", Parameters: map[string]any{"type": "object"}}}
	req := m.buildRequest([]*types.Message{types.NewUserMessage("hi")}, cfg, true)
	if !req.EnableEnhancement || req.Seed != 42 || req.Stream != true || req.MaxTokens != 512 {
		t.Fatalf("request = %+v", req)
	}
	if len(req.Tools) != 1 || req.Tools[0].Function.Name != "t" {
		t.Fatalf("tools = %+v", req.Tools)
	}
}

// TestQwenBuildRequest_MetaAndTools (D1): qwen-specific meta flags and
// tools reach the request.
func TestQwenBuildRequest_MetaAndTools(t *testing.T) {
	m := NewQwenModel("qwen-plus", "k", nil)
	cfg := genCfg()
	cfg.Meta = map[string]any{"enable_search": true}
	cfg.Tools = []*kernel.ToolInfo{{Name: "t", Description: "d", Parameters: nil}}
	req := m.buildRequest([]*types.Message{types.NewUserMessage("hi")}, cfg, false)
	if !req.EnableSearch || req.Model != "qwen-plus" {
		t.Fatalf("request = %+v", req)
	}
	if len(req.Tools) != 1 {
		t.Fatalf("tools = %+v", req.Tools)
	}
}

// TestOllamaBuildRequest_OptionsMap (D1): generation options land in the
// provider's options map under their Ollama names.
func TestOllamaBuildRequest_OptionsMap(t *testing.T) {
	m := NewOllamaModel("llama3", nil)
	req := m.buildRequest([]*types.Message{types.NewUserMessage("hi")}, genCfg(), true)
	if num, ok := req.Options["num_predict"].(int); !ok || num != 512 {
		t.Fatalf("num_predict = %v, want 512", req.Options["num_predict"])
	}
	if !closeTo(req.Options["temperature"], 0.7) || !closeTo(req.Options["top_p"], 0.9) {
		t.Fatalf("options = %+v", req.Options)
	}
	if req.Stream != true || req.Model != "llama3" {
		t.Fatalf("request = %+v", req)
	}
}

// closeTo compares a float option stored as any against a target with
// tolerance (float32/float64 representations differ in the last bits).
func closeTo(v any, want float64) bool {
	f, ok := v.(float64)
	if !ok {
		f32, ok32 := v.(float32)
		if !ok32 {
			return false
		}
		f = float64(f32)
	}
	diff := f - want
	if diff < 0 {
		diff = -diff
	}
	return diff < 1e-6
}

// TestDeepSeekBuildRequest_StreamOptions (D1/C1): stream mode requests the
// usage chunk.
func TestDeepSeekBuildRequest_StreamOptions(t *testing.T) {
	m := NewDeepSeekModel("deepseek-chat", "k", nil)
	req := m.buildRequest([]*types.Message{types.NewUserMessage("hi")}, &kernel.GenConfig{}, true)
	if req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
		t.Fatalf("stream_options = %+v, want include_usage", req.StreamOptions)
	}
}

// TestOpenAIBuildRequest_NonStreamOmitsStreamOptions (D1): the
// include_usage request is a stream-mode concern only.
func TestOpenAIBuildRequest_NonStreamOmitsStreamOptions(t *testing.T) {
	m := NewOpenAIModel("gpt-4o", "k", nil)
	req := m.buildRequest([]*types.Message{types.NewUserMessage("hi")}, &kernel.GenConfig{}, false)
	if req.StreamOptions != nil {
		t.Fatalf("non-stream request must not carry stream_options: %+v", req.StreamOptions)
	}
	_ = context.Background()
}
