package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestRAGSearchTool_ExecuteReturnsCompactLLMJSON(t *testing.T) {
	workspace := t.TempDir()
	kbNotes := filepath.Join(workspace, "kb", "notes")
	if err := os.MkdirAll(kbNotes, 0o755); err != nil {
		t.Fatal(err)
	}

	content := `---
title: Architecture Note
date: 2026-02-18
tags: [rag, index]
source: internal
confidentiality: internal
---

Bleve based retrieval and chunk indexing discussion.
`
	if err := os.WriteFile(filepath.Join(kbNotes, "arch.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	rcfg := config.DefaultConfig().Tools.RAG
	tool := NewRAGSearchTool(workspace, rcfg, config.ProvidersConfig{})
	if tool == nil {
		t.Fatal("expected non-nil rag tool")
	}

	if _, err := tool.service.BuildIndex(context.Background()); err != nil {
		t.Fatalf("BuildIndex failed: %v", err)
	}

	res := tool.Execute(context.Background(), map[string]interface{}{
		"query": "bleve retrieval",
		"top_k": float64(5),
	})
	if res.IsError {
		t.Fatalf("expected success, got error: %s", res.ForLLM)
	}
	if !res.Silent {
		t.Fatalf("expected silent result for tool loop context")
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(res.ForLLM), &payload); err != nil {
		t.Fatalf("expected json payload, got err: %v", err)
	}
	if payload["query"] == "" {
		t.Fatalf("missing query in compact payload")
	}
	if _, ok := payload["sources"]; !ok {
		t.Fatalf("missing sources in compact payload")
	}
	if _, ok := payload["items"]; !ok {
		t.Fatalf("missing items in compact payload")
	}
}
