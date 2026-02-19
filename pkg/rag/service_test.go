package rag

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestBuildIndexAndSearch(t *testing.T) {
	workspace := t.TempDir()
	kbNotes := filepath.Join(workspace, "kb", "notes")
	if err := os.MkdirAll(kbNotes, 0o755); err != nil {
		t.Fatal(err)
	}

	content := `---
title: Team Meeting
date: 2026-02-18
tags: [infra, cache]
source: internal
confidentiality: internal
---

# Decisions
We discussed caching strategy and invalidation policy for api responses.
`
	if err := os.WriteFile(filepath.Join(kbNotes, "meeting.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	rcfg := config.DefaultConfig().Tools.RAG
	svc := NewService(workspace, rcfg, config.ProvidersConfig{})
	info, err := svc.BuildIndex(context.Background())
	if err != nil {
		t.Fatalf("BuildIndex failed: %v", err)
	}
	if info.TotalChunks == 0 {
		t.Fatalf("expected chunks > 0, warnings=%v", info.Warnings)
	}

	res, err := svc.Search(context.Background(), SearchRequest{Query: "caching strategy", TopK: 5})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if res.Full == nil || len(res.Full.Items) == 0 {
		t.Fatalf("expected non-empty full results; notes=%v", res.Full.Notes)
	}
	if res.LLM == nil || len(res.LLM.Items) == 0 {
		t.Fatalf("expected non-empty llm compact results")
	}

	item := res.Full.Items[0]
	if item.ChunkRef.ChunkOrdinal <= 0 || item.ChunkRef.SourcePath == "" {
		t.Fatalf("invalid chunk ref: %+v", item.ChunkRef)
	}
}

func TestRestrictedFilterDefaultExcludesRestricted(t *testing.T) {
	workspace := t.TempDir()
	kbNotes := filepath.Join(workspace, "kb", "notes")
	if err := os.MkdirAll(kbNotes, 0o755); err != nil {
		t.Fatal(err)
	}

	content := `---
title: Secret Note
date: 2026-02-10
tags: [secret]
source: internal
confidentiality: restricted
---

confidential incident details.
`
	if err := os.WriteFile(filepath.Join(kbNotes, "restricted.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	rcfg := config.DefaultConfig().Tools.RAG
	svc := NewService(workspace, rcfg, config.ProvidersConfig{})
	if _, err := svc.BuildIndex(context.Background()); err != nil {
		t.Fatalf("BuildIndex failed: %v", err)
	}

	res, err := svc.Search(context.Background(), SearchRequest{Query: "incident details", TopK: 5})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(res.Full.Items) != 0 {
		t.Fatalf("expected restricted docs to be excluded by default")
	}
}

func TestBuildIndexFailsForUnknownProvider(t *testing.T) {
	workspace := t.TempDir()
	rcfg := config.DefaultConfig().Tools.RAG
	rcfg.IndexProvider = "unknown-provider"

	svc := NewService(workspace, rcfg, config.ProvidersConfig{})
	_, err := svc.BuildIndex(context.Background())
	if err == nil {
		t.Fatalf("expected provider initialization error")
	}
	if !strings.Contains(err.Error(), "unsupported rag index_provider") {
		t.Fatalf("unexpected error: %v", err)
	}
}
