package rag

import (
	"context"
	"math"
	"strings"
)

// bowEmbedder is a deterministic bag-of-words embedder for tests. It builds a
// shared vocabulary across all Embed calls and returns sparse float32 vectors.
// No API key needed. Not suitable for production — only for testing the
// ranking/scoring pipeline.
type bowEmbedder struct {
	vocab map[string]int
	dim   int
}

func newBOWEmbedder() *bowEmbedder {
	return &bowEmbedder{
		vocab: make(map[string]int),
		dim:   0,
	}
}

func (b *bowEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	// first pass: grow vocabulary
	for _, text := range texts {
		for _, tok := range bowTokenize(text) {
			if _, ok := b.vocab[tok]; !ok {
				b.vocab[tok] = b.dim
				b.dim++
			}
		}
	}

	// second pass: build TF vectors
	vecs := make([][]float32, len(texts))
	for i, text := range texts {
		vec := make([]float32, b.dim)
		toks := bowTokenize(text)
		if len(toks) == 0 {
			vecs[i] = vec
			continue
		}
		for _, tok := range toks {
			idx := b.vocab[tok]
			vec[idx]++
		}
		// L2 normalize
		var norm float64
		for _, v := range vec {
			norm += float64(v) * float64(v)
		}
		if norm > 0 {
			norm = math.Sqrt(norm)
			for j := range vec {
				vec[j] = float32(float64(vec[j]) / norm)
			}
		}
		vecs[i] = vec
	}
	return vecs, nil
}

func (b *bowEmbedder) Dims() int {
	return b.dim
}

func bowTokenize(s string) []string {
	parts := tokenSplitRE.Split(strings.ToLower(s), -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" && len(p) > 1 {
			out = append(out, p)
		}
	}
	return out
}
