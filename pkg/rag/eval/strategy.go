package eval

import (
	"fmt"

	"github.com/sipeed/picoclaw/pkg/rag"

	"gopkg.in/yaml.v3"
)

// Strategy defines a retrieval configuration to evaluate. Each strategy
// varies one or more axes: profile, chunker, search mode, embedder.
type Strategy struct {
	Name           string         `yaml:"name"`
	ProfileID      string         `yaml:"profile"`
	ChunkerName    string         `yaml:"chunker"`
	SearchMode     rag.SearchMode `yaml:"mode"`
	ChunkSoftBytes int            `yaml:"chunk_soft"`
	ChunkHardBytes int            `yaml:"chunk_hard"`
	EmbedderName   string         `yaml:"embedder"` // "none", "bow", "openai", etc.
}

// strategiesFile is the YAML schema for strategy configuration.
type strategiesFile struct {
	Strategies []Strategy `yaml:"strategies"`
}

// ParseStrategies reads a YAML file defining evaluation strategies.
func ParseStrategies(data []byte) ([]Strategy, error) {
	var sf strategiesFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("parse strategies: %w", err)
	}
	for i := range sf.Strategies {
		if sf.Strategies[i].Name == "" {
			sf.Strategies[i].Name = fmt.Sprintf("strategy-%d", i+1)
		}
		if sf.Strategies[i].ProfileID == "" {
			sf.Strategies[i].ProfileID = "default_research"
		}
		if sf.Strategies[i].ChunkerName == "" {
			sf.Strategies[i].ChunkerName = "markdown"
		}
		if sf.Strategies[i].SearchMode == "" {
			sf.Strategies[i].SearchMode = rag.ModeHybrid
		}
	}
	return sf.Strategies, nil
}

// DefaultStrategies returns a sensible baseline set for quick evaluation.
func DefaultStrategies() []Strategy {
	return []Strategy{
		{
			Name:           "bm25-default",
			ProfileID:      "default_research",
			ChunkerName:    "markdown",
			SearchMode:     rag.ModeKeywordOnly,
			ChunkSoftBytes: 4096,
			ChunkHardBytes: 8192,
			EmbedderName:   "none",
		},
		{
			Name:           "bm25-small-chunks",
			ProfileID:      "default_research",
			ChunkerName:    "markdown",
			SearchMode:     rag.ModeKeywordOnly,
			ChunkSoftBytes: 512,
			ChunkHardBytes: 1024,
			EmbedderName:   "none",
		},
		{
			Name:           "bm25-fixed-1k",
			ProfileID:      "default_research",
			ChunkerName:    "fixed-size",
			SearchMode:     rag.ModeKeywordOnly,
			ChunkSoftBytes: 1024,
			ChunkHardBytes: 1024,
			EmbedderName:   "none",
		},
	}
}

// ResolveChunker returns a rag.Chunker for the given strategy.
func ResolveChunker(s Strategy) rag.Chunker {
	soft := s.ChunkSoftBytes
	if soft <= 0 {
		soft = 4096
	}
	hard := s.ChunkHardBytes
	if hard <= 0 {
		hard = 8192
	}

	switch s.ChunkerName {
	case "fixed-size":
		return rag.FixedSizeChunker{Size: soft}
	default:
		return rag.MarkdownChunker{SoftLimit: soft, HardLimit: hard}
	}
}
