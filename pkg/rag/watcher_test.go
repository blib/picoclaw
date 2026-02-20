package rag

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestWatcherReindexOnFileChange(t *testing.T) {
	dir := t.TempDir()
	kbDir := filepath.Join(dir, "kb")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// write initial file
	writeTestMD(t, kbDir, "a.md", "# Alpha\n\nThis is alpha content about caching.\n")

	svc := newTestService(t, dir, kbDir)
	if _, err := svc.BuildIndex(context.Background()); err != nil {
		t.Fatalf("initial build: %v", err)
	}

	w, err := NewWatcher(svc,
		WithReindexDebounce(50*time.Millisecond),
		WithFlushDebounce(200*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	// modify the file — should trigger reindex + flush
	writeTestMD(t, kbDir, "a.md", "# Alpha\n\nUpdated content about deployment.\n")

	// wait for reindex debounce + flush debounce + slack
	time.Sleep(500 * time.Millisecond)

	// verify the provider picked up the change
	res, err := svc.provider.Search(context.Background(), "deployment", ProviderSearchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("Search after watcher reindex: %v", err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("expected hits for 'deployment' after watcher reindex")
	}
}

func TestWatcherNewFileCreation(t *testing.T) {
	dir := t.TempDir()
	kbDir := filepath.Join(dir, "kb")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeTestMD(t, kbDir, "a.md", "# Alpha\n\nInitial document.\n")

	svc := newTestService(t, dir, kbDir)
	if _, err := svc.BuildIndex(context.Background()); err != nil {
		t.Fatalf("initial build: %v", err)
	}

	w, err := NewWatcher(svc,
		WithReindexDebounce(50*time.Millisecond),
		WithFlushDebounce(200*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	// create a new file
	writeTestMD(t, kbDir, "b.md", "# Beta\n\nNew content about kubernetes orchestration.\n")

	time.Sleep(500 * time.Millisecond)

	res, err := svc.provider.Search(context.Background(), "kubernetes", ProviderSearchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("expected hits for 'kubernetes' after new file creation")
	}
}

func TestWatcherFlushClearsDirty(t *testing.T) {
	dir := t.TempDir()
	kbDir := filepath.Join(dir, "kb")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeTestMD(t, kbDir, "a.md", "# Test\n\nSome test content.\n")

	svc := newTestService(t, dir, kbDir)
	if _, err := svc.BuildIndex(context.Background()); err != nil {
		t.Fatalf("initial build: %v", err)
	}

	fp, ok := svc.provider.(FlushableProvider)
	if !ok {
		t.Skip("provider does not implement FlushableProvider")
	}

	// BuildInMemory marks dirty
	chunks, info, err := svc.buildChunksAndInfo(context.Background())
	if err != nil {
		t.Fatalf("buildChunksAndInfo: %v", err)
	}
	if err := fp.BuildInMemory(context.Background(), chunks, *info); err != nil {
		t.Fatalf("BuildInMemory: %v", err)
	}
	if !fp.IsDirty() {
		t.Fatal("expected dirty after BuildInMemory")
	}

	// Flush clears dirty
	if err := fp.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if fp.IsDirty() {
		t.Fatal("expected not dirty after Flush")
	}
}

func TestWatcherStopFlushesDirty(t *testing.T) {
	dir := t.TempDir()
	kbDir := filepath.Join(dir, "kb")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeTestMD(t, kbDir, "a.md", "# Test\n\nContent here.\n")

	svc := newTestService(t, dir, kbDir)
	if _, err := svc.BuildIndex(context.Background()); err != nil {
		t.Fatalf("initial build: %v", err)
	}

	w, err := NewWatcher(svc,
		WithReindexDebounce(50*time.Millisecond),
		WithFlushDebounce(10*time.Second), // very long — won't fire naturally
	)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	// trigger a change — reindex fires but flush is far away
	writeTestMD(t, kbDir, "a.md", "# Test\n\nModified content.\n")
	time.Sleep(200 * time.Millisecond) // wait for reindex only

	fp, ok := svc.provider.(FlushableProvider)
	if !ok {
		t.Skip("provider does not implement FlushableProvider")
	}
	if !fp.IsDirty() {
		t.Fatal("expected dirty before Stop")
	}

	// Stop should flush
	cancel()
	w.Stop()

	if fp.IsDirty() {
		t.Fatal("expected not dirty after Stop (should have flushed)")
	}
}

func TestIsRelevantEvent(t *testing.T) {
	tests := []struct {
		name string
		op   fsnotify.Op
		path string
		want bool
	}{
		{"write md", fsnotify.Write, "doc.md", true},
		{"create md", fsnotify.Create, "doc.md", true},
		{"remove md", fsnotify.Remove, "doc.md", true},
		{"chmod only md", fsnotify.Chmod, "doc.md", false},
		{"write txt", fsnotify.Write, "doc.txt", false},
		{"write json", fsnotify.Write, "data.json", false},
		{"create MD upper", fsnotify.Create, "DOC.MD", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := fsnotify.Event{Name: tt.path, Op: tt.op}
			if got := isRelevantEvent(ev); got != tt.want {
				t.Errorf("isRelevantEvent(%v) = %v, want %v", ev, got, tt.want)
			}
		})
	}
}

// helpers

func writeTestMD(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestService(t *testing.T, workspace, kbDir string) *Service {
	t.Helper()
	indexDir := filepath.Join(workspace, ".rag")
	rcfg := config.DefaultConfig().Tools.RAG
	rcfg.KBRoot = kbDir
	rcfg.IndexRoot = indexDir
	rcfg.IndexProvider = "comet"

	return NewService(workspace, rcfg, config.ProvidersConfig{})
}

func defaultTestRAGConfig() config.RAGToolsConfig {
	return config.RAGToolsConfig{
		QueueSize:            16,
		Concurrency:          3,
		ChunkSoftBytes:       4096,
		ChunkHardBytes:       8192,
		DocumentHardBytes:    10 * 1024 * 1024,
		MaxChunksPerDocument: 2000,
		DefaultProfileID:     "default_research",
	}
}
