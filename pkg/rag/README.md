# pkg/rag

ResearchRAG engine for PicoClaw.

## Overview

`pkg/rag` is a local-first retrieval engine for personal knowledge bases. It turns markdown files under `kb_root` into searchable chunks, stores an index on disk, and serves fast evidence retrieval for both CLI (`picoclaw rag ...`) and tool use (`rag_search`).

How it works end-to-end:

1. Ingest markdown files from `kb_root` (with workspace/symlink safety checks).
2. Split documents into chunks using the configured strategy (`markdown` default).
3. Persist index artifacts under `index_root/state`.
4. Execute search with provider backend:
   - `simple`: token-count lexical retrieval, smallest footprint.
   - `comet`: BM25 lexical retrieval, optional vector hybrid when embeddings are enabled.
5. Apply policy/filters/guardrails and return both rich audit output and compact LLM output.

Design goals and operational concerns:

- **Small-memory default path**: `simple` provider and `comet` keyword-only mode work without embeddings and are best for constrained devices.
- **Semantic search is opt-in**: vectors are only built when `allow_external_embeddings=true`; this increases RAM roughly linearly with corpus size and embedding dimensions.
- **CGO is a hard backend constraint**: with `CGO_ENABLED=0`, we cannot use true on-disk ANN engines (for example FAISS-based backends), so vector search remains in-memory and is RAM-bounded.
- **Crash consistency**: comet uses a dirty-flag + two-phase rebuild/flush protocol to recover safely after interruptions.
- **Portability first**: pure-Go implementation (`CGO_ENABLED=0` compatible) for single-binary cross-platform deployment.
- **Binary size budget**: current measured build in this workspace is ~26 MB; symbol-attributed `pkg/rag + comet + bbolt` is ~0.79% (directional estimate, see Section 20.4).
- **Current combined symbol size**: `pkg/rag + comet + bbolt` = `212,358` bytes (~`0.79%` of binary).

For sizing and tradeoffs, see:

- Section 20.2 for runtime memory formulas
- Section 20.3 for the 10 MiB document-size example
- Section 20.4 for measured binary impact in this workspace

## 1. Scope and status

`pkg/rag` is the retrieval core behind:

- CLI: `picoclaw rag index|search|chunk|info|list`
- Tool: `rag_search`

This package implements a **stable API** with a **pluggable index provider**:

- `simple` (default): JSON-backed local index with token-count scoring
- `comet`: pure-Go hybrid search — BM25 lexical + cosine vector similarity via [comet](https://github.com/wizenheimer/comet)

## 2. Package layout

- `types.go`: request/response/index/eval models
- `profiles.go`: fixed profile definitions + resolver
- `chunker.go`: FixedSizeChunker and MarkdownChunker implementations
- `chunker_paragraph.go`: ParagraphPacker strategy
- `chunker_sliding.go`: UnitSlidingWindow strategy
- `chunker_hierarchical.go`: Hierarchical (parent/child) strategy
- `chunker_semantic.go`: SemanticDrift strategy (pairwise cosine)
- `chunker_registry.go`: factory function `NewChunkerFromConfig`
- `unit_parser.go`: shared markdown unit parser (headings, paragraphs, code blocks, tables, list items)
- `provider.go`: pluggable provider contracts + factory
- `provider_simple.go`: JSON-backed provider
- `provider_comet.go`: Comet provider (BM25 + vector hybrid)
- `store.go`: bbolt + flat-binary persistence layer for comet provider (dirty flag, CRC-protected vectors)
- `watcher.go`: filesystem watcher with two-tier debounced re-indexing and flushing
- `embedder.go`: embedding interface + HTTP provider (OpenAI-compatible)
- `service.go`: indexing, search orchestration, queueing, eval, masking, filtering
- `service_test.go`: smoke tests for index/search/filter behavior
- `provider_comet_test.go`: comet provider unit tests (BM25, hybrid, persistence)
- `watcher_test.go`: watcher integration tests (reindex, flush, dirty flag, event filtering)
- `chunker_test.go`: unit tests for all 6 chunking strategies + registry
- `unit_parser_test.go`: unit parser tests
- `mock_embedder_test.go`: deterministic hash-based test embedder (64-dim, FNV-1a)

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

- `document_version = base64url_raw(sha256(raw_bytes))` (43 chars vs 64 for hex)
- If content changes, references are still resolvable by `chunk_ref`, but semantic meaning may drift (known MVP tradeoff)

## 5. Provider model

Configured by `tools.rag.index`:

- `simple` (default) — JSON-backed, token-count scoring, zero dependencies
- `comet` — pure-Go BM25 + optional vector hybrid via cosine similarity

Provider contract (`IndexProvider`):

- `Build(...)`
- `Search(...)`
- `FetchChunk(...)`
- `LoadIndexInfo(...)`
- `Capabilities()`

Extended contract (`FlushableProvider`, implemented by `comet`):

- `BuildInMemory(ctx, chunks, info)` — rebuild in-memory indexes without persisting; marks dirty flag
- `Flush() error` — persist current in-memory state to disk; clears dirty flag
- `Invalidate()` — discard all in-memory state (forces reload on next query)
- `IsDirty() bool` — returns whether in-memory state has unpersisted changes

The watcher uses this two-phase protocol: fast `BuildInMemory` on file changes, deferred `Flush` on a longer debounce. If the process exits between the two, the dirty flag in bbolt ensures a full rebuild on next start.

On-disk artifacts:

- simple: `workspace/.rag/state/index.json`
- comet: `workspace/.rag/state/index.db` (bbolt — metadata + chunks) + `workspace/.rag/state/vectors.bin` (flat binary, when embeddings enabled)

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
- `files_fingerprint`
- `warnings[]`
- `total_documents`
- `total_chunks`

`IndexedChunk` includes:

- identity: `source_path`, `chunk_ordinal`, `chunk_loc`
- metadata: `title`, `date`, `tags`
- content: `text`
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
7. Normalize text
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

## 7. Chunking strategies

PicoClaw supports 6 pluggable chunking strategies, selectable via `chunk_strategy` in RAG config. The default is `markdown`.

### Strategy overview

| Strategy          | Config key     | Description                                                                 | Best for                               |
| ----------------- | -------------- | --------------------------------------------------------------------------- | -------------------------------------- |
| FixedWindow       | `fixed`        | Byte-window with optional overlap                                           | Flat text, logs                        |
| MarkdownStructure | `markdown`     | Heading-aware split preserving atomic blocks                                | General markdown (default)             |
| ParagraphPacker   | `paragraph`    | Packs markdown units (paragraphs, code blocks, tables) up to soft limit     | Dense prose                            |
| UnitSlidingWindow | `sliding`      | Sliding window over markdown units with configurable stride                 | Overlapping context recall             |
| Hierarchical      | `hierarchical` | Parent (full section) + child (sub-chunks) with linkage                     | Long documents needing section context |
| SemanticDrift     | `semantic`     | Pairwise cosine similarity between consecutive units, splits on topic drift | Topic-diverse documents                |

### Shared unit parser

Strategies 3–6 use a shared markdown unit parser (`unit_parser.go`) that produces atomic `MarkdownUnit` values:

- `UnitHeading`: heading lines (level tracked)
- `UnitParagraph`: contiguous non-blank text
- `UnitCodeBlock`: fenced code blocks (backtick or tilde), kept atomic
- `UnitTable`: pipe-delimited table blocks, kept atomic
- `UnitListItem`: list items with continuation lines

Helper functions: `HeadingPathAt()`, `UnitBytes()`, `JoinUnits()`.

### Strategy details

**FixedWindow** (`chunker.go`)

- Splits content into byte-sized windows
- `ChunkHardBytes` controls window size, `ChunkOverlapBytes` controls overlap
- No markdown awareness

**MarkdownStructure** (`chunker.go`)

- Heading-aware: headings flush current chunk and update `heading_path`
- Preserves atomic code blocks and tables (never splits mid-block)
- Bounded by `ChunkSoftBytes` / `ChunkHardBytes`
- Each chunk has `ChunkLoc{heading_path, start_byte, end_byte}`

**ParagraphPacker** (`chunker_paragraph.go`)

- Greedily packs markdown units until `MaxSize` (= `ChunkSoftBytes`) exceeded
- Oversized single units become standalone chunks
- Heading path tracked via `HeadingPathAt()`

**UnitSlidingWindow** (`chunker_sliding.go`)

- Window of `SlidingWindowUnits` units, advancing by `SlidingStrideUnits`
- `MaxBytes` (= `ChunkSoftBytes`) truncation per window
- Overlapping windows improve recall for queries spanning unit boundaries

**Hierarchical** (`chunker_hierarchical.go`)

- Groups content by top-level headings (level ≤ 2) into parent chunks
- Each parent is sub-chunked into children via size-based packing
- Children carry `ParentIndex` linking back to their parent
- Parent chunks have `IsParent = true` in `IndexedChunk`
- `HierarchicalChildBytes` controls child chunk size (default: `ChunkSoftBytes / 4`)

**SemanticDrift** (`chunker_semantic.go`)

- Computes pairwise cosine similarity between consecutive unit embeddings
- Splits when similarity drops below `SemanticDriftThreshold`
- Falls back to size-only splitting when embedder is nil or fails
- Hard split on `MaxSize` (= `ChunkSoftBytes`) regardless of similarity
- Requires `Embedder` for semantic mode; works without it as a size-only packer

### Config fields

| Field                    | JSON                       | Default            | Used by                                              |
| ------------------------ | -------------------------- | ------------------ | ---------------------------------------------------- |
| `ChunkStrategy`          | `chunk_strategy`           | `"markdown"`       | All                                                  |
| `ChunkSoftBytes`         | `chunk_soft_bytes`         | `4096`             | markdown, paragraph, sliding, hierarchical, semantic |
| `ChunkHardBytes`         | `chunk_hard_bytes`         | `8192`             | fixed, markdown                                      |
| `ChunkOverlapBytes`      | `chunk_overlap_bytes`      | `0`                | fixed                                                |
| `SlidingWindowUnits`     | `sliding_window_units`     | `0` (must set)     | sliding                                              |
| `SlidingStrideUnits`     | `sliding_stride_units`     | `0` (must set)     | sliding                                              |
| `HierarchicalChildBytes` | `hierarchical_child_bytes` | `ChunkSoftBytes/4` | hierarchical                                         |
| `SemanticDriftThreshold` | `semantic_drift_threshold` | `0.0` (must set)   | semantic                                             |

### Chunking hash

The chunking hash includes the strategy name: `strategy:softBytes:hardBytes:docHardBytes`. This ensures index rebuilds when strategy changes.

### Text normalization (`normalizeText`)

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
10. Apply min-score cutoff (request > config > profile)
11. Deterministic sort + per-source cap
12. Build both outputs:
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
2. Service applies filters (tags, date range).
3. Service normalizes score components.
4. Service applies profile weights + risk penalty.
5. Drop candidates below effective `min_score` (priority: request > config > profile).
6. Service applies per-source cap and deterministic tie-break.

### Score cutoff (`min_score`)

After scoring, candidates with `final_score < min_score` are dropped.
Resolution order (first non-zero wins):

1. `SearchRequest.MinScore` — per-request override (pointer, nil = skip)
2. `config.Tools.RAG.MinScore` — global config override
3. `FixedProfile.MinScore` — profile default (0.05 for research, 0.10 for decisions)

When all candidates are dropped, the response returns `"insufficient evidence"` with a note
like `min_score=0.050 dropped 12 low-relevance candidate(s)`.

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
  - min_score: 0.05
  - cap: 3 chunks/source

- `decisions_recent`
  - mode: hybrid
  - topN: 150 / 80
  - weights: bm25 0.65, cosine 0.20, freshness 0.15
  - fixed metadata boost for notes/policy
  - min_score: 0.10
  - cap: 4 chunks/source

- `templates_lookup`
  - mode: keyword-only
  - topN: 200 / 0
  - weights: bm25 0.90, metadata 0.10
  - min_score: 0.05
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

The embedder uses the configured LLM provider's embedding endpoint (OpenAI-compatible API). Embeddings are computed during `BuildIndex` (or `BuildInMemory` for live re-indexing) and persisted to `vectors.bin` (CRC-protected flat binary, format v1) alongside the chunk index.

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
- `date_from`, `date_to`

Semantics:

- AND across groups
- OR within each list
- `tag_mode=all` requires all tag values to match

## 12. Guardrails and masking

### Injection risk flags

Heuristic flags applied per chunk:

- `policy_override_attempt`
- `tool_call_attempt`
- `instruction_like`

`risk_score` in `[0,1]` is converted to ranking penalty.

### Secret masking

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
- only `ref`, `text`, `score`, `notes`

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

The comet provider keeps BM25/vector indexes in memory and rebuilds from bbolt on load. Persistence uses bbolt for chunks/metadata and a CRC-protected flat binary file for vectors (~4x smaller than JSON). Live re-indexing via the FS watcher uses two-tier debounce (2 s reindex / 30 s flush) to balance freshness against disk write frequency. Suitable for picoclaw's target: personal knowledge bases on constrained hardware.

## 18. Comet provider

The `comet` provider uses [github.com/wizenheimer/comet](https://github.com/wizenheimer/comet) v0.1.1, a pure-Go library for hybrid search. No CGO, no build tags, no external dependencies.

### Why pure Go (CGO_ENABLED=0)

PicoClaw targets single-binary cross-compilation for RISC-V, ARM, and x86 — including $10 boards where a C toolchain may not exist. The `CGO_ENABLED=0` constraint makes this possible but severely limits the choice of retrieval backends:

**What CGO would unlock:**

- **SQLite + vector extensions** (sqlite-vec, sqlite-vss) — mature on-disk index, B-tree + ANN in one file, battle-tested concurrency
- **FAISS** (via cgo bindings) — production-grade ANN with IVF, HNSW, PQ; disk-mapped indexes for datasets that don't fit in RAM
- **DuckDB** — columnar analytics + vector search, excellent for hybrid BM25+vector workloads

**Current tradeoff:**

Comet keeps all indexes (BM25 + flat vector) in memory, persisted to bbolt + flat binary on disk. This works well for personal knowledge bases (hundreds to low thousands of documents). The full index is rebuilt into memory on startup from the on-disk representation.

**Known scaling wall:**

Once the index outgrows available RAM, there are no adequate pure-Go solutions for disk-backed ANN search. The pure-Go vector index ecosystem is limited to flat scans and toy HNSW implementations — nothing comparable to FAISS IVF/PQ or sqlite-vec for on-disk operation.

In practical terms: the `CGO_ENABLED=0` requirement is the blocking constraint for using FAISS-backed on-disk ANN in this package. As long as this constraint stays, retrieval quality can scale, but index residency remains memory-bound.

**Decision to revisit:**

The CGO constraint should be reconsidered if any of these become true:

1. Knowledge base grows beyond ~10k documents / ~100k chunks (RAM pressure)
2. Multi-user or shared-server deployment (concurrent disk-backed queries needed)
3. Cross-compilation can be solved via static linking or pre-built C libraries per target

For now, the pure-Go path is the right call — it keeps the build simple and the binary portable. But this is a conscious tradeoff, not a permanent architectural boundary.

### Architecture

- **BM25**: `comet.BM25SearchIndex` for lexical scoring
- **Vectors**: `comet.FlatIndex` with cosine distance for dense vector search
- **Hybrid**: `comet.HybridSearchIndex` fuses both via reciprocal rank fusion

### Persistence

Comet indexes are in-memory. State is persisted via `store.go`:

- `index.db` (bbolt): two buckets — `meta` (index info as JSON) and `chunks` (each chunk JSON-encoded, keyed by uint32 ordinal in big-endian). Opened per-operation (no held file lock between calls).
- `vectors.bin` (flat binary, format v1): magic + CRC-protected. Layout:

  ```
  Offset  Size   Field
  0       4B     Magic "PCVF" (PicoClaw Vector File)
  4       2B     Version (uint16 LE, currently 1)
  6       2B     Reserved (zero)
  8       4B     Count (uint32 LE, number of vectors)
  12      4B     Dims (uint32 LE, dimensions per vector)
  16      N×D×4B Payload (float32 LE, row-major)
  16+P    4B     CRC32-C (Castagnoli, over header + payload)
  ```

  ~4x smaller than JSON. Only written when embeddings are enabled. CRC validation on load detects corruption; magic + version enable format evolution.

On load (`ensureLoaded`): chunks are read from bbolt, vectors from the binary file (magic/version/CRC validated), and comet in-memory indexes (BM25, FlatIndex, HybridSearchIndex) are rebuilt. If the dirty flag is set (previous crash before flush), `ensureLoaded` returns `ErrDirtyIndex` and `BuildIndex` must be called to rebuild.

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

### Dirty flag

The bbolt `meta` bucket stores a `dirty` key (`0x01` = dirty, absent/`0x00` = clean). The dirty flag is set when `BuildInMemory` updates in-memory state without persisting, and cleared when `Flush` writes everything to disk.

On startup, if `IsDirty()` returns true, `ensureLoaded` returns `ErrDirtyIndex` — the caller must run a full `BuildIndex` before searches will work. This guarantees crash-consistency: if the process dies between an in-memory rebuild and a flush, the index is automatically rebuilt on next start.

### FS watcher

`watcher.go` provides live re-indexing when knowledge base files change on disk. It uses `fsnotify` (already in go.mod) to watch `kb_root` recursively.

**Two-tier debounce:**

| Tier    | Default | Action                                                         | Cost        |
| ------- | ------- | -------------------------------------------------------------- | ----------- |
| Reindex | 2 s     | `BuildInMemory` — rebuild in-memory indexes from changed files | CPU, no I/O |
| Flush   | 30 s    | `Flush` — persist to bbolt + vectors.bin, clear dirty flag     | Disk write  |

The short reindex debounce keeps search results fresh within seconds of a file save. The long flush debounce batches disk writes so rapid edits don't thrash storage — important on SD cards and eMMC.

**Lifecycle:**

```go
w := rag.NewWatcher(svc,
    rag.WithReindexDebounce(2*time.Second),
    rag.WithFlushDebounce(30*time.Second),
)
w.Start(ctx)   // begins watching in background goroutine
// ...
w.Stop()       // flushes if dirty, closes fsnotify
```

**Event filtering:** only `.md` file changes trigger re-indexing. Directory creates are auto-watched. Chmod-only events are ignored.

**Graceful shutdown:** `Stop()` calls `flushIfDirty()` to persist any pending in-memory state before closing, preventing unnecessary rebuilds on next start.

## 19. Embedding providers

Embeddings are handled by the `Embedder` interface in `embedder.go`. The default implementation (`httpEmbedder`) calls any OpenAI-compatible `/v1/embeddings` endpoint.

Supported providers (configured via `tools.rag.provider`):

| Provider | Default `api_base` | Default model | Dims | Needs key |
|----------|-------------------|---------------|------|-----------|
| `openai` | `https://api.openai.com/v1` | `text-embedding-3-small` | 1536 | yes |
| `ollama` | `http://localhost:11434/v1` | `nomic-embed-text` | 768 | no |
| `nvidia` | `https://integrate.api.nvidia.com/v1` | `NV-Embed-QA` | 1024 | yes |
| `zhipu` | `https://open.bigmodel.cn/api/paas/v4` | `embedding-3` | 2048 | yes |
| `vllm` | _(user must set `api_base`)_ | _(user must set `model`)_ | — | no |

When `api_base` is left empty, the provider default is used. When `api_key` is empty and the provider requires one, the embedder falls back to keyword-only with a warning.

Embeddings are opt-in: set `allow_external_embeddings: true` in RAG config. When disabled, the comet provider operates in BM25-only mode.

## 20. Memory footprint assessment (code + runtime)

This section gives a practical sizing model for `pkg/rag`, aligned with PicoClaw's lightweight goals in `ROADMAP.md` and the root `README.md`.

### 20.1 Code footprint (static)

Current codebase footprint in this repo:

- `pkg/rag`: 23 `.go` files, ~165 KB source (`wc -c` aggregate), ~236 KB directory size.
- `simple` provider path: no extra non-stdlib dependency inside `pkg/rag` runtime path.
- `comet` provider path: adds pure-Go dependencies:
  - `github.com/wizenheimer/comet` (module cache ~2.4 MB source)
  - `go.etcd.io/bbolt` (module cache ~1.0 MB source)

Notes:

- These are source/module-cache sizes, not exact binary-section deltas.
- Both providers remain `CGO_ENABLED=0` compatible and keep single-binary portability.

### 20.2 Runtime memory model

`simple` provider:

- First read loads full `IndexStore{Info, Chunks}` into memory and caches it.
- Memory scales with total chunk text + metadata object overhead.
- Best for smallest deployments when semantic search is not required.

`comet` provider (keyword/BM25 mode):

- Streams chunks from bbolt into BM25 index (`ensureLoaded`), without keeping a full `[]IndexedChunk` in steady state.
- Memory is mostly BM25 structures (token/posting lists), plus query-time temporary objects.

`comet` provider (hybrid/semantic mode):

- Adds `FlatIndex` vectors in RAM as `[]float32` per chunk.
- Vector payload is exact and dominates memory for large indexes.

Document-size sizing formulas:

- `B` = KB corpus bytes (all markdown source bytes under `kb_root`)
- `Sdoc` = average document size in bytes
- `Ddocs = ceil(B / Sdoc)` = estimated number of documents
- `E` = effective bytes per chunk after markdown-aware splitting (practical default estimate: ~3 KiB when `chunk_soft_bytes=4096`)
- `Cdoc = ceil(Sdoc / E)` = estimated chunks per document
- `Nchunks = Ddocs * Cdoc`
- Vector payload bytes: `Nchunks * dims * 4`
- `vectors.bin` on disk: `20 + Nchunks * dims * 4`
- 64-bit Go steady in-memory estimate (payload + node headers): `Nchunks * (dims*4 + ~32)`
- Build/reindex transient overhead adds one extra `[][]float32` header slice: `Nchunks * ~24` bytes (64-bit), typically much smaller than payload

### 20.3 10 MiB KB example (by average document size)

Assumptions:

- Total KB corpus size `B = 10 MiB`
- Effective chunk bytes `E = 3 KiB` (markdown default estimate)
- For this corpus size, `Nchunks ~= ceil(B/E) = 3414` (doc size distribution changes chunk boundaries, but total chunk count stays close for fixed `B` and `E`)

Default embedding dimensions from `embedder.go` provider presets:

- Ollama `nomic-embed-text`: 768d
- Nvidia `NV-Embed-QA`: 1024d
- OpenAI `text-embedding-3-small`: 1536d
- Zhipu `embedding-3`: 2048d
- vLLM/custom OpenAI-compatible: model-defined

Using `Nchunks ~= 3414` (10 MiB / 3 KiB), estimated vector RAM:

| Embedding dims | Estimated steady RAM with node overhead (`N*(dims*4+32)`) |
| -------------- | --------------------------------------------------------- |
| 768            | 10.1 MiB                                                  |
| 1024           | 13.4 MiB                                                  |
| 1536           | 20.1 MiB                                                  |
| 2048           | 26.8 MiB                                                  |

Scale rules:

- Memory is linear in corpus bytes and embedding dimensions.
- If corpus grows from 10 MiB to 50 MiB, vector memory grows ~5x.

Operational guidance for constrained boards:

- Tight RAM budget (<64 MB process target): prefer `simple`, or `comet` with `keyword-only` and embeddings disabled.
- If semantic search is required, prefer lower-dimension models (e.g., 768d) and keep chunk count bounded via chunking/profile controls.

### 20.4 Binary impact assessment (built binary)

Measured values on Mac machine:

- Symbol-size totals by package prefix:
  - `github.com/sipeed/picoclaw/pkg/rag`: 94,374 bytes
  - `github.com/wizenheimer/comet`: 44,752 bytes
  - `go.etcd.io/bbolt`: 73,232 bytes
  - Combined (these 3): 212,358 bytes (~0.79% of binary)

## 21. Testing

Package tests:

- `service_test.go`
  - build + search smoke
  - restricted filtering default
  - unknown provider error

- `provider_comet_test.go`
  - BM25-only search (no embedder)
  - hybrid search (mock embedder)
  - persistence across provider instances (bbolt + binary vectors)
  - chunk fetch by source path + ordinal
  - keyword-only mode override

- `eval_test.go`
  - recall, ranking, filter, tag, per-source cap, score breakdown, LLM format

- `mock_embedder_test.go`
  - deterministic hash-based embedder (64-dim, FNV-1a per token) for testing vector paths without API keys
  - stateless and order-independent (no growing vocabulary)

- `chunker_test.go`
  - FixedWindow: basic, overlap, no-overlap multi-chunk
  - MarkdownStructure: basic, atomic code block, atomic table, heading path
  - ParagraphPacker: basic, max size, oversized unit
  - UnitSlidingWindow: basic, overlap, more chunks than units
  - Hierarchical: basic, parent-child linkage, parent contains child content
  - SemanticDrift: without embedder, with embedder, max size enforcement
  - Registry: all strategies resolve, unknown strategy error, empty defaults to markdown

- `unit_parser_test.go`
  - headings, code blocks (backtick + tilde), unclosed fences, tables, list items, list continuations, mixed content, byte offsets, empty/blank input

- `watcher_test.go`
  - reindex on file modification (search finds new content after debounce)
  - reindex on new file creation
  - flush clears dirty flag
  - stop flushes pending dirty state
  - `isRelevantEvent` table test (7 subtests: .md filtering, chmod-only, non-markdown)

Integration-adjacent test:

- `pkg/tools/rag_search_test.go`
  - validates compact JSON payload contract for tool execution

## 22. Non-goals in this package (current)

- Draft synthesis (external skill)
- PDF extraction/parsing
- Contradiction detection beyond basic notes
- User-editable profile files
