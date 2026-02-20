package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

var (
	ErrQueueFull      = errors.New("queue_full")
	ErrIndexNotBuilt  = errors.New("rag index not built")
	tokenSplitRE      = regexp.MustCompile(`[^\pL\pN]+`)
	defaultRetryAfter = 3
)

type Service struct {
	workspace string
	cfg       config.RAGToolsConfig
	indexRoot string
	kbRoot    string
	provider  IndexProvider
	embedder  Embedder

	providerInitErr error
	sem             chan struct{}
	mu              sync.Mutex
	q               int

	// precomputed from cfg.DenylistPaths at construction
	denyExact    map[string]struct{} // lowered exact filenames
	denyPrefixes []string            // lowered directory prefixes (end with /)
}

// ServiceOption configures optional Service dependencies.
type ServiceOption func(*Service)

// WithEmbedder overrides the embedder created from config. Useful for testing
// with a fake embedder that doesn't require API keys.
func WithEmbedder(e Embedder) ServiceOption {
	return func(s *Service) { s.embedder = e }
}

// Close releases resources held by the service and its provider.
// Safe to call on a partially-constructed service (providerInitErr set).
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	if s.provider != nil {
		return s.provider.Close()
	}
	return nil
}

// NewService centralizes runtime defaults so every entry point (CLI and tool)
// gets identical behavior and reproducible scoring without duplicated wiring.
func NewService(workspace string, cfg config.RAGToolsConfig, providers config.ProvidersConfig, opts ...ServiceOption) *Service {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 16
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 3
	}
	if cfg.ChunkSoftBytes <= 0 {
		cfg.ChunkSoftBytes = 4096
	}
	if cfg.ChunkHardBytes <= 0 {
		cfg.ChunkHardBytes = 8192
	}
	if cfg.DocumentHardBytes <= 0 {
		cfg.DocumentHardBytes = 10 * 1024 * 1024
	}
	if cfg.MaxChunksPerDocument <= 0 {
		cfg.MaxChunksPerDocument = 2000
	}
	if cfg.DefaultProfileID == "" {
		cfg.DefaultProfileID = "default_research"
	}
	if cfg.KBRoot == "" {
		cfg.KBRoot = "workspace/kb"
	}
	if cfg.IndexRoot == "" {
		cfg.IndexRoot = "workspace/.rag"
	}

	indexRoot := resolveRAGPath(workspace, cfg.IndexRoot)
	kbRoot := resolveRAGPath(workspace, cfg.KBRoot)
	apiKey := cfg.EmbeddingAPIKey.Value()
	if apiKey == "" {
		apiKey = providers.GetProviderConfig(cfg.EmbeddingProvider).APIKey
	}
	embedder := newEmbedder(cfg.EmbeddingProvider, cfg.EmbeddingModelID,
		cfg.EmbeddingAPIBase, apiKey, cfg.AllowExternalEmbeddings)
	provider, providerErr := newIndexProvider(workspace, cfg, indexRoot, embedder)

	svc := &Service{
		workspace:       workspace,
		cfg:             cfg,
		indexRoot:       indexRoot,
		kbRoot:          kbRoot,
		provider:        provider,
		embedder:        embedder,
		providerInitErr: providerErr,
		sem:             make(chan struct{}, cfg.Concurrency),
	}
	svc.precomputeDenylist(cfg.DenylistPaths)
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

func resolveRAGPath(workspace, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	if strings.HasPrefix(value, "workspace/") {
		return filepath.Join(workspace, strings.TrimPrefix(value, "workspace/"))
	}
	return filepath.Join(workspace, value)
}

// RetryAfterSeconds exposes a deterministic backoff hint so callers can retry
// queue saturation without guessing and creating bursty traffic.
func (s *Service) RetryAfterSeconds() int {
	return defaultRetryAfter
}

// IsQueueFull lets integrations branch on overload using a typed check instead
// of string parsing, which keeps retry logic stable across error wording changes.
func IsQueueFull(err error) bool {
	return errors.Is(err, ErrQueueFull)
}

func (s *Service) providerOrErr() (IndexProvider, error) {
	if s.providerInitErr != nil {
		return nil, s.providerInitErr
	}
	if s.provider == nil {
		return nil, fmt.Errorf("rag provider is not configured")
	}
	return s.provider, nil
}

// BuildIndex rebuilds the local searchable snapshot from KB files in one pass
// so retrieval and audits refer to the same index version.
func (s *Service) BuildIndex(ctx context.Context) (*IndexInfo, error) {
	if err := os.MkdirAll(filepath.Join(s.indexRoot, "reports"), 0o755); err != nil {
		return nil, err
	}
	provider, err := s.providerOrErr()
	if err != nil {
		return nil, err
	}

	// Chunking is stateless IO — no concurrency slot needed.
	chunks, info, err := s.buildChunksAndInfo(ctx)
	if err != nil {
		return nil, err
	}

	// Only the provider mutation needs the semaphore slot.
	if err := s.acquireSem(ctx); err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}
	defer s.releaseSem()

	if err := provider.Build(ctx, chunks, *info); err != nil {
		return nil, err
	}

	return info, nil
}

// buildChunksAndInfo walks kbRoot and produces chunks + metadata without
// touching the provider. Used by both BuildIndex (full build) and the
// watcher (in-memory rebuild).
func (s *Service) buildChunksAndInfo(ctx context.Context) ([]IndexedChunk, *IndexInfo, error) {
	provider, err := s.providerOrErr()
	if err != nil {
		return nil, nil, err
	}

	indexedChunks := make([]IndexedChunk, 0, 512)
	warnings := make([]string, 0)
	docCount := 0

	absWorkspace, err := filepath.Abs(s.workspace)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve workspace path: %w", err)
	}
	if resolvedWorkspace, err := filepath.EvalSymlinks(absWorkspace); err == nil {
		absWorkspace = resolvedWorkspace
	}

	err = filepath.WalkDir(s.kbRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("walk_error:%s:%v", path, err))
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if d.IsDir() {
			return nil
		}

		if strings.ToLower(filepath.Ext(path)) != ".md" {
			return nil
		}

		absPath, err := filepath.Abs(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("path_abs_error:%s", path))
			return nil
		}

		resolved := absPath
		if r, err := filepath.EvalSymlinks(absPath); err == nil {
			resolved = r
		}
		if !isWithinPath(resolved, absWorkspace) {
			warnings = append(warnings, fmt.Sprintf("security_path_blocked:%s", path))
			return nil
		}

		relToKB, err := filepath.Rel(s.kbRoot, absPath)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("rel_error:%s", path))
			return nil
		}
		relToKB = filepath.ToSlash(relToKB)
		if isDenied(relToKB, s.denyExact, s.denyPrefixes) {
			warnings = append(warnings, fmt.Sprintf("security_path_blocked:%s", relToKB))
			return nil
		}

		data, err := os.ReadFile(absPath)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("read_error:%s", relToKB))
			return nil
		}
		if len(data) > s.cfg.DocumentHardBytes {
			warnings = append(warnings, fmt.Sprintf("doc_hard_limit:%s", relToKB))
			return nil
		}

		meta, body, parseWarnings := parseFrontmatter(string(data))
		for _, w := range parseWarnings {
			warnings = append(warnings, fmt.Sprintf("%s:%s", relToKB, w))
		}
		if meta.Confidentiality == "" {
			meta.Confidentiality = "internal"
		}

		docVersion := sha256Hex(data)
		docType := classifyDocType(relToKB)
		effectiveDate := meta.Date
		if meta.EffectiveDate != "" {
			effectiveDate = meta.EffectiveDate
		}

		chunks := splitMarkdownChunks(body, s.cfg.ChunkSoftBytes, s.cfg.ChunkHardBytes)
		if len(chunks) > s.cfg.MaxChunksPerDocument {
			chunks = chunks[:s.cfg.MaxChunksPerDocument]
			warnings = append(warnings, fmt.Sprintf("max_chunks_per_document:%s", relToKB))
		}

		for i, c := range chunks {
			norm := normalizeText(c.Text)
			flags, risk := detectInjectionRisk(norm)
			indexedChunks = append(indexedChunks, IndexedChunk{
				SourcePath:      relToKB,
				ChunkOrdinal:    i + 1,
				ChunkLoc:        c.Loc,
				DocumentVersion: docVersion,
				ParagraphID:     sha256Hex([]byte(relToKB + "\n" + norm)),
				Title:           meta.Title,
				Date:            effectiveDate,
				Project:         strings.ToLower(strings.TrimSpace(meta.Project)),
				Tags:            normalizeTags(meta.Tags),
				Confidentiality: strings.ToLower(strings.TrimSpace(meta.Confidentiality)),
				DocType:         docType,
				Text:            norm,
				Snippet:         safeSnippet(norm, 600),
				Flags:           flags,
				RiskScore:       risk,
			})
		}
		docCount++
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	now := time.Now().UTC()
	info := IndexInfo{
		IndexVersion:     fmt.Sprintf("idx-%d", now.Unix()),
		IndexState:       "healthy",
		IndexProvider:    provider.Name(),
		BuiltAt:          now.Format(time.RFC3339),
		EmbeddingModelID: s.cfg.EmbeddingModelID,
		ChunkingHash:     sha256Hex([]byte(fmt.Sprintf("%d:%d:%d", s.cfg.ChunkSoftBytes, s.cfg.ChunkHardBytes, s.cfg.MaxChunksPerDocument))),
		Warnings:         warnings,
		TotalDocuments:   docCount,
		TotalChunks:      len(indexedChunks),
	}

	return indexedChunks, &info, nil
}

// acquireSem blocks until a concurrency slot is available or ctx is cancelled.
// Use for heavyweight operations (build, reindex) that should respect the
// concurrency limit but are not counted against the search queue depth.
func (s *Service) acquireSem(ctx context.Context) error {
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) releaseSem() {
	select {
	case <-s.sem:
	default:
	}
}

func (s *Service) beginQueued(ctx context.Context) error {
	s.mu.Lock()
	if s.q >= s.cfg.QueueSize {
		s.mu.Unlock()
		return ErrQueueFull
	}
	s.q++
	s.mu.Unlock()

	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		s.mu.Lock()
		s.q--
		s.mu.Unlock()
		return ctx.Err()
	}
}

func (s *Service) endQueued() {
	select {
	case <-s.sem:
	default:
	}
	s.mu.Lock()
	if s.q > 0 {
		s.q--
	}
	s.mu.Unlock()
}

// Search applies profile-constrained retrieval and ranking to return evidence
// with predictable policy behavior (privacy filters, risk downrank, source caps).
func (s *Service) Search(ctx context.Context, req SearchRequest) (*SearchResult, error) {
	if err := s.beginQueued(ctx); err != nil {
		return nil, err
	}
	defer s.endQueued()
	provider, err := s.providerOrErr()
	if err != nil {
		return nil, err
	}

	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	profile := ResolveProfile(req.ProfileID, s.cfg.DefaultProfileID)
	mode := req.Mode
	if mode == "" {
		mode = profile.DefaultMode
	}

	notes := make([]string, 0)
	caps := provider.Capabilities()
	semanticAvailable := s.cfg.AllowExternalEmbeddings && caps.Semantic
	if mode == ModeSemanticOnly || mode == ModeHybrid {
		if !semanticAvailable {
			notes = append(notes, "semantic unavailable; fallback=keyword-only")
			mode = ModeKeywordOnly
		}
	}

	if err := validateFilters(req.Filters); err != nil {
		return nil, err
	}

	if len(tokenize(query)) == 0 {
		return nil, fmt.Errorf("query does not contain searchable tokens")
	}

	topK := req.TopK
	if topK <= 0 {
		topK = 20
	}
	if topK > 100 {
		topK = 100
	}

	candidateLimit := profile.BM25TopN
	if candidateLimit <= 0 {
		candidateLimit = 200
	}
	minForTopK := topK * 4
	if candidateLimit < minForTopK {
		candidateLimit = minForTopK
	}
	if candidateLimit > 2000 {
		candidateLimit = 2000
	}

	searchRes, err := provider.Search(ctx, query, ProviderSearchOptions{
		Limit: candidateLimit,
		Mode:  mode,
	})
	if err != nil {
		return nil, err
	}

	// Pin freshness to index build time so scores are stable and
	// reproducible within an index version.
	refTime := time.Now().UTC()
	if bt, ok := parseISODate(searchRes.IndexInfo.BuiltAt); ok {
		refTime = bt
	}

	type cand struct {
		Chunk     IndexedChunk
		RawBM25   float64
		RawCosine float64
		RawFused  float64
		FreshNorm float64
		MetaBoost float64
		Score     float64
		Breakdown ScoreBreakdown
	}
	cands := make([]cand, 0, 128)

	for _, hit := range searchRes.Hits {
		chunk := hit.Chunk
		if !passesFilters(chunk, req.Filters) {
			continue
		}
		if hit.LexicalScore <= 0 && hit.SemanticScore <= 0 && hit.FusedScore <= 0 {
			continue
		}
		fresh := freshnessNorm(chunk.Date, refTime)
		boost := metadataBoost(profile, chunk)
		cands = append(cands, cand{
			Chunk:     chunk,
			RawBM25:   hit.LexicalScore,
			RawCosine: hit.SemanticScore,
			RawFused:  hit.FusedScore,
			FreshNorm: fresh,
			MetaBoost: boost,
		})
	}

	if len(cands) == 0 {
		empty := &EvidencePackFull{
			Query:     query,
			ProfileID: profile.ID,
			IndexInfo: searchRes.IndexInfo,
			Items:     []EvidenceItemFull{},
			Coverage:  Coverage{},
			Notes:     append(notes, "insufficient evidence"),
		}
		compact := toLLMCompact(query, profile.ID, empty.Items, empty.Notes)
		return &SearchResult{Full: empty, LLM: compact}, nil
	}

	// Determine whether candidates carry pre-fused scores (e.g. RRF from
	// hybrid provider) or separate lexical/semantic components.
	hasFused := len(cands) > 0 && cands[0].RawFused > 0

	sort.SliceStable(cands, func(i, j int) bool {
		primary := func(c cand) float64 {
			if hasFused {
				return c.RawFused
			}
			return c.RawBM25
		}
		if primary(cands[i]) == primary(cands[j]) {
			if cands[i].Chunk.SourcePath == cands[j].Chunk.SourcePath {
				return cands[i].Chunk.ChunkOrdinal < cands[j].Chunk.ChunkOrdinal
			}
			return cands[i].Chunk.SourcePath < cands[j].Chunk.SourcePath
		}
		return primary(cands[i]) > primary(cands[j])
	})

	topN := profile.BM25TopN
	if topN <= 0 || topN > len(cands) {
		topN = len(cands)
	}
	cands = cands[:topN]

	minBM, maxBM := cands[0].RawBM25, cands[0].RawBM25
	minCos, maxCos := cands[0].RawCosine, cands[0].RawCosine
	minFused, maxFused := cands[0].RawFused, cands[0].RawFused
	for _, c := range cands {
		if c.RawBM25 < minBM {
			minBM = c.RawBM25
		}
		if c.RawBM25 > maxBM {
			maxBM = c.RawBM25
		}
		if c.RawCosine < minCos {
			minCos = c.RawCosine
		}
		if c.RawCosine > maxCos {
			maxCos = c.RawCosine
		}
		if c.RawFused < minFused {
			minFused = c.RawFused
		}
		if c.RawFused > maxFused {
			maxFused = c.RawFused
		}
	}

	for i := range cands {
		var bmNorm, cosNorm float64

		if hasFused {
			// Provider already fused lexical+semantic (e.g. RRF). Use
			// the fused score as the combined retrieval signal, spreading
			// it across BM25+Cosine weights so profile math still applies.
			fusedNorm := 1.0
			if maxFused > minFused {
				fusedNorm = (cands[i].RawFused - minFused) / (maxFused - minFused)
			}
			bmNorm = fusedNorm
			cosNorm = fusedNorm
		} else {
			bmNorm = 1.0
			if maxBM > minBM {
				bmNorm = (cands[i].RawBM25 - minBM) / (maxBM - minBM)
			}

			cosNorm = 0.0
			if semanticAvailable && mode != ModeKeywordOnly {
				cosNorm = 1.0
				if maxCos > minCos {
					cosNorm = (cands[i].RawCosine - minCos) / (maxCos - minCos)
				}
			}

			if mode == ModeSemanticOnly {
				bmNorm = 0.0
			}
			if mode == ModeKeywordOnly {
				cosNorm = 0.0
			}
		}

		final := profile.WeightBM25*bmNorm + profile.WeightCosine*cosNorm + profile.WeightFreshness*cands[i].FreshNorm + profile.WeightMetadataBoost*cands[i].MetaBoost
		if final < 0 {
			final = 0
		}
		penalty := 1.0 - (0.2 * cands[i].Chunk.RiskScore)
		if penalty < 0.5 {
			penalty = 0.5
		}
		final *= penalty
		cands[i].Score = final
		cands[i].Breakdown = ScoreBreakdown{
			BM25Norm:      bmNorm,
			CosineNorm:    cosNorm,
			FreshnessNorm: cands[i].FreshNorm,
			MetadataBoost: cands[i].MetaBoost,
			FinalScore:    final,
		}
	}

	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Score == cands[j].Score {
			if cands[i].Chunk.SourcePath == cands[j].Chunk.SourcePath {
				return cands[i].Chunk.ChunkOrdinal < cands[j].Chunk.ChunkOrdinal
			}
			return cands[i].Chunk.SourcePath < cands[j].Chunk.SourcePath
		}
		return cands[i].Score > cands[j].Score
	})

	perSource := make(map[string]int)
	items := make([]EvidenceItemFull, 0, topK)
	for _, c := range cands {
		if len(items) >= topK {
			break
		}
		if perSource[c.Chunk.SourcePath] >= profile.PerSourceCap {
			continue
		}
		perSource[c.Chunk.SourcePath]++
		items = append(items, EvidenceItemFull{
			SourcePath: c.Chunk.SourcePath,
			ChunkRef: ChunkRef{
				SourcePath:   c.Chunk.SourcePath,
				ChunkOrdinal: c.Chunk.ChunkOrdinal,
			},
			ChunkLoc:        c.Chunk.ChunkLoc,
			DocumentVersion: c.Chunk.DocumentVersion,
			Title:           c.Chunk.Title,
			Date:            c.Chunk.Date,
			Snippet:         c.Chunk.Snippet,
			Score:           c.Score,
			ScoreBreakdown:  c.Breakdown,
			Flags:           c.Chunk.Flags,
		})
	}

	coverage := buildCoverage(items)
	full := &EvidencePackFull{
		Query:     query,
		ProfileID: profile.ID,
		IndexInfo: searchRes.IndexInfo,
		Items:     items,
		Coverage:  coverage,
		Notes:     notes,
	}

	llm := toLLMCompact(query, profile.ID, items, notes)
	return &SearchResult{Full: full, LLM: llm}, nil
}

func buildCoverage(items []EvidenceItemFull) Coverage {
	if len(items) == 0 {
		return Coverage{}
	}
	sources := make(map[string]struct{})
	var minT, maxT time.Time
	for i, it := range items {
		sources[it.SourcePath] = struct{}{}
		if t, ok := parseISODate(it.Date); ok {
			if i == 0 || minT.IsZero() || t.Before(minT) {
				minT = t
			}
			if i == 0 || maxT.IsZero() || t.After(maxT) {
				maxT = t
			}
		}
	}
	cov := Coverage{UniqueSources: len(sources)}
	if !minT.IsZero() && !maxT.IsZero() {
		cov.TimeSpan = &TimeSpan{From: minT.Format("2006-01-02"), To: maxT.Format("2006-01-02")}
	}
	return cov
}

func toLLMCompact(query, profileID string, items []EvidenceItemFull, notes []string) *EvidencePackLLM {
	sourceAlias := map[string]string{}
	sources := map[string]string{}
	aliasSeq := 1
	llmItems := make([]EvidenceItemLLM, 0, len(items))

	for _, item := range items {
		alias, ok := sourceAlias[item.SourcePath]
		if !ok {
			alias = fmt.Sprintf("S%d", aliasSeq)
			aliasSeq++
			sourceAlias[item.SourcePath] = alias
			sources[alias] = item.SourcePath
		}
		llmItems = append(llmItems, EvidenceItemLLM{
			Ref:     fmt.Sprintf("%s#%d", alias, item.ChunkRef.ChunkOrdinal),
			Snippet: item.Snippet,
			Score:   item.Score,
		})
	}

	return &EvidencePackLLM{
		Query:     query,
		ProfileID: profileID,
		Sources:   sources,
		Items:     llmItems,
		Notes:     notes,
	}
}

// FetchChunk resolves an exact chunk reference for follow-up inspection, so the
// agent can request full text only when needed instead of inflating initial payloads.
func (s *Service) FetchChunk(ctx context.Context, sourcePath string, chunkOrdinal int) (*ChunkResult, error) {
	provider, err := s.providerOrErr()
	if err != nil {
		return nil, err
	}
	chunk, err := provider.FetchChunk(ctx, sourcePath, chunkOrdinal)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("chunk not found")
		}
		return nil, err
	}
	return &ChunkResult{
		SourcePath:   chunk.SourcePath,
		ChunkOrdinal: chunk.ChunkOrdinal,
		ChunkLoc:     chunk.ChunkLoc,
		Text:         chunk.Text,
		Snippet:      chunk.Snippet,
	}, nil
}

func validateFilters(filters SearchFilters) error {
	if !filters.AllowRestricted {
		for _, c := range filters.ConfidentialityAllow {
			if strings.EqualFold(c, "restricted") {
				return fmt.Errorf("restricted cannot be requested when allow_restricted=false")
			}
		}
	}
	return nil
}

func passesFilters(chunk IndexedChunk, filters SearchFilters) bool {
	if !filters.AllowRestricted && strings.EqualFold(chunk.Confidentiality, "restricted") {
		return false
	}

	if len(filters.ConfidentialityAllow) > 0 {
		ok := false
		for _, c := range filters.ConfidentialityAllow {
			if strings.EqualFold(strings.TrimSpace(c), chunk.Confidentiality) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}

	if len(filters.DocType) > 0 {
		ok := false
		for _, d := range filters.DocType {
			if strings.EqualFold(strings.TrimSpace(d), chunk.DocType) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}

	if len(filters.Project) > 0 {
		ok := false
		for _, p := range filters.Project {
			if strings.EqualFold(strings.TrimSpace(p), chunk.Project) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}

	if len(filters.Tags) > 0 {
		if strings.EqualFold(filters.TagMode, "all") {
			for _, tag := range filters.Tags {
				if !containsTag(chunk.Tags, tag) {
					return false
				}
			}
		} else {
			ok := false
			for _, tag := range filters.Tags {
				if containsTag(chunk.Tags, tag) {
					ok = true
					break
				}
			}
			if !ok {
				return false
			}
		}
	}

	if filters.DateFrom != "" || filters.DateTo != "" {
		t, ok := parseISODate(chunk.Date)
		if !ok {
			return false
		}
		if filters.DateFrom != "" {
			if from, ok := parseISODate(filters.DateFrom); ok && t.Before(from) {
				return false
			}
		}
		if filters.DateTo != "" {
			if to, ok := parseISODate(filters.DateTo); ok && t.After(to) {
				return false
			}
		}
	}

	return true
}

func containsTag(tags []string, value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, t := range tags {
		if strings.ToLower(strings.TrimSpace(t)) == value {
			return true
		}
	}
	return false
}

func tokenize(s string) []string {
	parts := tokenSplitRE.Split(strings.ToLower(s), -1)
	tokens := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			tokens = append(tokens, p)
		}
	}
	return tokens
}

func lexicalScore(queryTokens []string, text string) float64 {
	if len(queryTokens) == 0 || text == "" {
		return 0
	}
	lc := strings.ToLower(text)
	score := 0.0
	for _, t := range queryTokens {
		if strings.Contains(lc, t) {
			score += float64(strings.Count(lc, t))
		}
	}
	return score
}

// freshnessNorm computes an exponential decay score relative to refTime so
// scores are stable and reproducible within an index version.
func freshnessNorm(date string, refTime time.Time) float64 {
	t, ok := parseISODate(date)
	if !ok {
		return 0
	}
	ageDays := refTime.Sub(t).Hours() / 24
	if ageDays < 0 {
		ageDays = 0
	}
	halfLife := 365.0
	return math.Exp(-math.Ln2 * ageDays / halfLife)
}

func metadataBoost(profile FixedProfile, chunk IndexedChunk) float64 {
	boost := 0.0
	if profile.PreferNotesPolicy && (chunk.DocType == "note" || chunk.DocType == "policy") {
		boost += 1.0
	}
	if profile.ID == "templates_lookup" && chunk.DocType == "template" {
		boost += 1.0
	}
	return boost
}

func classifyDocType(relPath string) string {
	relPath = strings.ToLower(filepath.ToSlash(relPath))
	switch {
	case strings.HasPrefix(relPath, "notes/"):
		return "note"
	case strings.HasPrefix(relPath, "papers/"):
		return "paper"
	case strings.HasPrefix(relPath, "templates/"):
		return "template"
	case strings.HasSuffix(relPath, "policy.md"):
		return "policy"
	case strings.HasSuffix(relPath, "glossary.md"):
		return "glossary"
	default:
		return "note"
	}
}

func parseFrontmatter(content string) (docMeta, string, []string) {
	meta := docMeta{}
	warnings := make([]string, 0)
	if !strings.HasPrefix(content, "---\n") {
		return meta, content, warnings
	}
	end := strings.Index(content[4:], "\n---\n")
	if end < 0 {
		warnings = append(warnings, "frontmatter_unclosed")
		return meta, content, warnings
	}
	fm := content[4 : 4+end]
	body := content[4+end+5:]

	lines := strings.Split(fm, "\n")
	inTags := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") && inTags {
			meta.Tags = append(meta.Tags, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
			continue
		}
		inTags = false
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		value := strings.TrimSpace(parts[1])
		// Strip outer quotes but preserve inner colons.
		if (strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`)) ||
			(strings.HasPrefix(value, `'`) && strings.HasSuffix(value, `'`)) {
			value = value[1 : len(value)-1]
		}
		switch key {
		case "title":
			meta.Title = value
		case "date":
			meta.Date = value
		case "effective_date":
			meta.EffectiveDate = value
		case "project":
			meta.Project = value
		case "source":
			meta.Source = value
		case "confidentiality":
			meta.Confidentiality = strings.ToLower(value)
		case "tags":
			if value == "" {
				inTags = true
				continue
			}
			if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
				value = strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
			}
			for _, t := range strings.Split(value, ",") {
				t = strings.TrimSpace(strings.Trim(t, `"'`))
				if t != "" {
					meta.Tags = append(meta.Tags, t)
				}
			}
		}
	}
	return meta, body, warnings
}

func normalizeTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := map[string]struct{}{}
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

func detectInjectionRisk(text string) ([]string, float64) {
	flags := make([]string, 0, 3)
	risk := 0.0
	lc := strings.ToLower(text)
	if strings.Contains(lc, "ignore previous") || strings.Contains(lc, "system prompt") || strings.Contains(lc, "developer message") {
		flags = append(flags, "policy_override_attempt")
		risk += 0.7
	}
	if strings.Contains(lc, "call tool") || strings.Contains(lc, "execute command") || strings.Contains(lc, "run this") {
		flags = append(flags, "tool_call_attempt")
		risk += 0.5
	}
	if strings.Contains(lc, "must do") || strings.Contains(lc, "you must") {
		flags = append(flags, "instruction_like")
		risk += 0.3
	}
	if risk > 1.0 {
		risk = 1.0
	}
	return flags, risk
}

func safeSnippet(text string, max int) string {
	if max <= 0 {
		max = 600
	}
	masked := maskSecrets(text)
	runes := []rune(masked)
	if len(runes) <= max {
		return masked
	}
	return string(runes[:max]) + "..."
}

// secretPatterns is compiled once at package init. maskSecrets runs per-chunk
// during indexing and per-snippet during search, so avoiding repeated compilation
// matters (~14k calls on a 2000-chunk build).
var secretPatterns = []struct {
	re *regexp.Regexp
	rp string
}{
	{regexp.MustCompile(`(?i)sk-[a-z0-9]{20,}`), "[REDACTED_API_KEY]"},
	{regexp.MustCompile(`(?i)api[_-]?key\s*[:=]\s*[^\s]+`), "api_key=[REDACTED]"},
	{regexp.MustCompile(`(?i)bearer\s+[a-z0-9\-\._~\+/]+=*`), "Bearer [REDACTED]"},
	{regexp.MustCompile(`(?i)password\s*[:=]\s*[^\s]+`), "password=[REDACTED]"},
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`), "[REDACTED_PRIVATE_KEY]"},
	{regexp.MustCompile(`AKIA[0-9A-Z]{16}`), "[REDACTED_AWS_KEY]"},
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`), "[REDACTED_TOKEN]"},
}

func maskSecrets(text string) string {
	out := text
	for _, r := range secretPatterns {
		out = r.re.ReplaceAllString(out, r.rp)
	}
	return out
}

// precomputeDenylist splits raw deny patterns into exact-match and prefix
// sets so per-file checks avoid repeated normalization.
func (s *Service) precomputeDenylist(raw []string) {
	s.denyExact = make(map[string]struct{}, len(raw))
	s.denyPrefixes = make([]string, 0, len(raw))
	for _, d := range raw {
		dn := strings.ToLower(filepath.ToSlash(strings.TrimSpace(d)))
		if dn == "" {
			continue
		}
		if strings.HasSuffix(dn, "/") {
			s.denyPrefixes = append(s.denyPrefixes, dn)
		} else {
			s.denyExact[dn] = struct{}{}
		}
	}
}

func isDenied(relPath string, exact map[string]struct{}, prefixes []string) bool {
	norm := strings.ToLower(filepath.ToSlash(relPath))

	// exact match (filename or relative path)
	if _, ok := exact[norm]; ok {
		return true
	}
	// check if any exact entry matches as a path component
	for dn := range exact {
		if strings.HasSuffix(norm, "/"+dn) || strings.Contains(norm, "/"+dn+"/") {
			return true
		}
	}
	// prefix/directory matches
	for _, dn := range prefixes {
		if strings.HasPrefix(norm, dn) || strings.Contains(norm, "/"+strings.TrimSuffix(dn, "/")+"/") {
			return true
		}
	}
	return false
}

func isWithinPath(candidate, root string) bool {
	c := filepath.Clean(candidate)
	r := filepath.Clean(root)
	rel, err := filepath.Rel(r, c)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
