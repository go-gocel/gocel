package observability

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/go-gocel/gocel/core/kernel"
	"github.com/go-gocel/gocel/core/runtime"

	"github.com/go-gocel/gocel/core/types"
)

// 鈹€鈹€ Construction 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestNewObservabilityModule(t *testing.T) {
	m := NewObservabilityModule()
	if m == nil {
		t.Fatal("module is nil")
	}
	if m.cfg.ServiceName != "gocel" {
		t.Errorf("ServiceName = %q, want %q", m.cfg.ServiceName, "gocel")
	}
	if m.cfg.ExporterType != ExporterStdout {
		t.Errorf("ExporterType = %d, want %d", m.cfg.ExporterType, ExporterStdout)
	}
	if !m.cfg.CollectTraces {
		t.Error("CollectTraces should be true by default")
	}
	if !m.cfg.CollectMetrics {
		t.Error("CollectMetrics should be true by default")
	}
}

func TestNewObservabilityModule_WithOptions(t *testing.T) {
	m := NewObservabilityModule(
		WithServiceName("my-app"),
		WithEnvironment("production"),
		WithNoopExporter(),
	)
	if m.cfg.ServiceName != "my-app" {
		t.Errorf("ServiceName = %q, want %q", m.cfg.ServiceName, "my-app")
	}
	if m.cfg.Environment != "production" {
		t.Errorf("Environment = %q, want %q", m.cfg.Environment, "production")
	}
	if m.cfg.ExporterType != ExporterNoop {
		t.Errorf("ExporterType = %d, want %d", m.cfg.ExporterType, ExporterNoop)
	}
}

func TestNewObservabilityModule_WithStdoutExporter(t *testing.T) {
	m := NewObservabilityModule(WithStdoutExporter())
	if m.cfg.ExporterType != ExporterStdout {
		t.Errorf("ExporterType = %d, want %d", m.cfg.ExporterType, ExporterStdout)
	}
}

func TestNewObservabilityModule_WithAttributes(t *testing.T) {
	m := NewObservabilityModule(
		WithNoopExporter(),
		WithAttribute("env", "test"),
		WithAttribute("version", "1.0"),
	)
	if m.cfg.Attributes == nil {
		t.Fatal("Attributes should not be nil")
	}
	if m.cfg.Attributes["env"] != "test" {
		t.Errorf("Attributes[env] = %q, want %q", m.cfg.Attributes["env"], "test")
	}
	if m.cfg.Attributes["version"] != "1.0" {
		t.Errorf("Attributes[version] = %q, want %q", m.cfg.Attributes["version"], "1.0")
	}
}

// 鈹€鈹€ ExporterType constants 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestExporterType_Values(t *testing.T) {
	if ExporterNoop != 0 {
		t.Errorf("ExporterNoop = %d, want 0", ExporterNoop)
	}
	if ExporterStdout != 1 {
		t.Errorf("ExporterStdout = %d, want 1", ExporterStdout)
	}
	if ExporterOTLP != 2 {
		t.Errorf("ExporterOTLP = %d, want 2", ExporterOTLP)
	}
	if ExporterFile != 3 {
		t.Errorf("ExporterFile = %d, want 3", ExporterFile)
	}
}

// 鈹€鈹€ Register (with ExporterNoop) 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestObservabilityModule_Register(t *testing.T) {
	m := NewObservabilityModule(WithNoopExporter())
	rt := runtime.NewRuntime(nil, nil)
	// Should not panic
	m.Register(rt)
}

func TestObservabilityModule_Register_Twice(t *testing.T) {
	m := NewObservabilityModule(WithNoopExporter())
	rt := runtime.NewRuntime(nil, nil)
	m.Register(rt)
	// Registering again should be idempotent (guarded by sync.Once)
	m.Register(rt)
}

func TestObservabilityModule_Register_DisableTraces(t *testing.T) {
	m := NewObservabilityModule(
		WithNoopExporter(),
		func(m *ObservabilityModule) { m.cfg.CollectTraces = false },
	)
	rt := runtime.NewRuntime(nil, nil)
	m.Register(rt)
}

func TestObservabilityModule_Register_DisableMetrics(t *testing.T) {
	m := NewObservabilityModule(
		WithNoopExporter(),
		func(m *ObservabilityModule) { m.cfg.CollectMetrics = false },
	)
	rt := runtime.NewRuntime(nil, nil)
	m.Register(rt)
}

// 鈹€鈹€ Shutdown 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestObservabilityModule_Shutdown(t *testing.T) {
	m := NewObservabilityModule(WithNoopExporter())
	rt := runtime.NewRuntime(nil, nil)
	m.Register(rt)
	ctx := context.Background()
	// Should not panic
	m.Shutdown(ctx)
}

func TestObservabilityModule_Shutdown_WithoutRegister(t *testing.T) {
	m := NewObservabilityModule(WithNoopExporter())
	ctx := context.Background()
	// Shutdown without registering first — should not panic
	m.Shutdown(ctx)
}

func TestObservabilityModule_Shutdown_Idempotent(t *testing.T) {
	m := NewObservabilityModule(WithNoopExporter())
	rt := runtime.NewRuntime(nil, nil)
	m.Register(rt)
	ctx := context.Background()
	m.Shutdown(ctx)
	// Second shutdown should be idempotent
	m.Shutdown(ctx)
}

// 鈹€鈹€ WithExporter 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestWithExporter(t *testing.T) {
	m := NewObservabilityModule(WithExporter(ExporterNoop))
	if m.cfg.ExporterType != ExporterNoop {
		t.Errorf("ExporterType = %d, want %d", m.cfg.ExporterType, ExporterNoop)
	}

	m2 := NewObservabilityModule(WithExporter(ExporterStdout))
	if m2.cfg.ExporterType != ExporterStdout {
		t.Errorf("ExporterType = %d, want %d", m2.cfg.ExporterType, ExporterStdout)
	}

	m3 := NewObservabilityModule(WithExporter(ExporterFile))
	if m3.cfg.ExporterType != ExporterFile {
		t.Errorf("ExporterType = %d, want %d", m3.cfg.ExporterType, ExporterFile)
	}
}

// 鈹€鈹€ WithTraceSampleRate 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestWithTraceSampleRate(t *testing.T) {
	m := NewObservabilityModule(WithNoopExporter(), WithTraceSampleRate(0.5))
	if m.cfg.TraceSampleRate != 0.5 {
		t.Errorf("TraceSampleRate = %f, want 0.5", m.cfg.TraceSampleRate)
	}
}

// 鈹€鈹€ WithCollectMetrics/WithCollectTraces 鈹€鈹€鈹€鈹€鈹€

func TestWithCollectMetrics(t *testing.T) {
	m := NewObservabilityModule(WithNoopExporter(), WithCollectMetrics(false))
	if m.cfg.CollectMetrics {
		t.Error("CollectMetrics should be false")
	}
}

func TestWithCollectTraces(t *testing.T) {
	m := NewObservabilityModule(WithNoopExporter(), WithCollectTraces(false))
	if m.cfg.CollectTraces {
		t.Error("CollectTraces should be false")
	}
}

// 鈹€鈹€ File exporter 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestObservabilityModule_FileExporter(t *testing.T) {
	dir := t.TempDir()
	m := NewObservabilityModule(WithFileExporter(dir))
	rt := runtime.NewRuntime(nil, nil)
	m.Register(rt)
	// Should create output directory
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Errorf("output dir should exist: %s", dir)
	}
	m.Shutdown(context.Background())
}

// 鈹€鈹€ Hook emission (noop mode) 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestObservabilityModule_Hooks_Noop(t *testing.T) {
	m := NewObservabilityModule(WithNoopExporter())
	rt := runtime.NewRuntime(nil, nil)
	m.Register(rt)

	ctx := context.Background()

	// Emit through various hook points — should not panic
	// onModelCall expects *kernel.ModelCallInfo
	msgs := []*types.Message{types.NewUserMessage("test")}
	var err error
	var mcInfo *kernel.ModelCallInfo
	ctx, mcInfo, err = m.onModelCall(ctx, &kernel.ModelCallInfo{Messages: msgs})
	if err != nil {
		t.Fatalf("onModelCall: %v", err)
	}

	// onModelResult expects *kernel.ModelCallInfo
	ctx, mcInfo, err = m.onModelResult(ctx, &kernel.ModelCallInfo{
		Messages: []*types.Message{types.NewUserMessage("test")},
		Response: types.NewAssistantMessage("response"),
	})
	if err != nil {
		t.Fatalf("onModelResult: %v", err)
	}
	_ = mcInfo

	// onToolCall expects *kernel.ToolCallInfo
	var tci *kernel.ToolCallInfo
	ctx, tci, err = m.onToolCall(ctx, &kernel.ToolCallInfo{
		Name: "test_tool",
		Args: "{}",
	})
	if err != nil {
		t.Fatalf("onToolCall: %v", err)
	}
	_ = tci

	// onToolResult expects *kernel.ToolCallInfo
	var atci *kernel.ToolCallInfo
	ctx, atci, err = m.onToolResult(ctx, &kernel.ToolCallInfo{
		Name:   "test_tool",
		Args:   "{}",
		Result: "ok",
	})
	if err != nil {
		t.Fatalf("onToolResult: %v", err)
	}
	_ = atci
}

// 鈹€鈹€ DefaultConfig 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.ServiceName != "gocel" {
		t.Errorf("ServiceName = %q, want %q", cfg.ServiceName, "gocel")
	}
	if cfg.Environment != "development" {
		t.Errorf("Environment = %q, want %q", cfg.Environment, "development")
	}
	if cfg.ExporterType != ExporterStdout {
		t.Errorf("ExporterType = %d, want %d", cfg.ExporterType, ExporterStdout)
	}
	if cfg.TraceSampleRate != 1.0 {
		t.Errorf("TraceSampleRate = %f, want 1.0", cfg.TraceSampleRate)
	}
	if !cfg.CollectMetrics {
		t.Error("CollectMetrics should be true")
	}
	if !cfg.CollectTraces {
		t.Error("CollectTraces should be true")
	}
	if cfg.MetricsExportInterval != 10*time.Second {
		t.Errorf("MetricsExportInterval = %v, want 10s", cfg.MetricsExportInterval)
	}
}

// 鈹€鈹€ WithFileExporter 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestWithFileExporter(t *testing.T) {
	m := NewObservabilityModule(WithFileExporter("/tmp/gocel-test"))
	if m.cfg.ExporterType != ExporterFile {
		t.Errorf("ExporterType = %d, want %d", m.cfg.ExporterType, ExporterFile)
	}
	if m.cfg.OutputDir != "/tmp/gocel-test" {
		t.Errorf("OutputDir = %q, want %q", m.cfg.OutputDir, "/tmp/gocel-test")
	}
}

// 鈹€鈹€ Guard: double Register 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

func TestDoubleRegister(t *testing.T) {
	m := NewObservabilityModule(WithNoopExporter())
	rt := runtime.NewRuntime(nil, nil)
	m.Register(rt)
	// Second register must not panic or log (guarded by lazyInit mutex + sync.Once)
	m.Register(rt)
}
