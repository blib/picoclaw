package rag

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/wizenheimer/comet"
)

type cometProvider struct {
	mu       sync.RWMutex
	embedder Embedder
	store    *Store

	chunks     []IndexedChunk
	vectors    [][]float32
	info       *IndexInfo
	txtIdx     *comet.BM25SearchIndex
	vecIdx     comet.VectorIndex
	hasVecs    bool
	vecDims    int
	dirty      bool
	indexReady bool
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
	if err := p.flushLocked(); err != nil {
		return err
	}
	// Release chunk/vector memory — bbolt + vectors.bin are the source of truth.
	p.chunks = nil
	p.vectors = nil
	return nil
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
	if err := p.flushLocked(); err != nil {
		return err
	}
	// Release chunk/vector memory — bbolt + vectors.bin are the source of truth.
	p.chunks = nil
	p.vectors = nil
	return nil
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
	p.vecIdx = nil
	p.hasVecs = false
	p.dirty = false
	p.indexReady = false
}

// IsDirty returns whether in-memory state is ahead of disk.
func (p *cometProvider) IsDirty() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.dirty
}

func (p *cometProvider) embedAndBuild(ctx context.Context, chunks []IndexedChunk, info IndexInfo) error {
	infoCopy := info
	p.info = &infoCopy
	p.indexReady = false
	p.hasVecs = false

	if p.embedder != nil && len(chunks) > 0 {
		vecCache := p.buildVectorCache()

		allVecs := make([][]float32, len(chunks))
		var toEmbed []int
		for i, c := range chunks {
			if cached, ok := vecCache[c.ParagraphID]; ok {
				allVecs[i] = cached
			} else {
				toEmbed = append(toEmbed, i)
			}
		}
		vecCache = nil // release cache memory

		if len(toEmbed) < len(chunks) {
			logger.Info(fmt.Sprintf("rag: reusing %d/%d cached vectors, embedding %d new chunks",
				len(chunks)-len(toEmbed), len(chunks), len(toEmbed)))
		}

		const batchSize = 64
		for start := 0; start < len(toEmbed); start += batchSize {
			end := start + batchSize
			if end > len(toEmbed) {
				end = len(toEmbed)
			}
			texts := make([]string, end-start)
			for j, idx := range toEmbed[start:end] {
				texts[j] = chunks[idx].Text
			}
			vecs, err := p.embedder.Embed(ctx, texts)
			if err != nil {
				return fmt.Errorf("embed batch %d-%d: %w", start, end, err)
			}
			for j, idx := range toEmbed[start:end] {
				allVecs[idx] = vecs[j]
			}
		}

		p.vectors = allVecs
		p.vecDims = p.embedder.Dims()
		p.hasVecs = true
	} else {
		p.vectors = nil
	}

	p.chunks = chunks
	if err := p.buildIndexes(chunks, p.vectors); err != nil {
		return err
	}
	p.indexReady = true
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
		p.vecIdx = flat
	}

	return nil
}

func (p *cometProvider) Search(ctx context.Context, query string, opts ProviderSearchOptions) (*ProviderSearchResult, error) {
	if err := p.ensureLoaded(); err != nil {
		return nil, err
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	if !p.indexReady {
		if p.info == nil {
			return nil, ErrIndexNotBuilt
		}
		return &ProviderSearchResult{IndexInfo: *p.info}, nil
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
	results, err := p.txtIdx.NewSearch().
		WithQuery(query).
		WithK(limit).
		Execute()
	if err != nil {
		return nil, err
	}

	ids := make([]uint32, 0, len(results))
	for _, r := range results {
		ids = append(ids, r.Id)
	}
	chunkMap, err := p.resolveHits(ids)
	if err != nil {
		return nil, err
	}

	hits := make([]ProviderHit, 0, len(results))
	for _, r := range results {
		chunk, ok := chunkMap[r.Id]
		if !ok {
			continue
		}
		hits = append(hits, ProviderHit{
			Chunk:        chunk,
			LexicalScore: float64(r.Score),
		})
	}

	return &ProviderSearchResult{IndexInfo: *p.info, Hits: hits}, nil
}

func (p *cometProvider) searchHybrid(ctx context.Context, query string, limit int) (*ProviderSearchResult, error) {
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

	// Run BM25 search.
	bm25Results, err := p.txtIdx.NewSearch().
		WithQuery(query).
		WithK(limit).
		Execute()
	if err != nil {
		return nil, fmt.Errorf("bm25 search: %w", err)
	}

	// Run vector search.
	vecResults, err := p.vecIdx.NewSearch().
		WithQuery(qvecs[0]).
		WithK(limit).
		Execute()
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}

	// Merge results by ID.
	type mergedHit struct {
		lexical  float64
		semantic float64
	}
	merged := make(map[uint32]*mergedHit)
	for _, r := range bm25Results {
		merged[r.Id] = &mergedHit{lexical: float64(r.Score)}
	}
	for _, r := range vecResults {
		// Cosine distance → similarity: sim = 1 - dist
		sim := 1.0 - float64(r.Score)
		if sim < 0 {
			sim = 0
		}
		if h, ok := merged[r.GetId()]; ok {
			h.semantic = sim
		} else {
			merged[r.GetId()] = &mergedHit{semantic: sim}
		}
	}

	ids := make([]uint32, 0, len(merged))
	for id := range merged {
		ids = append(ids, id)
	}
	chunkMap, err := p.resolveHits(ids)
	if err != nil {
		return nil, err
	}

	hits := make([]ProviderHit, 0, len(merged))
	for id, h := range merged {
		chunk, ok := chunkMap[id]
		if !ok {
			continue
		}
		hits = append(hits, ProviderHit{
			Chunk:         chunk,
			LexicalScore:  h.lexical,
			SemanticScore: h.semantic,
		})
	}

	return &ProviderSearchResult{IndexInfo: *p.info, Hits: hits}, nil
}

func (p *cometProvider) FetchChunk(_ context.Context, sourcePath string, chunkOrdinal int) (*IndexedChunk, error) {
	if err := p.ensureLoaded(); err != nil {
		return nil, err
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	// Fast path: in-memory chunks (watcher dirty state).
	if p.chunks != nil {
		norm := filepath.ToSlash(sourcePath)
		for i := range p.chunks {
			if p.chunks[i].SourcePath == norm && p.chunks[i].ChunkOrdinal == chunkOrdinal {
				c := p.chunks[i]
				return &c, nil
			}
		}
		return nil, os.ErrNotExist
	}
	// Slow path: read from bbolt.
	return p.store.LoadChunkBySourceAndOrdinal(sourcePath, chunkOrdinal)
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
	if p.indexReady {
		p.mu.RUnlock()
		return nil
	}
	p.mu.RUnlock()

	// Slow path: take write lock and stream from disk into comet indexes.
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.indexReady {
		return nil
	}

	if p.store.IsDirty() {
		return ErrDirtyIndex
	}

	info, err := p.store.LoadIndexInfo()
	if err != nil {
		if errors.Is(err, ErrIndexNotBuilt) {
			return nil
		}
		return err
	}
	p.info = info

	if info.TotalChunks == 0 {
		return nil
	}

	// Stream chunks into BM25 index — no intermediate []IndexedChunk slice.
	txtIdx := comet.NewBM25SearchIndex()
	var chunkCount int
	if err := p.store.ForEachChunk(func(idx uint32, chunk IndexedChunk) error {
		chunkCount++
		return txtIdx.Add(idx, chunk.Text)
	}); err != nil {
		return err
	}
	p.txtIdx = txtIdx

	// Stream vectors into FlatIndex — no intermediate [][]float32 slice.
	var flat *comet.FlatIndex
	var vecCount int
	if err := p.store.ForEachVector(func(idx uint32, vec []float32) error {
		if flat == nil {
			p.vecDims = len(vec)
			var fErr error
			flat, fErr = comet.NewFlatIndex(p.vecDims, comet.Cosine)
			if fErr != nil {
				return fmt.Errorf("create flat index: %w", fErr)
			}
		}
		node := comet.NewVectorNodeWithID(idx, vec)
		vecCount++
		return flat.Add(*node)
	}); err != nil {
		return err
	}

	var vecIdx comet.VectorIndex
	if flat != nil && vecCount == chunkCount {
		vecIdx = flat
		p.hasVecs = true
	}

	// Detect embedding model change.
	if p.hasVecs && p.embedder != nil && p.embedder.Dims() > 0 && p.vecDims != p.embedder.Dims() {
		p.hasVecs = false
		return fmt.Errorf("rag index embedding dimensions mismatch: stored=%d, embedder=%d; rebuild required", p.vecDims, p.embedder.Dims())
	}

	p.vecIdx = vecIdx
	p.indexReady = true
	return nil
}

// resolveHits maps comet result IDs back to IndexedChunk data.
// Uses in-memory chunks if available (dirty watcher state), otherwise
// reads from bbolt in a single batch transaction.
func (p *cometProvider) resolveHits(ids []uint32) (map[uint32]IndexedChunk, error) {
	if p.chunks != nil {
		result := make(map[uint32]IndexedChunk, len(ids))
		for _, id := range ids {
			if int(id) < len(p.chunks) {
				result[id] = p.chunks[id]
			}
		}
		return result, nil
	}
	return p.store.LoadChunksByIndexes(ids)
}

// buildVectorCache builds a ParagraphID → vector mapping from the previous
// index state for incremental embedding. Checks in-memory state first (dirty
// watcher), then falls back to disk. Returns empty map if no previous data.
func (p *cometProvider) buildVectorCache() map[string][]float32 {
	cache := make(map[string][]float32)

	// Use in-memory data if available (watcher dirty state).
	if p.chunks != nil && p.vectors != nil && len(p.chunks) == len(p.vectors) {
		for i, c := range p.chunks {
			if c.ParagraphID != "" {
				cache[c.ParagraphID] = p.vectors[i]
			}
		}
		return cache
	}

	// Load from disk.
	chunks, err := p.store.LoadChunks()
	if err != nil || len(chunks) == 0 {
		return cache
	}
	vecs, err := p.store.LoadVectors()
	if err != nil || len(vecs) != len(chunks) {
		return cache
	}
	for i, c := range chunks {
		if c.ParagraphID != "" {
			cache[c.ParagraphID] = vecs[i]
		}
	}
	return cache
}
