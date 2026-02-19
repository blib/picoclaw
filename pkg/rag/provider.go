package rag

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
)

type ProviderCapabilities struct {
	Semantic bool
}

// ProviderSearchOptions keeps provider-side retrieval bounded so service-level
// ranking remains predictable even when backends differ.
type ProviderSearchOptions struct {
	Limit int
	Mode  SearchMode
}

// ProviderHit carries backend-native scores forward so profile math can stay
// centralized and auditable in the service layer.
type ProviderHit struct {
	Chunk         IndexedChunk
	LexicalScore  float64
	SemanticScore float64
}

// ProviderSearchResult bundles candidates with index metadata to keep responses
// traceable to a concrete index build across providers.
type ProviderSearchResult struct {
	IndexInfo IndexInfo
	Hits      []ProviderHit
}

// IndexProvider isolates index and candidate generation concerns so storage and
// search engines can be swapped without changing public RAG APIs.
type IndexProvider interface {
	Name() string
	Capabilities() ProviderCapabilities
	Build(ctx context.Context, chunks []IndexedChunk, info IndexInfo) error
	Search(ctx context.Context, query string, opts ProviderSearchOptions) (*ProviderSearchResult, error)
	FetchChunk(ctx context.Context, sourcePath string, chunkOrdinal int) (*IndexedChunk, error)
	LoadIndexInfo(ctx context.Context) (*IndexInfo, error)
}

func newIndexProvider(workspace string, cfg config.RAGToolsConfig, indexRoot string, embedder Embedder) (IndexProvider, error) {
	id := strings.ToLower(strings.TrimSpace(cfg.IndexProvider))
	if id == "" {
		id = "simple"
	}

	switch id {
	case "simple", "json":
		return newSimpleProvider(indexRoot), nil
	case "comet":
		return newCometProvider(indexRoot, embedder), nil
	default:
		return nil, fmt.Errorf("unsupported rag index_provider: %s", cfg.IndexProvider)
	}
}
