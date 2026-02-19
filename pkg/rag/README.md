# pkg/rag

ResearchRAG engine for PicoClaw.

## 1. Scope and status

`pkg/rag` is the retrieval core behind:

- CLI: `picoclaw rag index|search|chunk|eval`
- Tool: `rag_search`

This package implements a **stable API** with a **pluggable index provider**:

- `simple` (default): JSON-backed local index with token-count scoring
- `comet`: pure-Go hybrid search — BM25 lexical + cosine vector similarity via [comet](https://github.com/wizenheimer/comet)

## 2. Package layout

- `types.go`: request/response/index/eval models
- `profiles.go`: fixed profile definitions + resolver
- `chunker.go`: markdown chunk splitting and text normalization
- `provider.go`: pluggable provider contracts + factory
- `provider_simple.go`: JSON-backed provider
- `provider_comet.go`: Comet provider (BM25 + vector hybrid)
- `embedder.go`: embedding interface + HTTP provider (OpenAI-compatible)
- `service.go`: indexing, search orchestration, queueing, eval, masking, filtering
- `service_test.go`: smoke tests for index/search/filter behavior
- `provider_comet_test.go`: comet provider unit tests (BM25, hybrid, persistence)
- `embedder_bow_test.go`: deterministic bag-of-words test embedder

Related integration:

- `pkg/tools/rag_search.go`: tool wrapper using compact LLM output
- `cmd/picoclaw/rag_cmd.go`: CLI command handlers

## 3. Core API

Service constructor:

```go
svc := rag.NewService(workspace, cfg.Tools.RAG, cfg.Providers)
```

Provider selection:

- `cfg.Tools.RAG.IndexProvider = "simple"` (default)
- `cfg.Tools.RAG.IndexProvider = "comet"` (pure Go, no build tags needed)

Provider contract:

```go
type IndexProvider interface {
    Name() string
    Capabilities() ProviderCapabilities
    Build(ctx context.Context, chunks []IndexedChunk, info IndexInfo) error
    Search(ctx context.Context, query string, opts ProviderSearchOptions) (*ProviderSearchResult, error)
    FetchChunk(ctx context.Context, sourcePath string, chunkOrdinal int) (*IndexedChunk, error)
    LoadIndexInfo(ctx context.Context) (*IndexInfo, error)
}
```

Main methods:

- `BuildIndex(ctx) (*IndexInfo, error)`
- `Search(ctx, req SearchRequest) (*SearchResult, error)`
- `FetchChunk(ctx, sourcePath string, chunkOrdinal int) (*ChunkResult, error)`
- `Eval(ctx, goldenPath, baselinePath, profileID string) (*EvalReport, int, error)`

Queue helpers:

- `IsQueueFull(err error) bool`
- `RetryAfterSeconds() int`

## 4. Identity model (MVP)

### Source identity

- Primary source identifier: `source_path`
- `source_path` is canonical path relative to configured `kb_root`

### Chunk identity

- Primary chunk reference:

```json
{ "source_path": "kb/notes/meeting.md", "chunk_ordinal": 3 }
```

- Chunk ordinals are assigned in index build order for each document

### Versioning

- `document_version = sha256(raw_bytes)`
- If content changes, references are still resolvable by `chunk_ref`, but semantic meaning may drift (known MVP tradeoff)

## 5. Provider model

Configured by `tools.rag.index_provider`:

- `simple` (default) — JSON-backed, token-count scoring, zero dependencies
- `comet` — pure-Go BM25 + optional vector hybrid via cosine similarity

Provider contract (`IndexProvider`):

- `Build(...)`
- `Search(...)`
- `FetchChunk(...)`
- `LoadIndexInfo(...)`
- `Capabilities()`

On-disk artifacts:

- simple: `workspace/.rag/state/index.json`
- comet: `workspace/.rag/state/index.json` + `workspace/.rag/state/vectors.json` (when embeddings enabled)

`IndexInfo.index_provider` is persisted and returned in `EvidencePackFull`.

In-memory/on-disk shape:

```go
type IndexStore struct {
    Info   IndexInfo
    Chunks []IndexedChunk
}
```

`IndexInfo` includes:

- `index_version`
- `index_state` (`healthy` / `degraded`)
- `built_at`
- `embedding_model_id`
- `chunking_hash`
- `warnings[]`
- `total_documents`
- `total_chunks`

`IndexedChunk` includes:

- identity: `source_path`, `chunk_ordinal`, `chunk_loc`
- metadata: `title`, `date`, `project`, `tags`, `confidentiality`, `doc_type`
- content: `text`, `snippet`
- safety: `flags`, `risk_score`
- diagnostics: `document_version`, `paragraph_id`

## 6. Ingestion/indexing pipeline

`BuildIndex()` pipeline:

1. Resolve config paths (`index_root`, `kb_root`)
2. Walk `kb_root` recursively
3. Keep only `.md` files
4. Enforce security gates:
   - denylist path block
   - symlink resolution remains inside workspace
5. Parse frontmatter + markdown body
6. Chunk body with `splitMarkdownChunks`
7. Normalize text and generate snippet
8. Run injection heuristics and assign risk
9. Append `IndexedChunk` entries to provider payload
10. Provider persists index in its own format

### Hard/soft limits

- `chunk_soft_bytes` default: `4096`
- `chunk_hard_bytes` default: `8192`
- `document_hard_bytes` default: `10MB`
- `max_chunks_per_document` default: `2000`

Behavior:

- attempt full indexing
- trim chunk stream if max chunk count exceeded
- skip document on hard limit or security violations

## 7. Markdown chunking details

Chunker strategy in `chunker.go`:

- split by lines
- headings (`# ...`) flush current chunk and update `heading_path`
- blank lines flush current chunk
- chunk text is bounded by size limits
- each chunk has `ChunkLoc{heading_path,start_char,end_char}`

Text normalization (`normalizeText`):

- normalize line endings
- trim spaces
- collapse whitespace with regex

## 8. Search pipeline

`Search()` pipeline:

1. Queue admission (`queue_size`, `concurrency`)
2. Resolve provider and query candidates
3. Resolve profile (`ResolveProfile`)
4. Resolve effective mode
5. Validate filters
6. Candidate selection (lexical score + filters)
7. Normalize BM25-like scores over candidates
8. Apply profile-weighted scoring
9. Apply risk penalty from guardrails
10. Deterministic sort + per-source cap
11. Build both outputs:
    - `EvidencePackFull`
    - `EvidencePackLLM` (compact)

### Current lexical scoring

- `simple` provider: token containment/count heuristic (`lexicalScore`)
- `comet` provider: BM25 scoring via `BM25SearchIndex`, optionally fused with cosine vector similarity using reciprocal rank fusion

`simple` scoring details:

- tokenize query by non-alnum Unicode split
- count occurrences per token in lowercase chunk text
- sum token counts

`comet` scoring details:

- BM25 lexical scoring for text queries
- when embeddings are available: `HybridSearchIndex` fuses BM25 + cosine vector scores via reciprocal rank fusion
- when embeddings are unavailable: falls back to BM25-only via `BM25SearchIndex`
- `ModeKeywordOnly` forces BM25-only path even when vectors exist

`Service` keeps profile weighting, filtering, risk penalty, and deterministic ordering backend-agnostic.

Candidate flow:

1. Provider returns top-N raw hits.
2. Service applies access/filter policy (`confidentiality`, tags/project/doc_type/date).
3. Service normalizes score components.
4. Service applies profile weights + risk penalty.
5. Service applies per-source cap and deterministic tie-break.

### Deterministic ordering

Final sort key:

1. `score` descending
2. `source_path` ascending
3. `chunk_ordinal` ascending

## 9. Fixed profiles (hardcoded)

Defined in `profiles.go`.

- `default_research`
  - mode: hybrid
  - topN: 120 / 120
  - weights: bm25 0.60, cosine 0.35, freshness 0.05
  - cap: 3 chunks/source

- `decisions_recent`
  - mode: hybrid
  - topN: 150 / 80
  - weights: bm25 0.65, cosine 0.20, freshness 0.15
  - fixed metadata boost for notes/policy
  - cap: 4 chunks/source

- `templates_lookup`
  - mode: keyword-only
  - topN: 200 / 0
  - weights: bm25 0.90, metadata 0.10
  - cap: 5 chunks/source

## 10. Semantic / vector search

The `comet` provider supports hybrid (BM25 + vector) search when an embedder is configured.

### Enabling embeddings

Set `allow_external_embeddings: true` in RAG config and configure an embedding provider:

```json
{
  "tools": {
    "rag": {
      "index_provider": "comet",
      "allow_external_embeddings": true
    }
  }
}
```

The embedder uses the configured LLM provider's embedding endpoint (OpenAI-compatible API). Embeddings are computed during `BuildIndex` and persisted to `vectors.json` alongside the chunk index.

### Search modes

- **hybrid** (default): fuses BM25 text scores with cosine vector similarity via reciprocal rank fusion
- **keyword-only**: BM25 only, skips vector search even if embeddings exist
- **semantic-only**: vector similarity only (falls back to keyword-only if embeddings unavailable)

When mode requires semantic but embeddings are unavailable:

- fallback to `keyword-only`
- warning note added: `semantic unavailable; fallback=keyword-only`

## 11. Filters semantics

Supported in `SearchFilters`:

- `tags`
- `tag_mode` (`any` / `all`)
- `project`
- `doc_type`
- `date_from`, `date_to`
- `confidentiality_allow`
- `allow_restricted`

Semantics:

- AND across groups
- OR within each list
- `tag_mode=all` requires all tag values to match
- restricted content is blocked unless `allow_restricted=true`
- validation fails if `restricted` requested without allow flag

## 12. Guardrails and masking

### Injection risk flags

Heuristic flags applied per chunk:

- `policy_override_attempt`
- `tool_call_attempt`
- `instruction_like`

`risk_score` in `[0,1]` is converted to ranking penalty.

### Snippet masking

`maskSecrets` redacts patterns for:

- API keys
- bearer tokens
- password assignments
- private keys (PEM/SSH)
- AWS key IDs
- JWT-like token shapes

## 13. Output contracts

### EvidencePackFull

Used by CLI/audit.
Contains rich item metadata and score breakdown.

### EvidencePackLLM

Used by `rag_search` tool to reduce token cost.

Compact design:

- one source alias table: `S1 -> source_path`
- items reference aliases: `S1#3`
- only `ref`, `snippet`, `score`, `notes`

## 14. Queueing/concurrency model

Implementation:

- bounded queue counter guarded by mutex
- worker slots via buffered channel semaphore

Config knobs:

- `queue_size`
- `concurrency`

Overflow behavior:

- `Search` returns `ErrQueueFull`
- caller can map to `busy/queue_full` with `retry_after_seconds`

## 15. Eval harness

`Eval()` supports YAML/JSON golden files.

Case fields:

- `query`
- `must_include_source_paths`
- `acceptable_source_paths`
- `must_include_chunk_refs`
- `forbidden_claims`

Current metric implementation:

- `recall@k` (required)

Exit code contract:

- `0`: success, no degradation
- `1`: technical/runtime failure
- `2`: malformed input/config/baseline
- `3`: degraded vs baseline

## 16. Error model and common failures

Primary errors:

- `ErrQueueFull`
- `ErrIndexNotBuilt`
- parse/validation errors for filters and golden files

Typical operational issues:

- missing `kb_root` content -> empty results
- denylist too broad -> many skipped files
- stale references after source file moves (known MVP behavior)

## 17. Performance characteristics

Current complexity (approx):

- Build index: `O(total_input_bytes)` + embedding API calls (batched, 64 chunks/batch)
- Search (simple): `O(total_chunks)` scan with filtering and sort
- Search (comet BM25): `O(total_chunks)` BM25 scoring
- Search (comet hybrid): BM25 + flat vector scan, fused via reciprocal rank fusion

The comet provider keeps indexes in memory and rebuilds from JSON on load. This is suitable for picoclaw's target: personal knowledge bases that fit comfortably in a few MB of RAM.

## 18. Comet provider

The `comet` provider uses [github.com/wizenheimer/comet](https://github.com/wizenheimer/comet) v0.1.1, a pure-Go library for hybrid search. No CGO, no build tags, no external dependencies.

### Architecture

- **BM25**: `comet.BM25SearchIndex` for lexical scoring
- **Vectors**: `comet.FlatIndex` with cosine distance for dense vector search
- **Hybrid**: `comet.HybridSearchIndex` fuses both via reciprocal rank fusion

### Persistence

Comet indexes are in-memory. State is persisted as JSON:

- `index.json`: chunk metadata + index info (same format as `simple` provider)
- `vectors.json`: float32 embedding vectors (only when embeddings enabled)

On startup, indexes are rebuilt from the persisted JSON. This is appropriate for picoclaw's target dataset sizes (typical KB fits in a few MB of RAM).

### Config

```json
{
  "tools": {
    "rag": {
      "index_provider": "comet"
    }
  }
}
```

No special build flags or external dependencies required.

## 19. Embedding providers

Embeddings are handled by the `Embedder` interface in `embedder.go`. The default implementation (`httpEmbedder`) calls any OpenAI-compatible `/v1/embeddings` endpoint.

Supported providers (auto-detected from LLM provider config):

- OpenAI
- Anthropic (via proxy)
- OpenRouter
- Gemini
- Zhipu
- Any OpenAI-compatible endpoint

Embeddings are opt-in: set `allow_external_embeddings: true` in RAG config. When disabled, the comet provider operates in BM25-only mode.

## 20. Testing

Package tests:

- `service_test.go`
  - build + search smoke
  - restricted filtering default
  - unknown provider error

- `provider_comet_test.go`
  - BM25-only search (no embedder)
  - hybrid search (BOW embedder)
  - persistence across provider instances
  - chunk fetch by source path + ordinal
  - keyword-only mode override

- `eval_test.go`
  - recall, ranking, filter, tag, per-source cap, score breakdown, LLM format

- `embedder_bow_test.go`
  - deterministic bag-of-words embedder for testing vector paths without API keys

Integration-adjacent test:

- `pkg/tools/rag_search_test.go`
  - validates compact JSON payload contract for tool execution

## 21. Non-goals in this package (current)

- Draft synthesis (external skill)
- PDF extraction/parsing
- Contradiction detection beyond basic notes
- User-editable profile files
