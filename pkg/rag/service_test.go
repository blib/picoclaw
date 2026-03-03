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

func TestEnsureIndexRebuildsWhenFileAdded(t *testing.T) {
	workspace := t.TempDir()
	kbNotes := filepath.Join(workspace, "kb", "notes")
	if err := os.MkdirAll(kbNotes, 0o755); err != nil {
		t.Fatal(err)
	}

	content := `---
title: First Doc
date: 2026-02-10
---

First document content about caching.
`
	if err := os.WriteFile(filepath.Join(kbNotes, "first.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	rcfg := config.DefaultConfig().Tools.RAG
	svc := NewService(workspace, rcfg, config.ProvidersConfig{})
	if _, err := svc.BuildIndex(context.Background()); err != nil {
		t.Fatalf("initial BuildIndex failed: %v", err)
	}

	// Add a new file (simulates file added while binary was down).
	newContent := `---
title: Second Doc
date: 2026-02-11
---

Second document about networking.
`
	if err := os.WriteFile(filepath.Join(kbNotes, "second.md"), []byte(newContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create fresh service instance (simulates restart) — index already exists on disk.
	svc2 := NewService(workspace, rcfg, config.ProvidersConfig{})
	// EnsureIndex should detect the new file and rebuild.
	svc2.EnsureIndex(context.Background())

	res, err := svc2.Search(context.Background(), SearchRequest{Query: "networking", TopK: 5})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	found := false
	for _, item := range res.Full.Items {
		if strings.HasSuffix(item.SourcePath, "second.md") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected second.md (added after initial build) to be indexed after restart")
	}
}

func TestEnsureIndexRebuildsWhenConfigChanged(t *testing.T) {
	workspace := t.TempDir()
	kbNotes := filepath.Join(workspace, "kb", "notes")
	if err := os.MkdirAll(kbNotes, 0o755); err != nil {
		t.Fatal(err)
	}

	content := `---
title: Doc
date: 2026-02-10
---

Some content about testing.
`
	if err := os.WriteFile(filepath.Join(kbNotes, "doc.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	rcfg := config.DefaultConfig().Tools.RAG
	svc := NewService(workspace, rcfg, config.ProvidersConfig{})
	info1, err := svc.BuildIndex(context.Background())
	if err != nil {
		t.Fatalf("initial BuildIndex failed: %v", err)
	}

	// Change embedding provider (simulates user editing config while binary is down).
	rcfg2 := rcfg
	rcfg2.EmbeddingProvider = "ollama"
	rcfg2.EmbeddingModelID = "nomic-embed-text"
	svc2 := NewService(workspace, rcfg2, config.ProvidersConfig{})
	svc2.EnsureIndex(context.Background())

	provider2, _ := svc2.providerOrErr()
	info2, _ := provider2.LoadIndexInfo(context.Background())
	if info2 == nil {
		t.Fatal("expected index info after EnsureIndex")
	}

	if info2.ConfigHash == info1.ConfigHash {
		t.Error("expected config hash to change after provider/model change")
	}
	if info2.EmbeddingModelID != "nomic-embed-text" {
		t.Errorf("expected embedding model 'nomic-embed-text', got %q", info2.EmbeddingModelID)
	}
}

func TestEnsureIndexRebuildsLegacyIndex(t *testing.T) {
	workspace := t.TempDir()
	kbNotes := filepath.Join(workspace, "kb", "notes")
	if err := os.MkdirAll(kbNotes, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(kbNotes, "doc.md"), []byte("---\ntitle: Doc\ndate: 2026-02-10\n---\n\nContent.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rcfg := config.DefaultConfig().Tools.RAG
	svc := NewService(workspace, rcfg, config.ProvidersConfig{})
	if _, err := svc.BuildIndex(context.Background()); err != nil {
		t.Fatalf("initial BuildIndex failed: %v", err)
	}

	// Simulate legacy index by clearing ConfigHash and FilesFingerprint.
	provider, _ := svc.providerOrErr()
	info, _ := provider.LoadIndexInfo(context.Background())
	info.ConfigHash = ""
	info.FilesFingerprint = ""
	if err := provider.Build(context.Background(), nil, *info); err != nil {
		// Build with nil chunks just to store the modified info.
		// Fall back to loading chunks and rebuilding.
		chunks, _ := provider.LoadChunks(context.Background())
		if err := provider.Build(context.Background(), chunks, *info); err != nil {
			t.Fatalf("failed to store legacy index info: %v", err)
		}
	}

	// New service instance should detect missing hashes and rebuild.
	svc2 := NewService(workspace, rcfg, config.ProvidersConfig{})
	svc2.EnsureIndex(context.Background())

	provider2, _ := svc2.providerOrErr()
	info2, _ := provider2.LoadIndexInfo(context.Background())
	if info2 == nil {
		t.Fatal("expected index info after EnsureIndex")
	}
	if info2.ConfigHash == "" {
		t.Error("expected non-empty ConfigHash after rebuild of legacy index")
	}
	if info2.FilesFingerprint == "" {
		t.Error("expected non-empty FilesFingerprint after rebuild of legacy index")
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

func TestSearchAutoBuildOnFirstUse(t *testing.T) {
	workspace := t.TempDir()
	kbNotes := filepath.Join(workspace, "kb", "notes")
	if err := os.MkdirAll(kbNotes, 0o755); err != nil {
		t.Fatal(err)
	}

	content := `---
title: Auto Build Test
date: 2026-02-21
tags: [test]
---

This document tests automatic index building on first search.
`
	if err := os.WriteFile(filepath.Join(kbNotes, "auto.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	rcfg := config.DefaultConfig().Tools.RAG
	svc := NewService(workspace, rcfg, config.ProvidersConfig{})

	// Search without calling BuildIndex — should auto-build.
	res, err := svc.Search(context.Background(), SearchRequest{Query: "automatic index building", TopK: 5})
	if err != nil {
		t.Fatalf("Search (auto-build) failed: %v", err)
	}
	if res.Full == nil || len(res.Full.Items) == 0 {
		t.Fatalf("expected results after auto-build; notes=%v", res.Full.Notes)
	}
}

func TestMinScoreCutoffDropsLowRelevance(t *testing.T) {
	workspace := t.TempDir()
	kbDir := filepath.Join(workspace, "kb", "notes")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// One highly relevant document.
	relevant := `---
title: Caching Strategy
date: 2026-02-18
tags: [cache]
---

Our caching strategy uses write-through with TTL-based invalidation for API responses.
`
	// One weakly relevant document — mentions "caching" once among unrelated text.
	weak := `---
title: Unrelated Notes
date: 2026-02-01
tags: [misc]
---

The quarterly budget review covered travel expenses, office supplies, and equipment.
Procurement timelines for hardware were discussed at length.
Someone briefly mentioned caching but the topic was dropped immediately.
We also discussed parking allocation and kitchen supplies.
`
	if err := os.WriteFile(filepath.Join(kbDir, "relevant.md"), []byte(relevant), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kbDir, "weak.md"), []byte(weak), 0o644); err != nil {
		t.Fatal(err)
	}

	rcfg := config.DefaultConfig().Tools.RAG
	svc := NewService(workspace, rcfg, config.ProvidersConfig{})
	if _, err := svc.BuildIndex(context.Background()); err != nil {
		t.Fatalf("BuildIndex failed: %v", err)
	}

	// Search without min_score — should return results from both docs.
	resAll, err := svc.Search(context.Background(), SearchRequest{Query: "caching strategy invalidation", TopK: 20})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if resAll.Full == nil || len(resAll.Full.Items) == 0 {
		t.Fatalf("expected results without min_score filter")
	}

	// Search with very high min_score — should drop everything.
	highCutoff := 999.0
	resNone, err := svc.Search(context.Background(), SearchRequest{
		Query:    "caching strategy invalidation",
		TopK:     20,
		MinScore: &highCutoff,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(resNone.Full.Items) != 0 {
		t.Fatalf("expected 0 items with min_score=%.1f, got %d", highCutoff, len(resNone.Full.Items))
	}
	foundNote := false
	for _, n := range resNone.Full.Notes {
		if strings.Contains(n, "min_score=") && strings.Contains(n, "dropped") {
			foundNote = true
			break
		}
	}
	if !foundNote {
		t.Fatalf("expected min_score drop note in notes=%v", resNone.Full.Notes)
	}

	// Search with moderate min_score — should filter weak hits but keep strong ones.
	if len(resAll.Full.Items) > 1 {
		// Use the score of the last item + epsilon as cutoff.
		lastScore := resAll.Full.Items[len(resAll.Full.Items)-1].Score
		midCutoff := lastScore + 0.001
		resMid, err := svc.Search(context.Background(), SearchRequest{
			Query:    "caching strategy invalidation",
			TopK:     20,
			MinScore: &midCutoff,
		})
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(resMid.Full.Items) >= len(resAll.Full.Items) {
			t.Fatalf("expected mid cutoff (%.4f) to drop at least one result; all=%d mid=%d",
				midCutoff, len(resAll.Full.Items), len(resMid.Full.Items))
		}
	}
}
