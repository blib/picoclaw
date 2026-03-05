package tools

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestExecTool_SyncExecution(t *testing.T) {
	tool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}

	result := tool.Execute(context.Background(), map[string]any{
		"command": "echo sync_output",
	})

	if result.IsError {
		t.Fatalf("expected success: %s", result.ForLLM)
	}
	if result.Async {
		t.Error("sync execution should not be async")
	}
}

func TestExecTool_BackgroundWithoutBus(t *testing.T) {
	tool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}

	// background=true but no bus → ExecuteAsync falls back to synchronous
	cb := func(_ context.Context, _ *ToolResult) {
		t.Error("callback should not be invoked for sync fallback")
	}

	result := tool.ExecuteAsync(context.Background(), map[string]any{
		"command":    "echo fallback",
		"background": true,
	}, cb)

	if result.Async {
		t.Error("should fall back to sync when no bus is configured")
	}
	if result.IsError {
		t.Fatalf("expected success: %s", result.ForLLM)
	}
}

func TestExecTool_BackgroundWithBus(t *testing.T) {
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	tool, err := NewExecToolWithConfig(t.TempDir(), false, nil, msgBus)
	if err != nil {
		t.Fatal(err)
	}

	ctx := WithToolContext(context.Background(), "telegram", "chat-123")

	cb := func(_ context.Context, _ *ToolResult) {}
	result := tool.ExecuteAsync(ctx, map[string]any{
		"command":    "echo bg_output",
		"background": true,
	}, cb)

	if !result.Async {
		t.Fatal("expected async result")
	}

	// Consume the inbound message published by the background goroutine.
	msg, ok := msgBus.ConsumeInbound(ctx)
	if !ok {
		t.Fatal("expected inbound message from background exec")
	}

	if msg.Channel != "system" {
		t.Errorf("expected channel=system, got %q", msg.Channel)
	}
	if msg.ChatID != "telegram:chat-123" {
		t.Errorf("expected chatID=telegram:chat-123, got %q", msg.ChatID)
	}
	if !strings.Contains(msg.Content, "bg_output") {
		t.Errorf("expected output in message content: %s", msg.Content)
	}
	if !strings.Contains(msg.Content, "completed") {
		t.Errorf("expected 'completed' in message content: %s", msg.Content)
	}
}

func TestExecTool_BackgroundBlockedCommand(t *testing.T) {
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	tool, err := NewExecToolWithConfig(t.TempDir(), false, nil, msgBus)
	if err != nil {
		t.Fatal(err)
	}

	ctx := WithToolContext(context.Background(), "telegram", "chat-123")

	cb := func(_ context.Context, _ *ToolResult) {}
	result := tool.ExecuteAsync(ctx, map[string]any{
		"command":    "sudo rm -rf /",
		"background": true,
	}, cb)

	if !result.Async {
		t.Fatal("expected async result even for blocked commands")
	}

	msg, ok := msgBus.ConsumeInbound(ctx)
	if !ok {
		t.Fatal("expected inbound message from background exec")
	}

	if !strings.Contains(msg.Content, "failed") {
		t.Errorf("expected 'failed' in message content: %s", msg.Content)
	}
}

func TestExecTool_ImplementsAsyncExecutor(t *testing.T) {
	tool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}

	var _ AsyncExecutor = tool // compile-time check
}

// captureStdout runs fn and returns whatever it wrote to os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	old := os.Stdout
	os.Stdout = w
	defer func() {
		os.Stdout = old
		_ = w.Close()
		_ = r.Close()
	}()

	fn()

	_ = w.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func boolPtr(b bool) *bool { return &b }

func TestWarnDeprecatedExecConfig_EnableDenyPatternsFalse(t *testing.T) {
	out := captureStdout(t, func() {
		warnDeprecatedExecConfig(config.ExecConfig{
			EnableDenyPatterns: boolPtr(false),
		})
	})

	if !strings.Contains(out, "enable_deny_patterns: false") {
		t.Errorf("expected warning about 'enable_deny_patterns: false', got: %s", out)
	}
	if !strings.Contains(out, "risk_threshold: critical") {
		t.Errorf("expected migration hint to 'risk_threshold: critical', got: %s", out)
	}
}

func TestWarnDeprecatedExecConfig_EnableDenyPatternsTrue(t *testing.T) {
	out := captureStdout(t, func() {
		warnDeprecatedExecConfig(config.ExecConfig{
			EnableDenyPatterns: boolPtr(true),
		})
	})

	if !strings.Contains(out, "enable_deny_patterns") {
		t.Errorf("expected deprecation warning, got: %s", out)
	}
	if !strings.Contains(out, "Remove this field") {
		t.Errorf("expected removal hint, got: %s", out)
	}
}

func TestWarnDeprecatedExecConfig_NilNoWarning(t *testing.T) {
	out := captureStdout(t, func() {
		warnDeprecatedExecConfig(config.ExecConfig{})
	})

	if strings.Contains(out, "enable_deny_patterns") {
		t.Errorf("expected no warning when field is absent, got: %s", out)
	}
}

func TestWarnDeprecatedExecConfig_CustomPatterns(t *testing.T) {
	out := captureStdout(t, func() {
		warnDeprecatedExecConfig(config.ExecConfig{
			CustomDenyPatterns:  []string{"rm"},
			CustomAllowPatterns: []string{"ls"},
		})
	})

	if !strings.Contains(out, "custom_deny_patterns") {
		t.Errorf("expected custom_deny_patterns warning, got: %s", out)
	}
	if !strings.Contains(out, "custom_allow_patterns") {
		t.Errorf("expected custom_allow_patterns warning, got: %s", out)
	}
}

func TestWarnDeprecatedExecConfig_AllDeprecatedFields(t *testing.T) {
	out := captureStdout(t, func() {
		warnDeprecatedExecConfig(config.ExecConfig{
			EnableDenyPatterns:  boolPtr(false),
			CustomDenyPatterns:  []string{"rm"},
			CustomAllowPatterns: []string{"ls"},
		})
	})

	// All three warnings should fire.
	for _, want := range []string{
		"enable_deny_patterns: false",
		"custom_deny_patterns",
		"custom_allow_patterns",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected warning containing %q, got: %s", want, out)
		}
	}
}

func TestNewExecToolWithConfig_EnableDenyPatternsFalseWarning(t *testing.T) {
	out := captureStdout(t, func() {
		cfg := &config.Config{}
		cfg.Tools.Exec.EnableDenyPatterns = boolPtr(false)
		_, err := NewExecToolWithConfig(t.TempDir(), false, cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
	})

	if !strings.Contains(out, "enable_deny_patterns: false") {
		t.Errorf("expected warning in NewExecToolWithConfig output: %s", out)
	}
}
