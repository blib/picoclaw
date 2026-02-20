package rag

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/wizenheimer/comet"
)

type cometProvider struct {
	mu       sync.RWMutex
	embedder Embedder
	store    *Store

	chunks  []IndexedChunk
	vectors [][]float32
	info    *IndexInfo
	txtIdx  *comet.BM25SearchIndex
	hybrid  comet.HybridSearchIndex
	hasVecs bool
	vecDims int
	dirty   bool
}

func newCometProvider(indexRoot string, embedder Embedder) (*cometProvider, error) {
	stateDir := filepath.Join(indexRoot, "state")
	store, err := OpenStore(stateDir)
	if err != nil {
		return nil, err
	}
	return &cometProvider{
		embedder: embedder,
		store:    store,
	}, nil
}

// Close releases the bbolt database. Safe to call multiple times.
func (p *cometProvider) Close() error {
	if p.store != nil {
		err := p.store.Close()
		p.store = nil
		return err
	}
	return nil
}

func (p *cometProvider) Name() string { return "comet" }

func (p *cometProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{Semantic: p.embedder != nil}
}

// Build rebuilds in-memory indexes and flushes to disk in one shot.
// Used by BuildIndex (CLI, one-shot mode). For watched mode, use
// BuildInMemory + Flush.
func (p *cometProvider) Build(ctx context.Context, chunks []IndexedChunk, info IndexInfo) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.embedAndBuild(ctx, chunks, info); err != nil {
		return err
	}
	return p.flushLocked()
}

// BuildInMemory rebuilds in-memory indexes without touching disk.
// Marks the provider as dirty so Flush must be called eventually.
func (p *cometProvider) BuildInMemory(ctx context.Context, chunks []IndexedChunk, info IndexInfo) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.embedAndBuild(ctx, chunks, info); err != nil {
		return err
	}
	p.dirty = true
	return p.store.SetDirty(true)
}

// Flush persists current in-memory state to disk and clears dirty flag.
func (p *cometProvider) Flush() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.flushLocked()
}

// flushLocked performs the flush while the caller already holds p.mu.
func (p *cometProvider) flushLocked() error {
	if p.info == nil {
		return nil
	}
	if err := p.store.SaveIndex(*p.info, p.chunks); err != nil {
		return err
	}
	if err := p.store.SaveVectors(p.vectors); err != nil {
		return err
	}
	if err := p.store.SetDirty(false); err != nil {
		return err
	}
	p.dirty = false
	return nil
}

// Invalidate clears in-memory state so the next operation reloads from disk.
func (p *cometProvider) Invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.chunks = nil
	p.vectors = nil
	p.info = nil
	p.txtIdx = nil
	p.hybrid = nil
	p.hasVecs = false
	p.dirty = false
}

// IsDirty returns whether in-memory state is ahead of disk.
func (p *cometProvider) IsDirty() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.dirty
}

func (p *cometProvider) embedAndBuild(ctx context.Context, chunks []IndexedChunk, info IndexInfo) error {
	p.chunks = chunks
	p.hasVecs = false
	p.vectors = nil
	infoCopy := info
	p.info = &infoCopy

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
		p.vectors = allVecs
		p.vecDims = p.embedder.Dims()
		p.hasVecs = true
	}

	return p.buildIndexes(chunks, p.vectors)
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

	p.mu.RLock()
	defer p.mu.RUnlock()

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

func (p *cometProvider) searchTextOnly(ctx context.Context, query string, limit int) (*ProviderSearchResult, error) {
	info, err := p.LoadIndexInfo(ctx)
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
		res, fallbackErr := p.searchTextOnly(ctx, query, limit)
		if fallbackErr != nil {
			return nil, fallbackErr
		}
		res.IndexInfo.Warnings = append(res.IndexInfo.Warnings,
			fmt.Sprintf("semantic_fallback_to_keyword: %v", err))
		return res, nil
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
			Chunk:      p.chunks[id],
			FusedScore: r.Score,
		})
	}

	return &ProviderSearchResult{IndexInfo: *info, Hits: hits}, nil
}

func (p *cometProvider) FetchChunk(_ context.Context, sourcePath string, chunkOrdinal int) (*IndexedChunk, error) {
	if err := p.ensureLoaded(); err != nil {
		return nil, err
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

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
	return p.store.LoadIndexInfo()
}

// ErrDirtyIndex signals that the on-disk index was not cleanly flushed and
// must be rebuilt from source files before it can be used.
var ErrDirtyIndex = errors.New("rag index dirty: rebuild required")

// ensureLoaded loads from disk if needed. Acquires a write lock internally
// only when loading is required, eliminating the unsafe RLock→Lock upgrade.
// Callers should NOT hold any lock when calling this.
func (p *cometProvider) ensureLoaded() error {
	p.mu.RLock()
	if len(p.chunks) > 0 {
		p.mu.RUnlock()
		return nil
	}
	p.mu.RUnlock()

	// Slow path: take write lock and load from disk.
	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock.
	if len(p.chunks) > 0 {
		return nil
	}

	if p.store.IsDirty() {
		return ErrDirtyIndex
	}
	chunks, err := p.store.LoadChunks()
	if err != nil {
		return err
	}
	vectors, err := p.store.LoadVectors()
	if err != nil {
		return err
	}
	if len(chunks) == 0 {
		return nil
	}
	p.chunks = chunks
	p.vectors = vectors

	if len(vectors) == len(p.chunks) {
		p.hasVecs = true
		if len(vectors) > 0 {
			p.vecDims = len(vectors[0])
		}
	}

	// Detect embedding model change: stored vectors have different
	// dimensions than the current embedder would produce.
	if p.hasVecs && p.embedder != nil && p.embedder.Dims() > 0 && p.vecDims != p.embedder.Dims() {
		p.chunks = nil
		p.vectors = nil
		p.hasVecs = false
		return fmt.Errorf("rag index embedding dimensions mismatch: stored=%d, embedder=%d; rebuild required", p.vecDims, p.embedder.Dims())
	}

	return p.buildIndexes(p.chunks, vectors)
}
