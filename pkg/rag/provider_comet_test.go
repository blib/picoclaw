package rag

import (
	"context"
	"testing"
	"time"
)

func mustCometProvider(t *testing.T, dir string, embedder Embedder) *cometProvider {
	t.Helper()
	p, err := newCometProvider(dir, embedder)
	if err != nil {
		t.Fatalf("newCometProvider: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func testChunks() []IndexedChunk {
	return []IndexedChunk{
		{SourcePath: "notes/meeting.md", ChunkOrdinal: 1, Text: "We discussed caching strategy and invalidation policy for api responses", Snippet: "caching strategy..."},
		{SourcePath: "notes/meeting.md", ChunkOrdinal: 2, Text: "The database migration requires downtime window of two hours", Snippet: "database migration..."},
		{SourcePath: "notes/design.md", ChunkOrdinal: 1, Text: "Cache eviction uses least recently used algorithm with ttl fallback", Snippet: "cache eviction..."},
		{SourcePath: "notes/ops.md", ChunkOrdinal: 1, Text: "Deploy the service to production using blue green deployment", Snippet: "deploy service..."},
	}
}

func testInfo() IndexInfo {
	return IndexInfo{
		IndexVersion:   "1",
		IndexState:     "ready",
		IndexProvider:  "comet",
		BuiltAt:        time.Now().UTC().Format(time.RFC3339),
		TotalDocuments: 3,
		TotalChunks:    4,
	}
}

func TestCometBM25Only(t *testing.T) {
	dir := t.TempDir()
	p := mustCometProvider(t, dir, nil)

	chunks := testChunks()
	if err := p.Build(context.Background(), chunks, testInfo()); err != nil {
		t.Fatalf("Build: %v", err)
	}

	res, err := p.Search(context.Background(), "caching", ProviderSearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("expected hits for 'caching'")
	}
	// chunks 0 and 2 mention cache/caching — one of them should be top hit
	top := res.Hits[0].Chunk.SourcePath
	if top != "notes/meeting.md" && top != "notes/design.md" {
		t.Fatalf("top hit %q not cache-related", top)
	}
}

func TestCometHybridSearch(t *testing.T) {
	dir := t.TempDir()
	emb := newBOWEmbedder()
	p := mustCometProvider(t, dir, emb)

	chunks := testChunks()
	if err := p.Build(context.Background(), chunks, testInfo()); err != nil {
		t.Fatalf("Build: %v", err)
	}

	if !p.Capabilities().Semantic {
		t.Fatal("expected Semantic capability with embedder")
	}

	// query uses only words present in chunks so BOW vocab doesn't grow
	res, err := p.Search(context.Background(), "caching strategy", ProviderSearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("expected hybrid hits")
	}
	if res.Hits[0].Chunk.SourcePath != "notes/meeting.md" || res.Hits[0].Chunk.ChunkOrdinal != 1 {
		t.Fatalf("expected meeting.md chunk 1 as top hit, got %s#%d",
			res.Hits[0].Chunk.SourcePath, res.Hits[0].Chunk.ChunkOrdinal)
	}
}

func TestCometPersistence(t *testing.T) {
	dir := t.TempDir()
	emb := newBOWEmbedder()

	// build with one provider instance
	p1 := mustCometProvider(t, dir, emb)
	chunks := testChunks()
	if err := p1.Build(context.Background(), chunks, testInfo()); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// close p1 to release bbolt lock before creating a fresh instance
	if err := p1.Close(); err != nil {
		t.Fatalf("Close p1: %v", err)
	}

	// load from disk with a fresh provider (simulates restart)
	p2 := mustCometProvider(t, dir, emb)
	info, err := p2.LoadIndexInfo(context.Background())
	if err != nil {
		t.Fatalf("LoadIndexInfo: %v", err)
	}
	if info.TotalChunks != 4 {
		t.Fatalf("expected 4 chunks, got %d", info.TotalChunks)
	}

	res, err := p2.Search(context.Background(), "database migration", ProviderSearchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("Search after reload: %v", err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("expected hits after reload")
	}
	if res.Hits[0].Chunk.SourcePath != "notes/meeting.md" || res.Hits[0].Chunk.ChunkOrdinal != 2 {
		t.Fatalf("expected meeting.md chunk 2 as top hit, got %s#%d",
			res.Hits[0].Chunk.SourcePath, res.Hits[0].Chunk.ChunkOrdinal)
	}
}

func TestCometFetchChunk(t *testing.T) {
	dir := t.TempDir()
	p := mustCometProvider(t, dir, nil)

	chunks := testChunks()
	if err := p.Build(context.Background(), chunks, testInfo()); err != nil {
		t.Fatalf("Build: %v", err)
	}

	c, err := p.FetchChunk(context.Background(), "notes/design.md", 1)
	if err != nil {
		t.Fatalf("FetchChunk: %v", err)
	}
	if c.Text != chunks[2].Text {
		t.Fatalf("wrong chunk text: %q", c.Text)
	}

	_, err = p.FetchChunk(context.Background(), "notes/nonexistent.md", 1)
	if err == nil {
		t.Fatal("expected error for missing chunk")
	}
}

func TestCometKeywordOnlyMode(t *testing.T) {
	dir := t.TempDir()
	emb := newBOWEmbedder()
	p := mustCometProvider(t, dir, emb)

	chunks := testChunks()
	if err := p.Build(context.Background(), chunks, testInfo()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !p.hasVecs {
		t.Fatal("expected vectors to be built")
	}

	// force keyword-only even though vectors exist
	res, err := p.Search(context.Background(), "deploy", ProviderSearchOptions{
		Limit: 5,
		Mode:  ModeKeywordOnly,
	})
	if err != nil {
		t.Fatalf("Search keyword-only: %v", err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("expected keyword hits for 'deploy'")
	}
	if res.Hits[0].Chunk.SourcePath != "notes/ops.md" {
		t.Fatalf("expected ops.md as top hit, got %s", res.Hits[0].Chunk.SourcePath)
	}
}
