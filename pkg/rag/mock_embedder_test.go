package rag

import (
	"context"
	"hash/fnv"
	"math"
	"strings"
)

const mockEmbedDim = 64

// mockEmbedder is a deterministic, stateless test embedder. Each token is
// hashed into a fixed-size vector (64 dims). Similar texts produce similar
// vectors because shared tokens land in the same buckets. No API keys, no
// growing vocabulary, no ordering effects.
type mockEmbedder struct{}

func newMockEmbedder() *mockEmbedder { return &mockEmbedder{} }

func (m *mockEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vecs := make([][]float32, len(texts))
	for i, text := range texts {
		vecs[i] = hashEmbed(text)
	}
	return vecs, nil
}

func (m *mockEmbedder) Dims() int { return mockEmbedDim }

func hashEmbed(text string) []float32 {
	vec := make([]float32, mockEmbedDim)
	toks := mockTokenize(text)
	for _, tok := range toks {
		h := fnv.New32a()
		h.Write([]byte(tok))
		bucket := int(h.Sum32()) % mockEmbedDim
		vec[bucket]++
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
	return vec
}

func mockTokenize(s string) []string {
	parts := tokenSplitRE.Split(strings.ToLower(s), -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" && len(p) > 1 {
			out = append(out, p)
		}
	}
	return out
}
