package eval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/rag"
)

// DatasetResult holds evaluation results for one (dataset, strategy) pair.
type DatasetResult struct {
	DatasetName   string        `json:"dataset"`
	StrategyName  string        `json:"strategy"`
	Metrics       MetricsSet    `json:"metrics"`
	PerQuery      []QueryResult `json:"per_query,omitempty"`
	IndexInfo     IndexSummary  `json:"index_info"`
	Embedding     EmbedderStats `json:"embedding"`
	Duration      time.Duration `json:"duration"`
}

// QueryResult holds per-query metrics and result details.
type QueryResult struct {
	QueryID   string     `json:"query_id"`
	QueryText string     `json:"query_text"`
	Metrics   MetricsSet `json:"metrics"`
	TopHits   []string   `json:"top_hits"` // doc IDs of top-10
}

// IndexSummary captures index build metadata.
type IndexSummary struct {
	TotalDocuments int `json:"total_documents"`
	TotalChunks    int `json:"total_chunks"`
}

// RunConfig controls the evaluation run.
type RunConfig struct {
	CacheDir string       // where to cache downloaded datasets
	TopK     int          // max K for search (default 10)
	Embedder rag.Embedder // optional: override embedder (nil = use config default)
	LogFunc  func(string) // progress logging
}

func (c *RunConfig) logf(format string, args ...any) {
	if c.LogFunc != nil {
		c.LogFunc(fmt.Sprintf(format, args...))
	}
}

// Run executes all (dataset × strategy) combinations and returns results.
func Run(ctx context.Context, datasets []Dataset, strategies []Strategy, cfg RunConfig) ([]DatasetResult, error) {
	if cfg.TopK <= 0 {
		cfg.TopK = 10
	}
	log := cfg.LogFunc
	if log == nil {
		log = func(string) {}
	}

	var results []DatasetResult

	for _, ds := range datasets {
		log(fmt.Sprintf("Preparing dataset: %s", ds.Name()))
		dsCache := filepath.Join(cfg.CacheDir, "datasets")
		if err := os.MkdirAll(dsCache, 0o755); err != nil {
			return nil, fmt.Errorf("create dataset cache dir: %w", err)
		}
		if err := ds.Prepare(ctx, dsCache); err != nil {
			return nil, fmt.Errorf("prepare dataset %s: %w", ds.Name(), err)
		}

		corpus := ds.Corpus()
		queries := ds.Queries()
		qrels := ds.RelevanceJudgments()

		log(fmt.Sprintf("  corpus=%d docs, queries=%d, qrels=%d",
			len(corpus), len(queries), len(qrels)))

		for _, strat := range strategies {
			log(fmt.Sprintf("  Running strategy: %s on %s", strat.Name, ds.Name()))
			start := time.Now()

			result, err := runSingle(ctx, ds.Name(), corpus, queries, qrels, strat, cfg)
			if err != nil {
				return nil, fmt.Errorf("run %s/%s: %w", ds.Name(), strat.Name, err)
			}
			result.Duration = time.Since(start)

log(fmt.Sprintf("    recall@10=%.3f nDCG@10=%.3f MRR@10=%.3f tokens=%d (%.1fs)",
			result.Metrics.Recall10, result.Metrics.NDCG10,
			result.Metrics.MRR10, result.Embedding.BestTokenCount(),
			result.Duration.Seconds()))

			results = append(results, *result)
		}
	}

	return results, nil
}

func runSingle(
	ctx context.Context,
	datasetName string,
	corpus []Document,
	queries []Query,
	qrels Qrels,
	strat Strategy,
	cfg RunConfig,
) (*DatasetResult, error) {
	// Deterministic workspace path under CacheDir for index persistence.
	workspace := filepath.Join(cfg.CacheDir, "indices", datasetName, strat.Name)

	// Check if a cached index already exists.
	indexDB := filepath.Join(workspace, "workspace", ".rag", "index.db")
	cached := false
	if _, err := os.Stat(indexDB); err == nil {
		cached = true
		cfg.logf("    Using cached index at %s", workspace)
	}

	var docIDMap map[string]string
	if !cached {
		var err error
		docIDMap, err = prepareWorkspace(workspace, corpus)
		if err != nil {
			return nil, err
		}
	} else {
		// Rebuild docIDMap from corpus (cheap, no disk writes).
		docIDMap = make(map[string]string, len(corpus))
		for _, doc := range corpus {
			filename := sanitizeFilename(doc.ID) + ".md"
			docIDMap[doc.ID] = filepath.Join("notes", filename)
		}
	}

	// Reverse map: sourcePath → docID.
	pathToDocID := make(map[string]string, len(docIDMap))
	for docID, path := range docIDMap {
		pathToDocID[path] = docID
	}

	// Build RAG service with strategy config.
	counting := wrapEmbedder(cfg.Embedder)
	svc, err := buildService(workspace, strat, counting)
	if err != nil {
		return nil, fmt.Errorf("build service: %w", err)
	}
	defer svc.Close()

	// Build index (comet is incremental — cached indices reuse existing data).
	indexInfo, err := svc.BuildIndex(ctx)
	if err != nil {
		return nil, fmt.Errorf("build index: %w", err)
	}

	// Filter queries to those with qrels.
	evalQueries := make([]Query, 0, len(queries))
	for _, q := range queries {
		if _, ok := qrels[q.ID]; ok {
			evalQueries = append(evalQueries, q)
		}
	}

	// Run search for each query.
	perQuery := make([]QueryResult, 0, len(evalQueries))
	for _, q := range evalQueries {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		res, err := svc.Search(ctx, rag.SearchRequest{
			Query:     q.Text,
			ProfileID: strat.ProfileID,
			Mode:      strat.SearchMode,
			TopK:      cfg.TopK,
			Filters:   rag.SearchFilters{AllowRestricted: true},
		})
		if err != nil {
			continue // skip failed queries
		}

		// Extract ranked doc IDs from results.
		ranked := extractRankedDocIDs(res, pathToDocID)

		// Compute metrics.
		relevant := qrels[q.ID]
		metrics := ComputeAll(ranked, relevant)

		topHits := ranked
		if len(topHits) > 10 {
			topHits = topHits[:10]
		}

		perQuery = append(perQuery, QueryResult{
			QueryID:   q.ID,
			QueryText: q.Text,
			Metrics:   metrics,
			TopHits:   topHits,
		})
	}

	avgMetrics := AverageMetrics(extractMetrics(perQuery))

	var embedStats EmbedderStats
	if counting != nil {
		embedStats = counting.Stats()
	}

	return &DatasetResult{
		DatasetName:  datasetName,
		StrategyName: strat.Name,
		Metrics:      avgMetrics,
		PerQuery:     perQuery,
		IndexInfo: IndexSummary{
			TotalDocuments: indexInfo.TotalDocuments,
			TotalChunks:    indexInfo.TotalChunks,
		},
		Embedding: embedStats,
	}, nil
}

// prepareWorkspace writes corpus documents as markdown files into the given
// workspace directory and returns the docID→sourcePath mapping.
func prepareWorkspace(workspace string, corpus []Document) (map[string]string, error) {
	kbNotes := filepath.Join(workspace, "kb", "notes")
	if err := os.MkdirAll(kbNotes, 0o755); err != nil {
		return nil, err
	}

	docIDMap := make(map[string]string, len(corpus))
	for _, doc := range corpus {
		filename := sanitizeFilename(doc.ID) + ".md"
		sourcePath := filepath.Join("notes", filename)
		content := DocToMarkdown(doc)

		if err := os.WriteFile(filepath.Join(kbNotes, filename), []byte(content), 0o644); err != nil {
			return nil, err
		}
		docIDMap[doc.ID] = sourcePath
	}

	return docIDMap, nil
}

func buildService(workspace string, strat Strategy, embedder *CountingEmbedder) (*rag.Service, error) {
	ragCfg := config.RAGToolsConfig{
		IndexProvider:  "comet",
		IndexRoot:      "workspace/.rag",
		KBRoot:         "workspace/kb",
		ChunkSoftBytes: strat.ChunkSoftBytes,
		ChunkHardBytes: strat.ChunkHardBytes,
		DefaultProfileID: strat.ProfileID,
	}

	chunker := ResolveChunker(strat)

	opts := []rag.ServiceOption{
		rag.WithChunker(chunker),
	}
	if embedder != nil {
		opts = append(opts, rag.WithEmbedder(embedder))
	}

	svc := rag.NewService(workspace, ragCfg, config.ProvidersConfig{}, opts...)
	return svc, nil
}

// wrapEmbedder wraps the given embedder in a CountingEmbedder.
// Returns nil if the input is nil.
func wrapEmbedder(e rag.Embedder) *CountingEmbedder {
	if e == nil {
		return nil
	}
	return NewCountingEmbedder(e)
}

// extractRankedDocIDs maps search result source paths back to BEIR doc IDs.
// Deduplicates by doc ID (multiple chunks → one doc appearance).
func extractRankedDocIDs(res *rag.SearchResult, pathToDocID map[string]string) []string {
	if res == nil || res.Full == nil {
		return nil
	}
	seen := make(map[string]bool)
	var ranked []string
	for _, item := range res.Full.Items {
		docID, ok := pathToDocID[item.SourcePath]
		if !ok {
			// Try with just the filename.
			base := filepath.Base(item.SourcePath)
			docID = strings.TrimSuffix(base, ".md")
		}
		if !seen[docID] {
			seen[docID] = true
			ranked = append(ranked, docID)
		}
	}
	return ranked
}

func extractMetrics(qr []QueryResult) []MetricsSet {
	m := make([]MetricsSet, len(qr))
	for i := range qr {
		m[i] = qr[i].Metrics
	}
	return m
}

func sanitizeFilename(id string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", " ", "_", ":", "_")
	return r.Replace(id)
}
