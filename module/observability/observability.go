// Package observability provides OpenTelemetry-based distributed tracing and metrics
// for gocel agent runs. It integrates via the Hook system without coupling to agent logic.
//
// ObservabilityModule exports distributed traces and metrics via OTLP, stdout, or file exporters.
// It supports service name, attributes, and custom exporter configuration.
//
// 可观测性模块。ObservabilityModule 通过 OpenTelemetry 导出分布式追踪和指标，
// 支持 OTLP、标准输出、文件三种导出方式。可配置服务名和自定义属性。
package observability

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/types"
)

// ObservabilityModule is the OpenTelemetry observability module.
// It registers hooks on AgentRuntime to create distributed spans and record metrics
// for agent runs, model calls, and tool calls.
//
// ObservabilityModule 是 OpenTelemetry 可观测性模块。它在 AgentRuntime 上
// 注册钩子，为 agent 运行、模型调用与工具调用创建分布式 span 并记录指标。
type ObservabilityModule struct {
	cfg         ObservabilityConfig
	mu          sync.Mutex
	initialized bool
	registered  bool // P1: prevents duplicate Register calls
	tp          *sdktrace.TracerProvider
	mp          *sdkmetric.MeterProvider
	tracer      trace.Tracer
	meter       metric.Meter
	closeOnce   sync.Once

	// File handles for ExporterFile (owned by module, closed on Shutdown)
	traceFile  *os.File
	metricFile *os.File

	// Metric instruments (created lazily in initInstruments)
	instrumentOnce        sync.Once
	instrumentErr         error
	stepCounter           metric.Int64Counter
	stepDuration          metric.Float64Histogram
	modelCallCounter      metric.Int64Counter
	modelCallDuration     metric.Float64Histogram
	modelPromptTokens     metric.Int64Histogram
	modelCompletionTokens metric.Int64Histogram
	toolCallCounter       metric.Int64Counter
	toolCallDuration      metric.Float64Histogram
}

// NewObservabilityModule creates an observability module with default config and applies options.
// NewObservabilityModule 以默认配置创建可观测性模块并应用选项。
func NewObservabilityModule(opts ...ObservabilityOption) *ObservabilityModule {
	m := &ObservabilityModule{cfg: DefaultConfig()}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Register registers observability hooks on the given runtime.
// The module's TracerProvider and MeterProvider are lazily initialized on
// first hook invocation using instance-level providers, so multiple
// ObservabilityModule instances never clobber each other's global OTel state.
//
// Register 在给定 runtime 上注册可观测性钩子。模块的 TracerProvider 与
// MeterProvider 在首次钩子调用时用实例级 provider 惰性初始化，因此多个
// ObservabilityModule 实例互不覆盖彼此的全局 OTel 状态。
func (m *ObservabilityModule) Register(rt kernel.HookRegistrar) {
	m.mu.Lock()
	if m.registered {
		m.mu.Unlock()
		return
	}
	m.registered = true
	m.mu.Unlock()

	// Always register MessagesBuilt — it's lightweight and enriches subsequent spans.
	rt.OnMessagesBuilt(m.onMessagesBuilt)

	if m.cfg.CollectTraces {
		rt.OnStepStart(m.onStepStart)
		rt.OnStepEnd(m.onStepEnd)
		rt.OnModelCall(m.onModelCall)
		rt.OnModelResult(m.onModelResult)
		rt.OnToolCall(m.onToolCall)
		rt.OnToolResult(m.onToolResult)
		rt.OnAgentEnd(m.onAgentEnd)
	}

	log.Printf("[observability] Register: traces=%v metrics=%v exporter=%d service=%s",
		m.cfg.CollectTraces, m.cfg.CollectMetrics, m.cfg.ExporterType, m.cfg.ServiceName)
}

// Shutdown cleanly shuts down the tracer and meter providers, flushing remaining data.
// Safe to call multiple times.
//
// Shutdown 干净地关闭 tracer 与 meter provider，刷新剩余数据。可安全地
// 多次调用。
func (m *ObservabilityModule) Shutdown(ctx context.Context) error {
	var errs []error
	m.closeOnce.Do(func() {
		if m.tp != nil {
			if err := m.tp.Shutdown(ctx); err != nil {
				errs = append(errs, fmt.Errorf("tracer provider shutdown: %w", err))
			}
		}
		if m.mp != nil {
			if err := m.mp.Shutdown(ctx); err != nil {
				errs = append(errs, fmt.Errorf("meter provider shutdown: %w", err))
			}
		}
		if m.traceFile != nil {
			m.traceFile.Close()
			m.traceFile = nil
		}
		if m.metricFile != nil {
			m.metricFile.Close()
			m.metricFile = nil
		}
	})
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// lazyInit creates TracerProvider and MeterProvider on first use.
func (m *ObservabilityModule) lazyInit() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.initialized {
		return nil
	}

	// Set global error handler (forward to standard log).
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		log.Printf("[observability] OTel error: %v", err)
	}))

	res, err := m.buildResource()
	if err != nil {
		return err
	}

	// --- TracerProvider ---
	m.tp, err = m.buildTracerProvider(res)
	if err != nil {
		return err
	}

	// --- MeterProvider ---
	m.mp, err = m.buildMeterProvider(res)
	if err != nil {
		return err
	}

	// Use instance-level providers so multiple ObservabilityModule instances
	// never clobber each other's global OTel state (P2).
	m.tracer = m.tp.Tracer(m.cfg.ServiceName)
	if m.cfg.CollectMetrics {
		m.meter = m.mp.Meter(m.cfg.ServiceName)
		// initInstruments had NO call site — every instrument stayed nil
		// and CollectMetrics recorded nothing (verified defect).
		if err := m.initInstruments(); err != nil {
			return err
		}
	}
	m.initialized = true
	return nil
}

// buildResource assembles the OTel resource from the module config.
func (m *ObservabilityModule) buildResource() (*resource.Resource, error) {
	// Build resource attributes
	resAttrs := []attribute.KeyValue{
		semconv.ServiceNameKey.String(m.cfg.ServiceName),
	}
	if m.cfg.Environment != "" {
		resAttrs = append(resAttrs, semconv.DeploymentEnvironmentKey.String(m.cfg.Environment))
	}
	// Attach Baggage members as resource-level attributes (from initial context).
	for k, v := range m.cfg.Attributes {
		resAttrs = append(resAttrs, attribute.String(k, v))
	}
	res, err := resource.New(context.Background(), resource.WithAttributes(resAttrs...))
	if err != nil {
		return nil, fmt.Errorf("create otel resource: %w", err)
	}
	return res, nil
}

// buildTracerProvider assembles the tracer provider for the configured
// exporter (stdout/OTLP/noop/file).
func (m *ObservabilityModule) buildTracerProvider(res *resource.Resource) (*sdktrace.TracerProvider, error) {
	if !m.cfg.CollectTraces {
		return sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample())), nil
	}
	traceOpts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(m.cfg.TraceSampleRate))),
	}

	switch m.cfg.ExporterType {
	case ExporterStdout:
		exp, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			return nil, fmt.Errorf("create stdout trace exporter: %w", err)
		}
		traceOpts = append(traceOpts, sdktrace.WithBatcher(exp))
	case ExporterOTLP:
		exp, err := otlptracehttp.New(context.Background(),
			otlptracehttp.WithEndpoint(m.cfg.OTLPEndpoint),
			otlptracehttp.WithInsecure(),
		)
		if err != nil {
			return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
		}
		traceOpts = append(traceOpts, sdktrace.WithBatcher(exp))
	case ExporterNoop:
		traceOpts = append(traceOpts, sdktrace.WithSampler(sdktrace.NeverSample()))
	case ExporterFile:
		if err := m.initFiles(); err != nil {
			return nil, fmt.Errorf("open observability output files: %w", err)
		}
		exp, err := stdouttrace.New(stdouttrace.WithWriter(m.traceFile))
		if err != nil {
			return nil, fmt.Errorf("create file trace exporter: %w", err)
		}
		traceOpts = append(traceOpts, sdktrace.WithBatcher(exp))
	}

	return sdktrace.NewTracerProvider(traceOpts...), nil
}

// buildMeterProvider assembles the meter provider for the configured
// exporter (stdout/OTLP/noop/file).
func (m *ObservabilityModule) buildMeterProvider(res *resource.Resource) (*sdkmetric.MeterProvider, error) {
	if !m.cfg.CollectMetrics {
		return sdkmetric.NewMeterProvider(), nil
	}
	metricOpts := []sdkmetric.Option{
		sdkmetric.WithResource(res),
	}

	readerOpts := []sdkmetric.PeriodicReaderOption{}
	if m.cfg.MetricsExportInterval > 0 {
		readerOpts = append(readerOpts, sdkmetric.WithInterval(m.cfg.MetricsExportInterval))
	}

	switch m.cfg.ExporterType {
	case ExporterStdout:
		exp, err := stdoutmetric.New()
		if err != nil {
			return nil, fmt.Errorf("create stdout metric exporter: %w", err)
		}
		metricOpts = append(metricOpts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp, readerOpts...)))
	case ExporterOTLP:
		exp, err := otlpmetrichttp.New(context.Background(),
			otlpmetrichttp.WithEndpoint(m.cfg.OTLPEndpoint),
			otlpmetrichttp.WithInsecure(),
		)
		if err != nil {
			return nil, fmt.Errorf("create OTLP metric exporter: %w", err)
		}
		metricOpts = append(metricOpts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp, readerOpts...)))
	case ExporterNoop:
		// No-op MeterProvider is created below
	case ExporterFile:
		if m.metricFile == nil {
			// initFiles wasn't called — traces disabled but metrics enabled. Open files.
			if err := m.initFiles(); err != nil {
				return nil, fmt.Errorf("open observability output files: %w", err)
			}
		}
		exp, err := stdoutmetric.New(stdoutmetric.WithWriter(m.metricFile))
		if err != nil {
			return nil, fmt.Errorf("create file metric exporter: %w", err)
		}
		metricOpts = append(metricOpts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp, readerOpts...)))
	}

	return sdkmetric.NewMeterProvider(metricOpts...), nil
}

// initFiles creates trace.jsonl and metrics.jsonl in OutputDir.
// Called from lazyInit for ExporterFile.
func (m *ObservabilityModule) initFiles() error {
	dir := m.cfg.OutputDir
	if dir == "" {
		return fmt.Errorf("OutputDir is required for ExporterFile")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create output dir %s: %w", dir, err)
	}
	tracePath := filepath.Join(dir, "traces.jsonl")
	f, err := os.Create(tracePath)
	if err != nil {
		return fmt.Errorf("create trace file %s: %w", tracePath, err)
	}
	m.traceFile = f

	metricPath := filepath.Join(dir, "metrics.jsonl")
	f2, err := os.Create(metricPath)
	if err != nil {
		m.traceFile.Close()
		m.traceFile = nil
		return fmt.Errorf("create metric file %s: %w", metricPath, err)
	}
	m.metricFile = f2

	log.Printf("[observability] writing traces to %s, metrics to %s", tracePath, metricPath)
	return nil
}

func (m *ObservabilityModule) initInstruments() error {
	if m.meter == nil {
		return fmt.Errorf("meter not available (CollectMetrics disabled)")
	}
	m.instrumentOnce.Do(func() {
		m.instrumentErr = m.createInstruments()
	})
	return m.instrumentErr
}

func (m *ObservabilityModule) createInstruments() error {
	var err error

	m.stepCounter, err = m.meter.Int64Counter("gocel.steps.total",
		metric.WithDescription("Total number of agent execution steps"),
	)
	if err != nil {
		return fmt.Errorf("create step counter: %w", err)
	}

	m.stepDuration, err = m.meter.Float64Histogram("gocel.step.duration",
		metric.WithDescription("Step execution duration in seconds"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return fmt.Errorf("create step duration histogram: %w", err)
	}

	m.modelCallCounter, err = m.meter.Int64Counter("gocel.model.calls.total",
		metric.WithDescription("Total number of model calls"),
	)
	if err != nil {
		return fmt.Errorf("create model call counter: %w", err)
	}

	m.modelCallDuration, err = m.meter.Float64Histogram("gocel.model.call.duration",
		metric.WithDescription("Model call duration in seconds"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return fmt.Errorf("create model call duration histogram: %w", err)
	}

	m.modelPromptTokens, err = m.meter.Int64Histogram("gocel.model.prompt_tokens",
		metric.WithDescription("Number of prompt tokens per model call"),
	)
	if err != nil {
		return fmt.Errorf("create model prompt tokens histogram: %w", err)
	}

	m.modelCompletionTokens, err = m.meter.Int64Histogram("gocel.model.completion_tokens",
		metric.WithDescription("Number of completion tokens per model call"),
	)
	if err != nil {
		return fmt.Errorf("create model completion tokens histogram: %w", err)
	}

	m.toolCallCounter, err = m.meter.Int64Counter("gocel.tool.calls.total",
		metric.WithDescription("Total number of tool calls"),
	)
	if err != nil {
		return fmt.Errorf("create tool call counter: %w", err)
	}

	m.toolCallDuration, err = m.meter.Float64Histogram("gocel.tool.call.duration",
		metric.WithDescription("Tool call duration in seconds"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return fmt.Errorf("create tool call duration histogram: %w", err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Context keys for span / start-time storage
// ---------------------------------------------------------------------------

type contextKey struct{ name string }

var (
	stepSpanKey   = &contextKey{name: "step"}
	modelSpanKey  = &contextKey{name: "model"}
	toolSpanKey   = &contextKey{name: "tool"}
	stepStartKey  = &contextKey{name: "step.start"}
	modelStartKey = &contextKey{name: "model.start"}
	toolStartKey  = &contextKey{name: "tool.start"}
)

func contextWithValue(ctx context.Context, key *contextKey, val any) context.Context {
	return context.WithValue(ctx, key, val)
}

func contextValue(ctx context.Context, key *contextKey) any {
	return ctx.Value(key)
}

// ---------------------------------------------------------------------------
// mkSpan creates a child span named "gocel.<name>" in the current context.
// Baggage members from the context are attached as span attributes.
// ---------------------------------------------------------------------------

func (m *ObservabilityModule) mkSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	if m.tracer == nil {
		return ctx, trace.SpanFromContext(ctx)
	}
	ctx, span := m.tracer.Start(ctx, "gocel."+name, opts...)

	// Extract baggage from context and attach as span attributes.
	if bag := baggage.FromContext(ctx); bag.Len() > 0 {
		attrs := make([]attribute.KeyValue, 0, bag.Len())
		for _, m := range bag.Members() {
			attrs = append(attrs, attribute.String("baggage."+string(m.Key()), m.Value()))
		}
		span.SetAttributes(attrs...)
	}

	return ctx, span
}

// ---------------------------------------------------------------------------
// recordError sets the span status to Error and records the error as a span event.
// ---------------------------------------------------------------------------

func recordError(span trace.Span, err error) {
	if !span.IsRecording() {
		return
	}
	span.SetStatus(codes.Error, err.Error())
	span.RecordError(err, trace.WithAttributes(
		attribute.String("exception.type", fmt.Sprintf("%T", err)),
	))
}

// ---------------------------------------------------------------------------
// HookAfterMessages — enrich subsequent child spans with message metadata.
// ---------------------------------------------------------------------------

func (m *ObservabilityModule) onMessagesBuilt(ctx context.Context, msgs []*types.Message) (context.Context, []*types.Message, error) {
	_ = msgs // Placeholder — currently used only to keep the hook registered.
	return ctx, msgs, nil
}

// ---------------------------------------------------------------------------
// Step span lifecycle
// ---------------------------------------------------------------------------

func (m *ObservabilityModule) onStepStart(ctx context.Context, stepInfo *kernel.StepInfo) (context.Context, *kernel.StepInfo, error) {
	if err := m.lazyInit(); err != nil {
		log.Printf("[observability] lazyInit error: %v", err)
		return ctx, stepInfo, nil
	}

	ctx, span := m.mkSpan(ctx, "agent.step",
		trace.WithAttributes(
			attribute.String("agent.name", stepInfo.AgentName),
			attribute.Int("step.index", stepInfo.StepIndex),
		),
	)

	ctx = contextWithValue(ctx, stepSpanKey, span)
	ctx = contextWithValue(ctx, stepStartKey, time.Now())

	// Emit span event
	span.AddEvent("step.started",
		trace.WithAttributes(
			attribute.Int("step.index", stepInfo.StepIndex),
		),
	)

	return ctx, stepInfo, nil
}

func (m *ObservabilityModule) onStepEnd(ctx context.Context, stepInfo *kernel.StepInfo) (context.Context, *kernel.StepInfo, error) {
	spanVal := contextValue(ctx, stepSpanKey)
	span, ok := spanVal.(trace.Span)
	if !ok || !span.IsRecording() {
		return ctx, stepInfo, nil
	}

	var agentName string
	var stepContinue bool
	var hasToolCalls bool
	if stepInfo != nil {
		agentName = stepInfo.AgentName
		stepContinue = stepInfo.Continue
		hasToolCalls = stepInfo.HasToolCalls
		span.SetAttributes(
			attribute.Bool("step.continue", stepContinue),
			attribute.Bool("step.has_tool_calls", hasToolCalls),
		)
	}

	if start, ok2 := contextValue(ctx, stepStartKey).(time.Time); ok2 {
		duration := time.Since(start).Seconds()
		span.SetAttributes(attribute.Float64("step.duration_seconds", duration))
		if m.cfg.CollectMetrics && m.stepDuration != nil {
			m.stepDuration.Record(ctx, duration,
				metric.WithAttributeSet(attribute.NewSet(
					attribute.String("agent.name", agentName),
				)),
			)
		}
	}

	span.AddEvent("step.completed")

	if m.cfg.CollectMetrics && m.stepCounter != nil {
		m.stepCounter.Add(ctx, 1,
			metric.WithAttributeSet(attribute.NewSet(
				attribute.String("agent.name", agentName),
				attribute.Bool("step.continue", stepContinue),
			)),
		)
	}

	span.End()
	return ctx, stepInfo, nil
}

// ---------------------------------------------------------------------------
// Model call span lifecycle
// ---------------------------------------------------------------------------

func (m *ObservabilityModule) onModelCall(ctx context.Context, info *kernel.ModelCallInfo) (context.Context, *kernel.ModelCallInfo, error) {
	if err := m.lazyInit(); err != nil {
		return ctx, info, nil
	}

	msgs := info.Messages
	attrs := []attribute.KeyValue{
		attribute.Int("model.messages", len(msgs)),
	}
	totalChars := 0
	for _, msg := range msgs {
		if msg != nil {
			totalChars += len(msg.Content)
		}
	}
	attrs = append(attrs, attribute.Int("model.input_chars", totalChars))

	ctx, span := m.mkSpan(ctx, "model.call",
		trace.WithAttributes(attrs...),
	)
	ctx = contextWithValue(ctx, modelSpanKey, span)
	ctx = contextWithValue(ctx, modelStartKey, time.Now())

	span.AddEvent("model.call.started",
		trace.WithAttributes(attribute.Int("model.messages", len(msgs))),
	)

	return ctx, info, nil
}

func (m *ObservabilityModule) onModelResult(ctx context.Context, info *kernel.ModelCallInfo) (context.Context, *kernel.ModelCallInfo, error) {
	spanVal := contextValue(ctx, modelSpanKey)
	span, ok := spanVal.(trace.Span)
	if !ok || !span.IsRecording() {
		return ctx, info, nil
	}

	// Error handling
	if info != nil && info.Error != nil {
		recordError(span, info.Error)
		span.AddEvent("model.call.error",
			trace.WithAttributes(
				attribute.String("error.message", info.Error.Error()),
			),
		)
	} else {
		span.AddEvent("model.call.completed")
	}

	if info != nil {
		if info.Usage != nil {
			span.SetAttributes(
				attribute.Int("model.prompt_tokens", info.Usage.PromptTokens),
				attribute.Int("model.completion_tokens", info.Usage.CompletionTokens),
				attribute.Int("model.total_tokens", info.Usage.TotalTokens),
			)
		}
		if info.Response != nil {
			span.SetAttributes(
				attribute.Int("model.output_chars", len(info.Response.Content)),
			)
		}
	}

	// Duration & metric recording
	success := true
	if info != nil && info.Error != nil {
		success = false
	}
	if start, ok2 := contextValue(ctx, modelStartKey).(time.Time); ok2 {
		duration := time.Since(start).Seconds()
		span.SetAttributes(attribute.Float64("model.duration_seconds", duration))
		if m.cfg.CollectMetrics && m.modelCallDuration != nil {
			m.modelCallDuration.Record(ctx, duration,
				metric.WithAttributeSet(attribute.NewSet(
					attribute.Bool("success", success),
				)),
			)
		}
	}

	if m.cfg.CollectMetrics {
		if m.modelCallCounter != nil {
			m.modelCallCounter.Add(ctx, 1,
				metric.WithAttributeSet(attribute.NewSet(
					attribute.Bool("success", success),
				)),
			)
		}
		if info != nil && info.Usage != nil {
			if m.modelPromptTokens != nil {
				m.modelPromptTokens.Record(ctx, int64(info.Usage.PromptTokens))
			}
			if m.modelCompletionTokens != nil {
				m.modelCompletionTokens.Record(ctx, int64(info.Usage.CompletionTokens))
			}
		}
	}

	span.End()
	return ctx, info, nil
}

// ---------------------------------------------------------------------------
// Tool call span lifecycle
// ---------------------------------------------------------------------------

func (m *ObservabilityModule) onToolCall(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
	if err := m.lazyInit(); err != nil {
		log.Printf("[observability] lazyInit error: %v", err)
		return ctx, info, nil
	}

	ctx, span := m.mkSpan(ctx, "tool.call",
		trace.WithAttributes(
			attribute.String("tool.name", info.Name),
			attribute.Int("tool.args_len", len(info.Args)),
		),
	)
	ctx = contextWithValue(ctx, toolSpanKey, span)
	ctx = contextWithValue(ctx, toolStartKey, time.Now())

	span.AddEvent("tool.call.started",
		trace.WithAttributes(attribute.String("tool.name", info.Name)),
	)

	return ctx, info, nil
}

func (m *ObservabilityModule) onToolResult(ctx context.Context, info *kernel.ToolCallInfo) (context.Context, *kernel.ToolCallInfo, error) {
	spanVal := contextValue(ctx, toolSpanKey)
	span, ok := spanVal.(trace.Span)
	if !ok || !span.IsRecording() {
		return ctx, info, nil
	}

	// Result attributes always
	if info != nil {
		span.SetAttributes(
			attribute.Int("tool.result_len", len(info.Result)),
		)

		// Error handling
		if info.Error != nil {
			recordError(span, info.Error)
			span.AddEvent("tool.call.error",
				trace.WithAttributes(
					attribute.String("error.message", info.Error.Error()),
				),
			)
		} else {
			span.AddEvent("tool.call.completed")
		}
	}

	var toolName string
	var toolErr error
	if info != nil {
		toolName = info.Name
		toolErr = info.Error
	}

	if start, ok2 := contextValue(ctx, toolStartKey).(time.Time); ok2 {
		duration := time.Since(start).Seconds()
		span.SetAttributes(attribute.Float64("tool.duration_seconds", duration))
		if m.cfg.CollectMetrics && m.toolCallDuration != nil {
			m.toolCallDuration.Record(ctx, duration,
				metric.WithAttributeSet(attribute.NewSet(
					attribute.String("tool.name", toolName),
					attribute.Bool("success", toolErr == nil),
				)),
			)
		}
	}

	if m.cfg.CollectMetrics && m.toolCallCounter != nil {
		success := toolErr == nil
		m.toolCallCounter.Add(ctx, 1,
			metric.WithAttributeSet(attribute.NewSet(
				attribute.String("tool.name", toolName),
				attribute.Bool("success", success),
			)),
		)
	}

	span.End()
	return ctx, info, nil
}

// ---------------------------------------------------------------------------
// Agent run summary
// ---------------------------------------------------------------------------

func (m *ObservabilityModule) onAgentEnd(ctx context.Context, info *kernel.RunInfo) (context.Context, *kernel.RunInfo, error) {
	if err := m.lazyInit(); err != nil {
		return ctx, info, nil
	}

	msgs := info.AllMsgs
	if len(msgs) == 0 {
		return ctx, info, nil
	}

	roleCount := make(map[types.Role]int)
	totalChars := 0
	for _, msg := range msgs {
		if msg == nil {
			continue
		}
		totalChars += len(msg.Content)
		roleCount[msg.Role]++
	}

	_, span := m.mkSpan(context.Background(), "agent.run.complete",
		trace.WithAttributes(
			attribute.Int("agent.total_messages", len(msgs)),
			attribute.Int("agent.total_chars", totalChars),
			attribute.String("agent.roles", fmt.Sprintf("%v", roleCount)),
		),
	)
	if span.IsRecording() {
		span.AddEvent("agent.run.completed",
			trace.WithAttributes(
				attribute.Int("agent.total_messages", len(msgs)),
			),
		)
		span.End()
	}

	return ctx, info, nil
}

// ---------------------------------------------------------------------------
// Ensure ObservabilityModule implements a no-op error handler for OTel.
// ---------------------------------------------------------------------------

var _ otel.ErrorHandler = otel.ErrorHandlerFunc(func(err error) {})
