package eval

import (
	"context"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/rag"
)

// fakeEmbedder is a minimal embedder for testing CountingEmbedder.
type fakeEmbedder struct {
	dims int
	mu   sync.Mutex
	calls int
	texts int
	tokens int
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.mu.Lock()
	f.calls++
	f.texts += len(texts)
	f.tokens += len(texts) * 10 // fake: 10 tokens per text
	f.mu.Unlock()

	vecs := make([][]float32, len(texts))
	for i := range vecs {
		vecs[i] = make([]float32, f.dims)
	}
	return vecs, nil
}

func (f *fakeEmbedder) Dims() int { return f.dims }

// Implement rag.UsageTracker so CountingEmbedder picks up API tokens.
func (f *fakeEmbedder) Usage() rag.EmbedderUsage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return rag.EmbedderUsage{
		TotalCalls:  f.calls,
		TotalTexts:  f.texts,
		TotalTokens: f.tokens,
	}
}

func (f *fakeEmbedder) ResetUsage() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = 0
	f.texts = 0
	f.tokens = 0
}

func TestCountingEmbedder_Basic(t *testing.T) {
	inner := &fakeEmbedder{dims: 128}
	ce := NewCountingEmbedder(inner)

	ctx := context.Background()

	// First call: 3 texts.
	vecs, err := ce.Embed(ctx, []string{"hello world", "foo bar", "test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 3 {
		t.Fatalf("expected 3 vectors, got %d", len(vecs))
	}

	// Second call: 2 texts.
	_, err = ce.Embed(ctx, []string{"alpha", "beta"})
	if err != nil {
		t.Fatal(err)
	}

	stats := ce.Stats()

	if stats.TotalCalls != 2 {
		t.Errorf("TotalCalls = %d, want 2", stats.TotalCalls)
	}
	if stats.TotalTexts != 5 {
		t.Errorf("TotalTexts = %d, want 5", stats.TotalTexts)
	}
	if stats.TotalChars <= 0 {
		t.Errorf("TotalChars = %d, want > 0", stats.TotalChars)
	}
	if stats.EstimatedTokens <= 0 {
		t.Errorf("EstimatedTokens = %d, want > 0", stats.EstimatedTokens)
	}
	if stats.APITokens != 50 { // 5 texts * 10 fake tokens
		t.Errorf("APITokens = %d, want 50", stats.APITokens)
	}
	if stats.Dims != 128 {
		t.Errorf("Dims = %d, want 128", stats.Dims)
	}
}

func TestCountingEmbedder_Reset(t *testing.T) {
	inner := &fakeEmbedder{dims: 64}
	ce := NewCountingEmbedder(inner)

	ctx := context.Background()
	ce.Embed(ctx, []string{"a", "b"})

	ce.Reset()
	stats := ce.Stats()

	if stats.TotalCalls != 0 || stats.TotalTexts != 0 || stats.APITokens != 0 {
		t.Errorf("after Reset, stats should be zero: %+v", stats)
	}
}

func TestCountingEmbedder_BestTokenCount(t *testing.T) {
	// With API tokens available.
	s := EmbedderStats{EstimatedTokens: 100, APITokens: 42}
	if s.BestTokenCount() != 42 {
		t.Errorf("BestTokenCount with API = %d, want 42", s.BestTokenCount())
	}

	// Without API tokens.
	s = EmbedderStats{EstimatedTokens: 100, APITokens: 0}
	if s.BestTokenCount() != 100 {
		t.Errorf("BestTokenCount without API = %d, want 100", s.BestTokenCount())
	}
}

func TestCountingEmbedder_NilInner(t *testing.T) {
	ce := wrapEmbedder(nil)
	if ce != nil {
		t.Error("wrapEmbedder(nil) should return nil")
	}
}
