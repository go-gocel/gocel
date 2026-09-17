package observability

import (
	"time"

	"github.com/go-gocel/gocel/core/types"
)

// ObservabilityConfig holds all configuration for the observability module.
// ObservabilityConfig 保存可观测性模块的全部配置。
type ObservabilityConfig struct {
	// ServiceName identifies the service in traces and metrics (default "gocel").
	ServiceName string

	// Environment identifies the deployment environment (e.g. "dev", "prod").
	Environment string

	// ExporterType selects the output backend.
	ExporterType ExporterType

	// OTLPEndpoint is the gRPC/HTTP collector address (e.g. "localhost:4318").
	// Only used when ExporterType is ExporterOTLP.
	OTLPEndpoint string

	// TraceSampleRate controls trace sampling (0.0–1.0). Default 1.0 (sample all).
	TraceSampleRate float64

	// CollectMetrics enables metric collection. Default true.
	CollectMetrics bool

	// CollectTraces enables trace collection. Default true.
	CollectTraces bool

	// MetricsExportInterval controls how often metrics are exported. Default 10s.
	MetricsExportInterval time.Duration

	// Attributes are key-value pairs attached to every span and metric.
	Attributes map[string]string

	// OutputDir is the directory for file exporter output. Required when ExporterType is ExporterFile.
	OutputDir string
}

// ExporterType defines the observability data export backend.
// ExporterType 定义可观测性数据的导出后端。
type ExporterType int

const (
	// ExporterNoop disables all observability output.
	// ExporterNoop 禁用所有可观测性输出。
	ExporterNoop ExporterType = iota
	// ExporterStdout writes traces/metrics as JSON to stdout (development).
	// ExporterStdout 将追踪/指标以 JSON 形式写入标准输出（开发用途）。
	ExporterStdout
	// ExporterOTLP exports via OTLP protocol to a collector.
	// ExporterOTLP 通过 OTLP 协议导出到采集器。
	ExporterOTLP
	// ExporterFile writes traces/metrics as JSON Lines to files in OutputDir.
	// ExporterFile 将追踪/指标以 JSON Lines 写入 OutputDir 中的文件。
	ExporterFile
)

// DefaultConfig returns a sensible default configuration.
// DefaultConfig 返回一份合理的默认配置。
func DefaultConfig() ObservabilityConfig {
	return ObservabilityConfig{
		ServiceName:           "gocel",
		Environment:           "development",
		ExporterType:          ExporterStdout,
		TraceSampleRate:       1.0,
		CollectMetrics:        true,
		CollectTraces:         true,
		MetricsExportInterval: 10 * time.Second,
		Attributes:            nil,
	}
}

// ObservabilityOption configures the module at construction time.
// ObservabilityOption 在构造时配置模块。
type ObservabilityOption func(*ObservabilityModule)

// WithServiceName sets the OpenTelemetry service name.
// WithServiceName 设置 OpenTelemetry 服务名。
func WithServiceName(name string) ObservabilityOption {
	return func(m *ObservabilityModule) {
		m.cfg.ServiceName = name
	}
}

// WithEnvironment sets the deployment environment label.
// WithEnvironment 设置部署环境标签。
func WithEnvironment(env string) ObservabilityOption {
	return func(m *ObservabilityModule) {
		m.cfg.Environment = env
	}
}

// WithExporter selects the export backend.
// WithExporter 选择导出后端。
func WithExporter(t ExporterType) ObservabilityOption {
	return func(m *ObservabilityModule) {
		m.cfg.ExporterType = t
	}
}

// WithStdoutExporter configures JSON-to-stdout export (development).
// WithStdoutExporter 配置 JSON 到标准输出的导出（开发用途）。
func WithStdoutExporter() ObservabilityOption {
	return func(m *ObservabilityModule) {
		m.cfg.ExporterType = ExporterStdout
	}
}

// WithNoopExporter disables all observability output.
// WithNoopExporter 禁用所有可观测性输出。
func WithNoopExporter() ObservabilityOption {
	return func(m *ObservabilityModule) {
		m.cfg.ExporterType = ExporterNoop
	}
}

// WithOTLPEndpoint sets the OTLP collector address.
// WithOTLPEndpoint 设置 OTLP 采集器地址。
func WithOTLPEndpoint(endpoint string) ObservabilityOption {
	return func(m *ObservabilityModule) {
		m.cfg.ExporterType = ExporterOTLP
		m.cfg.OTLPEndpoint = endpoint
	}
}

// WithFileExporter configures JSON Lines export to files in the given directory.
// Traces go to traces.jsonl, metrics to metrics.jsonl.
//
// WithFileExporter 配置 JSON Lines 导出到指定目录的文件：追踪写入
// traces.jsonl，指标写入 metrics.jsonl。
func WithFileExporter(dir string) ObservabilityOption {
	return func(m *ObservabilityModule) {
		m.cfg.ExporterType = ExporterFile
		m.cfg.OutputDir = dir
	}
}

// WithTraceSampleRate sets the trace sampling rate (0.0–1.0).
// WithTraceSampleRate 设置追踪采样率（0.0–1.0）。
func WithTraceSampleRate(rate float64) ObservabilityOption {
	return func(m *ObservabilityModule) {
		if rate < 0 {
			rate = 0
		}
		if rate > 1 {
			rate = 1
		}
		m.cfg.TraceSampleRate = rate
	}
}

// WithCollectMetrics enables or disables metric collection.
// WithCollectMetrics 启用或禁用指标采集。
func WithCollectMetrics(enabled bool) ObservabilityOption {
	return func(m *ObservabilityModule) {
		m.cfg.CollectMetrics = enabled
	}
}

// WithCollectTraces enables or disables trace collection.
// WithCollectTraces 启用或禁用追踪采集。
func WithCollectTraces(enabled bool) ObservabilityOption {
	return func(m *ObservabilityModule) {
		m.cfg.CollectTraces = enabled
	}
}

// WithAttribute adds a single attribute attached to all spans and metrics.
// WithAttribute 添加一个附加到所有 span 与指标的属性。
func WithAttribute(key, value string) ObservabilityOption {
	return func(m *ObservabilityModule) {
		if m.cfg.Attributes == nil {
			m.cfg.Attributes = make(map[string]string)
		}
		m.cfg.Attributes[key] = value
	}
}

// tokenUsageFromMessages sums token usage across messages.
func tokenUsageFromMessages(msgs []*types.Message) *types.TokenUsage {
	var usage types.TokenUsage
	// Rough estimation from message content if actual usage isn't available
	for _, msg := range msgs {
		if msg == nil {
			continue
		}
		est := len(msg.Content) / 4
		if est <= 0 {
			est = 1
		}
		usage.PromptTokens += est
	}
	return &usage
}
