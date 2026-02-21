package eval

import (
	"context"
	"sync"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/rag"
)

// EmbedderStats holds accumulated cost/usage statistics for embedding during
// an evaluation run.
type EmbedderStats struct {
	TotalCalls      int `json:"total_calls"`
	TotalTexts      int `json:"total_texts"`
	TotalChars      int `json:"total_chars"`        // input characters
	EstimatedTokens int `json:"estimated_tokens"`   // chars/4 rough estimate
	APITokens       int `json:"api_tokens"`         // as reported by API (0 if unavailable)
	Dims            int `json:"dims"`               // embedding dimensions
}

// BestTokenCount returns APITokens if available, otherwise EstimatedTokens.
func (s EmbedderStats) BestTokenCount() int {
	if s.APITokens > 0 {
		return s.APITokens
	}
	return s.EstimatedTokens
}

// CountingEmbedder wraps a rag.Embedder and accumulates usage statistics.
// Safe for concurrent use.
type CountingEmbedder struct {
	inner rag.Embedder

	mu          sync.Mutex
	totalCalls  int
	totalTexts  int
	totalChars  int
}

// NewCountingEmbedder wraps inner and returns a counting proxy.
func NewCountingEmbedder(inner rag.Embedder) *CountingEmbedder {
	return &CountingEmbedder{inner: inner}
}

func (c *CountingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	chars := 0
	for _, t := range texts {
		chars += utf8.RuneCountInString(t)
	}

	vecs, err := c.inner.Embed(ctx, texts)

	// Track even on error — the API call was made.
	c.mu.Lock()
	c.totalCalls++
	c.totalTexts += len(texts)
	c.totalChars += chars
	c.mu.Unlock()

	return vecs, err
}

func (c *CountingEmbedder) Dims() int {
	return c.inner.Dims()
}

// Stats returns a snapshot of accumulated usage statistics.
// If the underlying embedder implements rag.UsageTracker, API-reported
// token counts are included.
func (c *CountingEmbedder) Stats() EmbedderStats {
	c.mu.Lock()
	calls := c.totalCalls
	texts := c.totalTexts
	chars := c.totalChars
	c.mu.Unlock()

	s := EmbedderStats{
		TotalCalls:      calls,
		TotalTexts:      texts,
		TotalChars:      chars,
		EstimatedTokens: chars / 4, // rough English approximation
		Dims:            c.inner.Dims(),
	}

	if ut, ok := c.inner.(rag.UsageTracker); ok {
		usage := ut.Usage()
		s.APITokens = usage.TotalTokens
	}

	return s
}

// Reset clears accumulated counters.
func (c *CountingEmbedder) Reset() {
	c.mu.Lock()
	c.totalCalls = 0
	c.totalTexts = 0
	c.totalChars = 0
	c.mu.Unlock()

	if ut, ok := c.inner.(rag.UsageTracker); ok {
		ut.ResetUsage()
	}
}
