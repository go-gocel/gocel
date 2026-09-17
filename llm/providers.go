// Package llm provides built-in LLM provider implementations.
//
// Each provider implements the kernel.ChatModel interface.
// Supported: DeepSeek, OpenAI, Anthropic, Gemini, Ollama, Qwen, Moonshot,
// Hunyuan, Baidu, plus the OpenAI-compatible family (Zhipu, Volc/Doubao,
// Yi, Stepfun, MiniMax, SiliconFlow, Infinity) and the DeepSeek-Anthropic
// endpoint. Use NewXxxModel constructors to create instances. All
// constructors accept provider-specific config options.
//
// LLM 提供者实现包。每个提供者实现 kernel.ChatModel 接口。
// 支持 DeepSeek、OpenAI、Anthropic、Gemini、Ollama、Qwen、Moonshot、
// Hunyuan、Baidu，以及 OpenAI 兼容家族（智谱、火山/豆包、零一、
// Stepfun、MiniMax、硅基流动、Infinity）与 DeepSeek-Anthropic 端点。
// 使用 NewXxxModel 构造函数创建实例，支持提供者专属配置。
package llm

// This file provides convenience constructors for common OpenAI-compatible providers.
//
// For any provider not listed here, use the universal adapter:
//
//	llm.NewOpenAICompatibleModel("model-name", "api-key", "https://base.url/v1")

// NewZhipuModel creates a Zhipu AI (GLM) model.
// Base URL: https://open.bigmodel.cn/api/paas/v4
// Models: glm-5, glm-5-plus, glm-5-air, glm-5-flash, glm-4-plus, glm-4-air, glm-4-flash
//
// NewZhipuModel 创建一个智谱 AI（GLM）模型。
func NewZhipuModel(model, apiKey string, cfg *OpenAIConfig) *OpenAIModel {
	if cfg == nil {
		cfg = &OpenAIConfig{}
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://open.bigmodel.cn/api/paas/v4"
	}
	return NewOpenAIModel(model, apiKey, cfg)
}

// NewVolcModel creates a ByteDance Volcano Engine (Doubao) model.
// Base URL: https://ark.cn-beijing.volces.com/api/v3
// Models: doubao-pro, doubao-lite, doubao-1.5-pro, etc.
//
// NewVolcModel 创建一个字节跳动火山引擎（豆包）模型。
func NewVolcModel(model, apiKey string, cfg *OpenAIConfig) *OpenAIModel {
	if cfg == nil {
		cfg = &OpenAIConfig{}
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://ark.cn-beijing.volces.com/api/v3"
	}
	return NewOpenAIModel(model, apiKey, cfg)
}

// NewYiModel creates a 01.AI Yi model.
// Base URL: https://api.01.ai/v1
// Models: yi-large, yi-medium, yi-vision, yi-lightning, yi-large-turbo, yi-large-preview, yi-large-rag
//
// NewYiModel 创建一个 01.AI 零一万物 Yi 模型。
func NewYiModel(model, apiKey string, cfg *OpenAIConfig) *OpenAIModel {
	if cfg == nil {
		cfg = &OpenAIConfig{}
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.01.ai/v1"
	}
	return NewOpenAIModel(model, apiKey, cfg)
}

// NewStepfunModel creates a Stepfun (Step) model.
// Base URL: https://api.stepfun.com/v1
// Models: step-2-16k, step-1-8k, step-1-32k, step-1-128k, step-1-256k, step-1-flash
//
// NewStepfunModel 创建一个阶跃星辰（Stepfun/Step）模型。
func NewStepfunModel(model, apiKey string, cfg *OpenAIConfig) *OpenAIModel {
	if cfg == nil {
		cfg = &OpenAIConfig{}
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.stepfun.com/v1"
	}
	return NewOpenAIModel(model, apiKey, cfg)
}

// NewMiniMaxModel creates a MiniMax model.
// Base URL: https://api.minimax.chat/v1
// Models: MiniMax-M2.7, MiniMax-T1, abab7, abab6.5, abab5.5
//
// NewMiniMaxModel 创建一个 MiniMax 模型。
func NewMiniMaxModel(model, apiKey string, cfg *OpenAIConfig) *OpenAIModel {
	if cfg == nil {
		cfg = &OpenAIConfig{}
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.minimax.chat/v1"
	}
	return NewOpenAIModel(model, apiKey, cfg)
}

// NewSiliconFlowModel creates a SiliconFlow model.
// Base URL: https://api.siliconflow.cn/v1
// Supports many open-source models: Qwen, DeepSeek, GLM, Yi, Llama, Mistral, etc.
//
// NewSiliconFlowModel 创建一个硅基流动（SiliconFlow）模型。
func NewSiliconFlowModel(model, apiKey string, cfg *OpenAIConfig) *OpenAIModel {
	if cfg == nil {
		cfg = &OpenAIConfig{}
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.siliconflow.cn/v1"
	}
	return NewOpenAIModel(model, apiKey, cfg)
}

// NewInfinityModel creates an Infinity API model.
// Base URL: https://api.infinity.ai/v1
//
// NewInfinityModel 创建一个 Infinity API 模型。
func NewInfinityModel(model, apiKey string, cfg *OpenAIConfig) *OpenAIModel {
	if cfg == nil {
		cfg = &OpenAIConfig{}
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.infinity.ai/v1"
	}
	return NewOpenAIModel(model, apiKey, cfg)
}

// NewDeepSeekAnthropicModel creates a DeepSeek model using the Anthropic-compatible API.
// Base URL: https://api.deepseek.com/anthropic
// This uses the Anthropic Claude API format.
//
// NewDeepSeekAnthropicModel 创建一个使用 Anthropic 兼容 API 的 DeepSeek 模型。
func NewDeepSeekAnthropicModel(model, apiKey string, cfg *AnthropicConfig) *AnthropicModel {
	if cfg == nil {
		cfg = &AnthropicConfig{}
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.deepseek.com/anthropic"
	}
	return NewAnthropicModel(model, apiKey, cfg)
}
