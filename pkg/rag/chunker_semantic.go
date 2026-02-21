package rag

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// SemanticDriftChunker detects topic boundaries by comparing embeddings of
// consecutive semantic units. When the cosine similarity between adjacent
// units drops below DriftThreshold, a chunk boundary is inserted.
//
// The embedder is called once per document with all units batched. The unit
// embeddings are throwaway — final chunks are re-embedded by the index
// pipeline for search.
//
// When the embedder is nil or fails, the chunker falls back to paragraph-
// packing behavior (degrade gracefully).
type SemanticDriftChunker struct {
	MaxSize        int
	DriftThreshold float64
	Embedder       Embedder
}

func (c SemanticDriftChunker) Name() string { return "semantic" }

func (c SemanticDriftChunker) Chunk(content string) []ChunkLocAndText {
	maxSize := c.MaxSize
	if maxSize <= 0 {
		maxSize = 4096
	}
	threshold := c.DriftThreshold
	if threshold <= 0 {
		threshold = 0.5
	}

	allUnits := ParseMarkdownUnits(content)

	// filter out headings for embedding, keep for heading path
	type contentUnit struct {
		unit    MarkdownUnit
		origIdx int
	}
	contentUnits := make([]contentUnit, 0, len(allUnits))
	for i, u := range allUnits {
		if u.Type == UnitHeading {
			continue
		}
		contentUnits = append(contentUnits, contentUnit{unit: u, origIdx: i})
	}

	if len(contentUnits) == 0 {
		return nil
	}

	// try to get embeddings for pairwise comparison
	texts := make([]string, len(contentUnits))
	for i, cu := range contentUnits {
		texts[i] = cu.unit.Text
	}
	similarities := c.computePairwiseSims(texts)

	// build chunks
	chunks := make([]ChunkLocAndText, 0, 32)
	var buf strings.Builder
	startByte := 0
	endByte := 0
	headingPath := ""

	flush := func() {
		text := strings.TrimSpace(buf.String())
		if text != "" {
			chunks = append(chunks, ChunkLocAndText{
				Loc: ChunkLoc{
					HeadingPath: headingPath,
					StartByte:   startByte,
					EndByte:     endByte,
				},
				Text: text,
			})
		}
		buf.Reset()
	}

	for i, cu := range contentUnits {
		uSize := len(cu.unit.Text)
		sep := 0
		if buf.Len() > 0 {
			sep = 1
		}

		// check if we should start a new chunk
		shouldSplit := false

		if i > 0 && similarities != nil {
			// pairwise similarity between unit i-1 and unit i
			if similarities[i-1] < threshold {
				shouldSplit = true
			}
		}

		// hard split on size — would adding this unit exceed maxSize?
		if buf.Len()+sep+uSize > maxSize && buf.Len() > 0 {
			shouldSplit = true
		}

		if shouldSplit && buf.Len() > 0 {
			flush()
		}

		if buf.Len() == 0 {
			headingPath = HeadingPathAt(allUnits, cu.origIdx)
			startByte = cu.unit.StartByte
		}
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(cu.unit.Text)
		endByte = cu.unit.EndByte

		// force flush if this single unit already fills the chunk
		if buf.Len() >= maxSize {
			flush()
		}
	}
	flush()
	return chunks
}

// computePairwiseSims returns cosine similarities between consecutive unit
// texts. Result[i] = similarity(texts[i], texts[i+1]). Returns nil if
// embedder is unavailable or fails.
func (c SemanticDriftChunker) computePairwiseSims(texts []string) []float64 {
	if c.Embedder == nil || len(texts) < 2 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	vectors, err := c.Embedder.Embed(ctx, texts)
	if err != nil {
		logger.Warn("rag: semantic drift chunker embed failed, falling back to size-only: " + err.Error())
		return nil
	}

	if len(vectors) != len(texts) {
		logger.Warn("rag: semantic drift chunker: vector count mismatch")
		return nil
	}

	sims := make([]float64, len(texts)-1)
	for i := 0; i < len(texts)-1; i++ {
		sims[i] = cosineSim(vectors[i], vectors[i+1])
	}
	return sims
}

func cosineSim(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}
