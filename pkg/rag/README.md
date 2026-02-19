# pkg/rag

ResearchRAG MVP engine for PicoClaw.

## 1. Scope and status

`pkg/rag` is the retrieval core behind:

- CLI: `picoclaw rag index|search|chunk|eval`
- Tool: `rag_search`

This package implements a **stable MVP API** with a **pluggable index provider**:

- `simple` (default): JSON-backed local index
- `bleve` (optional build tag): embedded Bleve index

## 2. Package layout

- `types.go`: request/response/index/eval models
- `profiles.go`: fixed profile definitions + resolver
- `chunker.go`: markdown chunk splitting and text normalization
- `provider.go`: pluggable provider contracts + factory
- `provider_simple.go`: JSON-backed provider
- `provider_bleve.go`: Bleve provider (build tag `bleve`)
- `service.go`: indexing, search orchestration, queueing, eval, masking, filtering
- `service_test.go`: smoke tests for index/search/filter behavior

Related integration:

- `pkg/tools/rag_search.go`: tool wrapper using compact LLM output
- `cmd/picoclaw/rag_cmd.go`: CLI command handlers

## 3. Core API

Service constructor:

```go
svc := rag.NewService(workspace, cfg.Tools.RAG)
```

Provider selection:

- `cfg.Tools.RAG.IndexProvider = "simple"` (default)
- `cfg.Tools.RAG.IndexProvider = "bleve"` (requires `-tags bleve`)

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

- `simple` (default)
- `bleve` (requires compile with `-tags bleve`)

Provider contract (`IndexProvider`):

- `Build(...)`
- `Search(...)`
- `FetchChunk(...)`
- `LoadIndexInfo(...)`
- `Capabilities()`

On-disk artifacts:

- simple: `workspace/.rag/state/index.json`
- bleve: `workspace/.rag/state/bleve/` + `workspace/.rag/state/index_info.json`

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
- `bleve` provider: embedded Bleve scoring over indexed fields

`simple` scoring details:

- tokenize query by non-alnum Unicode split
- count occurrences per token in lowercase chunk text
- sum token counts

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

## 10. Semantic mode behavior

Current status:

- semantic API shape exists
- semantic provider execution is not yet wired in MVP internals

When mode requires semantic but unavailable:

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

## 17. Performance characteristics (MVP)

Current complexity (approx):

- Build index: `O(total_input_bytes)`
- Search: `O(total_chunks)` scan with filtering and sort

This is acceptable for small/medium local KB and designed to be replaced by Bleve indexing/query execution for scale.

## 18. Bleve build

Bleve provider is optional to keep default builds dependency-light.

Enable Bleve build:

```bash
go build -tags bleve ./cmd/picoclaw
```

Install dependency before Bleve build:

```bash
go get github.com/blevesearch/bleve/v2@latest
```

Set config:

```json
{
  "tools": {
    "rag": {
      "index_provider": "bleve"
    }
  }
}
```

## 19. Bleve binary bloat: protobuf × reflect.Type DCE defeat

### The problem

Building with `-tags bleve` inflates the binary by ~40 MB (e.g. 25 MB → 71 MB on
linux/amd64). The extra weight is **not** bleve's search code — it's the Go
linker's dead-code elimination (DCE) being defeated by a subtle interaction
between two unrelated dependencies.

### How to detect it

```bash
# count "ReflectMethod" entries in the linker dependency graph
GOOS=linux GOARCH=amd64 \
  go build -tags bleve -ldflags='-dumpdep' -o /dev/null ./cmd/picoclaw/ \
  2>&1 | grep -c ReflectMethod
```

- **0** → DCE is healthy, binary is lean.
- **≥1** → the linker is keeping every exported method of every reachable type.
  Expect +30–40 MB of dead code in the binary.

### Root cause chain

The bloat requires **two** ingredients that individually are harmless:

1. **`bleve/v2` imports `index/upsidedown`** — the upsidedown package
   contains `upsidedown.pb.go` (protoc-generated). Its `init()` calls
   `protoimpl.TypeBuilder{}.Build()`, which internally calls
   `reflect.Type.Method(int)` on message descriptor types. The Go compiler
   tags this call site with `R_USEIFACEMETHOD type:reflect.Type+112`
   (offset 112 = the `Method(int) reflect.Method` entry in the
   `reflect.Type` interface).

2. **`openai-go` (or any package) embeds `reflect.Type` in a struct stored
   as `any`** — e.g. `type decoderEntry struct { reflect.Type }`. The
   promoted `Method(int)` wrapper on `decoderEntry` is tagged
   `REFLECTMETHOD` + `UsedInIface` by the compiler.
   Upstream issue: https://github.com/openai/openai-go/issues/609

When **both** are present the linker's `deadcode` pass (see
`cmd/link/internal/ld/deadcode.go`) matches the `ifaceMethod{Method, …}`
entry from (1) against the promoted wrapper from (2). This sets
`reflectSeen = true`, which forces the linker to mark **all** exported
methods of **all** reachable types as live — defeating DCE globally and
pulling in ~40 MB of otherwise-dead code (protobuf registries, gRPC stubs,
encoding tables, etc.).

Neither dependency triggers this alone. It is an emergent interaction:

```
upsidedown.pb.go init()
  → protoimpl.TypeBuilder.Build()
    → reflect.Type.Method(int)            ← emits R_USEIFACEMETHOD
                                              for Method(int)

openai-go shared.decoderEntry
  → struct { reflect.Type } stored as any ← promoted Method(int) tagged
                                              REFLECTMETHOD + UsedInIface

linker deadcode pass:
  ifaceMethod set ∋ {Method, …}
  + decoderEntry.Method is REFLECTMETHOD
  → reflectSeen = true
  → keep ALL exported methods of ALL reachable types
  → +40 MB
```

### Why upsidedown is imported even though we use scorch

`provider_bleve.go` creates indexes with `bleve.NewUsing(..., "scorch", ...)`,
never upsidedown. However, bleve's top-level package unconditionally imports
`index/upsidedown` in three files (`index.go`, `index_impl.go`,
`index_meta.go`) just to reference the constant `upsidedown.Name`
(`"upside_down"`). This single constant drags in the entire upsidedown
package, including its `.pb.go` and protobuf runtime.

### Fix

The `make build-bleve` target vendors dependencies, patches the 3 bleve
files in-place, and builds with `-mod=vendor`:

```bash
make build-bleve
```

The Makefile `vendor-patch` target runs `go mod vendor`, then uses `perl -pi`
to remove the upsidedown import and replace `upsidedown.Name` with the string
literal `"upside_down"` in 3 files:

| File            | Change                                                     |
| --------------- | ---------------------------------------------------------- |
| `index.go`      | remove import, replace `upsidedown.Name` → `"upside_down"` |
| `index_impl.go` | same                                                       |
| `index_meta.go` | same                                                       |

The upsidedown **store** subpackages (`store/boltdb`, `store/gtreap`) are
left untouched — they do not import the core upsidedown package and carry
no protobuf code. `vendor/` is gitignored and created on the fly.

The reference patch is stored in `pkg/rag/bleve-no-upsidedown.patch`.

Result:

| Build             | Binary size | ReflectMethod count |
| ----------------- | ----------- | ------------------- |
| no bleve tag      | 25 MB       | 0                   |
| bleve (unpatched) | 71 MB       | 12                  |
| bleve (patched)   | 31 MB       | 0                   |

### General detection for other projects

Any Go binary can hit this if it combines:

1. A dependency that calls `reflect.Type.Method(int)` or
   `reflect.Type.MethodByName(string)` at package init time (common in
   protobuf-generated code).
2. A dependency that embeds `reflect.Type` in a struct stored as an
   interface (uncommon but exists in some decoder/codec libraries).

To check your own binary:

```bash
go build -ldflags='-dumpdep' -o /dev/null ./... 2>&1 | grep -c ReflectMethod
```

If the count is >0, use `-ldflags='-v=2 -dumpdep'` and search for
`reached iface method:` and `markable method:` lines to identify the
matching pair, then trace which import pulls in the proto/reflect code.

## 20. Testing

Package tests:

- `service_test.go`
  - build + search smoke
  - restricted filtering default

Integration-adjacent test:

- `pkg/tools/rag_search_test.go`
  - validates compact JSON payload contract for tool execution

## 21. Non-goals in this package (current)

- Draft synthesis (external skill)
- PDF extraction/parsing
- Contradiction detection beyond basic notes
- User-editable profile files
