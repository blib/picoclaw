package rag

import (
	"fmt"

	"github.com/sipeed/picoclaw/pkg/config"
)

// NewChunkerFromConfig creates a Chunker based on the strategy name in cfg.
// The embedder parameter is only used by the "semantic" strategy and may be
// nil for all others.
func NewChunkerFromConfig(cfg config.RAGToolsConfig, embedder Embedder) (Chunker, error) {
	strategy := cfg.ChunkStrategy
	if strategy == "" {
		strategy = "markdown"
	}

	switch strategy {
	case "fixed":
		return FixedSizeChunker{
			Size:    cfg.ChunkHardBytes,
			Overlap: cfg.ChunkOverlapBytes,
		}, nil

	case "markdown":
		return MarkdownChunker{
			SoftLimit: cfg.ChunkSoftBytes,
			HardLimit: cfg.ChunkHardBytes,
		}, nil

	case "paragraph":
		return ParagraphPacker{
			MaxSize: cfg.ChunkSoftBytes,
		}, nil

	case "sliding":
		return UnitSlidingWindow{
			WindowUnits: cfg.SlidingWindowUnits,
			StrideUnits: cfg.SlidingStrideUnits,
			MaxBytes:    cfg.ChunkSoftBytes,
		}, nil

	case "hierarchical":
		childBytes := cfg.HierarchicalChildBytes
		if childBytes <= 0 {
			childBytes = cfg.ChunkSoftBytes / 4
			if childBytes <= 0 {
				childBytes = 1024
			}
		}
		return HierarchicalChunker{
			ParentMaxSize: cfg.ChunkSoftBytes,
			ChildMaxSize:  childBytes,
		}, nil

	case "semantic":
		return SemanticDriftChunker{
			MaxSize:        cfg.ChunkSoftBytes,
			DriftThreshold: cfg.SemanticDriftThreshold,
			Embedder:       embedder,
		}, nil

	default:
		return nil, fmt.Errorf("unknown chunk strategy: %q (valid: fixed, markdown, paragraph, sliding, hierarchical, semantic)", strategy)
	}
}
