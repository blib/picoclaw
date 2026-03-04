package shell

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"mvdan.cc/sh/v3/expand"
)

func TestRun_Success(t *testing.T) {
	result := Run(context.Background(), RunConfig{
		Command:       "echo 'hello world'",
		Dir:           t.TempDir(),
		Timeout:       5 * time.Second,
		RiskThreshold: RiskMedium,
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.Output)
	}
	if !strings.Contains(result.Output, "hello world") {
		t.Errorf("expected 'hello world' in output, got: %s", result.Output)
	}
}

func TestRun_BlocksDangerousCommand(t *testing.T) {
	result := Run(context.Background(), RunConfig{
		Command:       "rm -rf /",
		Dir:           t.TempDir(),
		Timeout:       5 * time.Second,
		RiskThreshold: RiskMedium,
	})

	if !result.IsError {
		t.Fatal("expected rm -rf to be blocked")
	}
	if !strings.Contains(result.Output, "blocked") {
		t.Errorf("expected 'blocked' in output: %s", result.Output)
	}
}

func TestRun_BlocksSudo(t *testing.T) {
	result := Run(context.Background(), RunConfig{
		Command:       "sudo ls",
		Dir:           t.TempDir(),
		Timeout:       5 * time.Second,
		RiskThreshold: RiskMedium,
	})

	if !result.IsError {
		t.Fatal("expected sudo to be blocked")
	}
	if !strings.Contains(result.Output, "risk_level=critical") {
		t.Errorf("expected risk_level=critical in output: %s", result.Output)
	}
}

func TestRun_BlocksVariableIndirection(t *testing.T) {
	// x=rm; $x -rf / — the old regex system couldn't catch this.
	result := Run(context.Background(), RunConfig{
		Command:       `x=rm; $x -rf /`,
		Dir:           t.TempDir(),
		Timeout:       5 * time.Second,
		RiskThreshold: RiskMedium,
	})

	if !result.IsError {
		t.Fatal("expected variable indirection bypass to be blocked")
	}
	if !strings.Contains(result.Output, "blocked") {
		t.Errorf("expected 'blocked' in output: %s", result.Output)
	}
}

func TestRun_BlocksCommandSubstitution(t *testing.T) {
	result := Run(context.Background(), RunConfig{
		Command:       "$(echo rm) -rf /tmp/safe_test_dir",
		Dir:           t.TempDir(),
		Timeout:       5 * time.Second,
		RiskThreshold: RiskMedium,
	})

	if !result.IsError {
		t.Fatal("expected command substitution bypass to be blocked")
	}
}

func TestRun_Timeout(t *testing.T) {
	result := Run(context.Background(), RunConfig{
		Command:       "sleep 60",
		Dir:           t.TempDir(),
		Timeout:       200 * time.Millisecond,
		RiskThreshold: RiskMedium,
	})

	if !result.IsError {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(result.Output, "timed out") {
		t.Errorf("expected 'timed out' in output: %s", result.Output)
	}
}

func TestRun_WorkingDir(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("test content"), 0o644)

	result := Run(context.Background(), RunConfig{
		Command:       "cat test.txt",
		Dir:           tmpDir,
		Timeout:       5 * time.Second,
		RiskThreshold: RiskMedium,
	})

	if result.IsError {
		t.Fatalf("expected success: %s", result.Output)
	}
	if !strings.Contains(result.Output, "test content") {
		t.Errorf("expected 'test content' in output: %s", result.Output)
	}
}

func TestRun_ParseError(t *testing.T) {
	result := Run(context.Background(), RunConfig{
		Command:       "echo 'unclosed",
		Dir:           t.TempDir(),
		Timeout:       5 * time.Second,
		RiskThreshold: RiskMedium,
	})

	if !result.IsError {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(result.Output, "parse") {
		t.Errorf("expected 'parse' in error: %s", result.Output)
	}
}

func TestRun_StderrCapture(t *testing.T) {
	result := Run(context.Background(), RunConfig{
		Command:       "echo stdout_msg; echo stderr_msg >&2",
		Dir:           t.TempDir(),
		Timeout:       5 * time.Second,
		RiskThreshold: RiskMedium,
	})

	if !strings.Contains(result.Output, "stdout_msg") {
		t.Errorf("expected stdout in output: %s", result.Output)
	}
	if !strings.Contains(result.Output, "stderr_msg") {
		t.Errorf("expected stderr in output: %s", result.Output)
	}
}

func TestRun_HighThresholdAllowsRm(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "delete_me.txt")
	os.WriteFile(testFile, []byte("bye"), 0o644)

	result := Run(context.Background(), RunConfig{
		Command:       "rm delete_me.txt",
		Dir:           tmpDir,
		Timeout:       5 * time.Second,
		RiskThreshold: RiskHigh,
	})

	if result.IsError {
		t.Fatalf("with threshold=high, rm should be allowed: %s", result.Output)
	}
	if _, err := os.Stat(testFile); err == nil {
		t.Error("file should have been deleted")
	}
}

func TestRun_EnvSanitization(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-secret-test")
	t.Setenv("PATH", os.Getenv("PATH"))

	result := Run(context.Background(), RunConfig{
		Command:       "env",
		Dir:           t.TempDir(),
		Timeout:       5 * time.Second,
		RiskThreshold: RiskMedium,
	})

	if result.IsError {
		t.Fatalf("expected env command to succeed: %s", result.Output)
	}
	if strings.Contains(result.Output, "OPENAI_API_KEY") {
		t.Error("OPENAI_API_KEY should not be in child environment")
	}
	if !strings.Contains(result.Output, "PATH=") {
		t.Error("PATH should be in child environment")
	}
}

func TestRun_PipelineCommand(t *testing.T) {
	result := Run(context.Background(), RunConfig{
		Command:       "echo 'line1\nline2\nline3' | wc -l",
		Dir:           t.TempDir(),
		Timeout:       5 * time.Second,
		RiskThreshold: RiskMedium,
	})

	if result.IsError {
		t.Fatalf("expected pipeline to succeed: %s", result.Output)
	}
}

func TestRun_DevNullRedirection(t *testing.T) {
	result := Run(context.Background(), RunConfig{
		Command:       "echo hello 2>/dev/null",
		Dir:           t.TempDir(),
		Timeout:       5 * time.Second,
		Restrict:      true,
		WorkspaceDir:  t.TempDir(),
		RiskThreshold: RiskMedium,
	})

	if result.IsError && strings.Contains(result.Output, "sandbox") {
		t.Errorf("/dev/null should not be blocked: %s", result.Output)
	}
}

func TestRun_RiskOverrides(t *testing.T) {
	// Override rm to low so it passes with threshold=medium.
	result := Run(context.Background(), RunConfig{
		Command:       "rm nonexistent_file_xyz 2>/dev/null; echo done",
		Dir:           t.TempDir(),
		Timeout:       5 * time.Second,
		RiskThreshold: RiskMedium,
		RiskOverrides: map[string]string{"rm": "low"},
	})

	// rm should be allowed because of override. The command may fail
	// (file doesn't exist) but it should not be _blocked_.
	if result.IsError && strings.Contains(result.Output, "blocked") {
		t.Errorf("rm should be allowed with override: %s", result.Output)
	}
}

// --- lookPath unit tests ---

func makeTestEnv(vars map[string]string) expand.Environ {
	return &sanitizedEnv{vars: vars}
}

func TestLookPath_PathContainsSlash(t *testing.T) {
	env := makeTestEnv(map[string]string{"PATH": "/usr/bin"})

	// Forward-slash path → returned as-is, no PATH search.
	got, err := lookPath(env, "/usr/bin/git")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/usr/bin/git" {
		t.Errorf("got %q, want /usr/bin/git", got)
	}

	// Relative path with slash → also returned as-is.
	got, err = lookPath(env, "./script.sh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "./script.sh" {
		t.Errorf("got %q, want ./script.sh", got)
	}
}

func TestLookPath_FindsExecutableInPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable-bit test")
	}
	dir := t.TempDir()
	binPath := filepath.Join(dir, "mytool")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	env := makeTestEnv(map[string]string{"PATH": dir})
	got, err := lookPath(env, "mytool")
	if err != nil {
		t.Fatalf("expected to find mytool: %v", err)
	}
	if got != binPath {
		t.Errorf("got %q, want %q", got, binPath)
	}
}

func TestLookPath_SkipsNonExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable-bit test")
	}
	dir := t.TempDir()
	// Create a file without executable bit.
	binPath := filepath.Join(dir, "noexec")
	if err := os.WriteFile(binPath, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	env := makeTestEnv(map[string]string{"PATH": dir})
	_, err := lookPath(env, "noexec")
	if err == nil {
		t.Error("expected error for non-executable file")
	}
}

func TestLookPath_PATHNotSet(t *testing.T) {
	env := makeTestEnv(map[string]string{})
	_, err := lookPath(env, "ls")
	if err == nil {
		t.Error("expected error when PATH is not set")
	}
}

func TestLookPath_PATHEmpty(t *testing.T) {
	env := makeTestEnv(map[string]string{"PATH": ""})
	_, err := lookPath(env, "ls")
	if err == nil {
		t.Error("expected error when PATH is empty")
	}
}
