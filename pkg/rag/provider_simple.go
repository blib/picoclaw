package rag

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type simpleProvider struct {
	indexFile string
}

func newSimpleProvider(indexRoot string) IndexProvider {
	return &simpleProvider{
		indexFile: filepath.Join(indexRoot, "state", "index.json"),
	}
}

func (p *simpleProvider) Name() string {
	return "simple"
}

func (p *simpleProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{Semantic: false}
}

func (p *simpleProvider) Build(_ context.Context, chunks []IndexedChunk, info IndexInfo) error {
	if err := os.MkdirAll(filepath.Dir(p.indexFile), 0o755); err != nil {
		return err
	}

	store := IndexStore{
		Info:   info,
		Chunks: chunks,
	}
	b, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.indexFile, b, 0o644)
}

func (p *simpleProvider) Search(_ context.Context, query string, opts ProviderSearchOptions) (*ProviderSearchResult, error) {
	store, err := p.loadStore()
	if err != nil {
		return nil, err
	}

	queryTokens := tokenize(query)
	hits := make([]ProviderHit, 0, len(store.Chunks))
	for _, chunk := range store.Chunks {
		score := lexicalScore(queryTokens, chunk.Text)
		if score <= 0 {
			continue
		}
		hits = append(hits, ProviderHit{
			Chunk:        chunk,
			LexicalScore: score,
		})
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].LexicalScore == hits[j].LexicalScore {
			if hits[i].Chunk.SourcePath == hits[j].Chunk.SourcePath {
				return hits[i].Chunk.ChunkOrdinal < hits[j].Chunk.ChunkOrdinal
			}
			return hits[i].Chunk.SourcePath < hits[j].Chunk.SourcePath
		}
		return hits[i].LexicalScore > hits[j].LexicalScore
	})

	limit := opts.Limit
	if limit <= 0 || limit > len(hits) {
		limit = len(hits)
	}

	return &ProviderSearchResult{
		IndexInfo: store.Info,
		Hits:      hits[:limit],
	}, nil
}

func (p *simpleProvider) FetchChunk(_ context.Context, sourcePath string, chunkOrdinal int) (*IndexedChunk, error) {
	store, err := p.loadStore()
	if err != nil {
		return nil, err
	}

	normalizedPath := filepath.ToSlash(strings.TrimSpace(sourcePath))
	for _, chunk := range store.Chunks {
		if chunk.SourcePath == normalizedPath && chunk.ChunkOrdinal == chunkOrdinal {
			c := chunk
			return &c, nil
		}
	}
	return nil, os.ErrNotExist
}

func (p *simpleProvider) LoadIndexInfo(_ context.Context) (*IndexInfo, error) {
	store, err := p.loadStore()
	if err != nil {
		return nil, err
	}
	info := store.Info
	return &info, nil
}

func (p *simpleProvider) loadStore() (*IndexStore, error) {
	data, err := os.ReadFile(p.indexFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrIndexNotBuilt
		}
		return nil, err
	}

	var store IndexStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, err
	}
	return &store, nil
}
