package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wizenheimer/comet"
)

type cometProvider struct {
	stateDir string
	embedder Embedder

	chunks  []IndexedChunk
	txtIdx  *comet.BM25SearchIndex
	hybrid  comet.HybridSearchIndex
	hasVecs bool
	vecDims int
}

func newCometProvider(indexRoot string, embedder Embedder) IndexProvider {
	return &cometProvider{
		stateDir: filepath.Join(indexRoot, "state"),
		embedder: embedder,
	}
}

func (p *cometProvider) Name() string { return "comet" }

func (p *cometProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{Semantic: p.embedder != nil}
}

func (p *cometProvider) Build(ctx context.Context, chunks []IndexedChunk, info IndexInfo) error {
	if err := os.MkdirAll(p.stateDir, 0o755); err != nil {
		return err
	}

	p.chunks = chunks
	p.hasVecs = false

	var vectors [][]float32
	if p.embedder != nil && len(chunks) > 0 {
		const batchSize = 64
		allVecs := make([][]float32, 0, len(chunks))
		for start := 0; start < len(chunks); start += batchSize {
			end := start + batchSize
			if end > len(chunks) {
				end = len(chunks)
			}
			texts := make([]string, end-start)
			for i, c := range chunks[start:end] {
				texts[i] = c.Text
			}
			vecs, err := p.embedder.Embed(ctx, texts)
			if err != nil {
				return fmt.Errorf("embed batch %d-%d: %w", start, end, err)
			}
			allVecs = append(allVecs, vecs...)
		}
		vectors = allVecs
		p.vecDims = p.embedder.Dims()
		p.hasVecs = true
	}

	if err := p.buildIndexes(chunks, vectors); err != nil {
		return err
	}

	store := IndexStore{Info: info, Chunks: chunks}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(p.chunksPath(), data, 0o644); err != nil {
		return err
	}

	if len(vectors) > 0 {
		vdata, err := json.Marshal(vectors)
		if err != nil {
			return err
		}
		if err := os.WriteFile(p.vectorsPath(), vdata, 0o644); err != nil {
			return err
		}
	} else {
		os.Remove(p.vectorsPath())
	}

	return nil
}

func (p *cometProvider) buildIndexes(chunks []IndexedChunk, vectors [][]float32) error {
	txtIdx := comet.NewBM25SearchIndex()
	for i, c := range chunks {
		if err := txtIdx.Add(uint32(i), c.Text); err != nil {
			return fmt.Errorf("bm25 add chunk %d: %w", i, err)
		}
	}
	p.txtIdx = txtIdx

	var vecIdx comet.VectorIndex
	if len(vectors) > 0 && p.vecDims > 0 {
		flat, err := comet.NewFlatIndex(p.vecDims, comet.Cosine)
		if err != nil {
			return fmt.Errorf("comet flat index: %w", err)
		}
		for i, vec := range vectors {
			node := comet.NewVectorNodeWithID(uint32(i), vec)
			if err := flat.Add(*node); err != nil {
				return fmt.Errorf("flat add %d: %w", i, err)
			}
		}
		vecIdx = flat
	}

	metaIdx := comet.NewRoaringMetadataIndex()
	p.hybrid = comet.NewHybridSearchIndex(vecIdx, txtIdx, metaIdx)
	return nil
}

func (p *cometProvider) Search(ctx context.Context, query string, opts ProviderSearchOptions) (*ProviderSearchResult, error) {
	if err := p.ensureLoaded(); err != nil {
		return nil, err
	}

	if len(p.chunks) == 0 {
		info, _ := p.LoadIndexInfo(ctx)
		if info == nil {
			return nil, ErrIndexNotBuilt
		}
		return &ProviderSearchResult{IndexInfo: *info}, nil
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 200
	}

	if p.hasVecs && opts.Mode != ModeKeywordOnly {
		return p.searchHybrid(ctx, query, limit)
	}

	return p.searchTextOnly(ctx, query, limit)
}

func (p *cometProvider) searchTextOnly(_ context.Context, query string, limit int) (*ProviderSearchResult, error) {
	info, err := p.LoadIndexInfo(context.Background())
	if err != nil {
		return nil, err
	}

	results, err := p.txtIdx.NewSearch().
		WithQuery(query).
		WithK(limit).
		Execute()
	if err != nil {
		return nil, err
	}

	hits := make([]ProviderHit, 0, len(results))
	for _, r := range results {
		id := int(r.Id)
		if id < 0 || id >= len(p.chunks) {
			continue
		}
		hits = append(hits, ProviderHit{
			Chunk:        p.chunks[id],
			LexicalScore: float64(r.Score),
		})
	}

	return &ProviderSearchResult{IndexInfo: *info, Hits: hits}, nil
}

func (p *cometProvider) searchHybrid(ctx context.Context, query string, limit int) (*ProviderSearchResult, error) {
	info, err := p.LoadIndexInfo(ctx)
	if err != nil {
		return nil, err
	}

	qvecs, err := p.embedder.Embed(ctx, []string{query})
	if err != nil {
		return p.searchTextOnly(ctx, query, limit)
	}

	results, err := p.hybrid.NewSearch().
		WithVector(qvecs[0]).
		WithText(query).
		WithK(limit).
		WithFusionKind(comet.ReciprocalRankFusion).
		Execute()
	if err != nil {
		return nil, err
	}

	hits := make([]ProviderHit, 0, len(results))
	for _, r := range results {
		id := int(r.ID)
		if id < 0 || id >= len(p.chunks) {
			continue
		}
		hits = append(hits, ProviderHit{
			Chunk:        p.chunks[id],
			LexicalScore: r.Score,
		})
	}

	return &ProviderSearchResult{IndexInfo: *info, Hits: hits}, nil
}

func (p *cometProvider) FetchChunk(_ context.Context, sourcePath string, chunkOrdinal int) (*IndexedChunk, error) {
	if err := p.ensureLoaded(); err != nil {
		return nil, err
	}
	norm := filepath.ToSlash(sourcePath)
	for i := range p.chunks {
		if p.chunks[i].SourcePath == norm && p.chunks[i].ChunkOrdinal == chunkOrdinal {
			c := p.chunks[i]
			return &c, nil
		}
	}
	return nil, os.ErrNotExist
}

func (p *cometProvider) LoadIndexInfo(_ context.Context) (*IndexInfo, error) {
	store, err := p.loadStore()
	if err != nil {
		return nil, err
	}
	info := store.Info
	return &info, nil
}

func (p *cometProvider) ensureLoaded() error {
	if len(p.chunks) > 0 {
		return nil
	}
	store, err := p.loadStore()
	if err != nil {
		return err
	}
	p.chunks = store.Chunks

	var vectors [][]float32
	if data, err := os.ReadFile(p.vectorsPath()); err == nil {
		if err := json.Unmarshal(data, &vectors); err == nil && len(vectors) == len(p.chunks) {
			p.hasVecs = true
			if len(vectors) > 0 {
				p.vecDims = len(vectors[0])
			}
		}
	}

	return p.buildIndexes(p.chunks, vectors)
}

func (p *cometProvider) loadStore() (*IndexStore, error) {
	data, err := os.ReadFile(p.chunksPath())
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

func (p *cometProvider) chunksPath() string {
	return filepath.Join(p.stateDir, "index.json")
}

func (p *cometProvider) vectorsPath() string {
	return filepath.Join(p.stateDir, "vectors.json")
}
