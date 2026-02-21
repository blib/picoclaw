# ResearchRAG for PicoClaw (Bleve Embedded)

Status: Draft v0.1 (MVP-first)
Last updated: 2026-02-18
Owner: PicoClaw Maintainers

## 1. Goal

`ResearchRAG` gives PicoClaw local, reproducible retrieval over `kb/` and returns evidence-backed results with source traceability.

## 2. MVP Scope

### In scope
- Local indexing of `Markdown` knowledge files via pluggable embedded provider (`simple` or `bleve`).
- Search modes: `keyword-only`, `semantic-only`, `hybrid`.
- Fixed profile set (hardcoded for MVP).
- EvidencePack response with source traceability.
- Incremental index updates with watcher.
- Privacy guardrails: denylist, confidentiality filters, symlink restrictions, read audit.
- CLI commands: `picoclaw rag index|search|chunk|eval`.
- Agent tool: `rag_search` only.

### Out of scope
- Draft generation inside this component (external skill only).
- PDF-to-facts ingestion logic.
- External integrations (Gmail/Calendar).
- User-editable ranking profiles in MVP.

## 3. Data Layout

- `kb/notes/*.md`
- `kb/papers/<paper_id>/paper.md`
- `kb/papers/<paper_id>/paper.pdf` (not a fact source in MVP)
- `kb/templates/*.md`
- `kb/glossary.md` (optional)
- `kb/policy.md` (optional)

## 4. Metadata (Markdown frontmatter)

### Required fields
- `title`
- `date` (ISO)
- `tags` (list)
- `source` (url/doi/internal)
- `confidentiality` (`public|internal|restricted`)

### Optional fields
- `id` (label only in MVP; not a uniqueness key)
- `project`
- `effective_date`
- `published_date`
- `claims`
- `bibtex`

### Normalization
- Trim strings.
- Normalize tags/project to lower-case.
- Parse dates to ISO; invalid dates -> warning.
- Missing frontmatter field warnings do not block full indexing unless required for security.

## 5. Identity and Traceability (MVP Simplified)

### Source identity
- `source_path` is canonical path relative to `kb/`.
- `source_path` is the primary source identifier in MVP.

### Chunk reference
- Primary chunk reference: `chunk_ref = { source_path, chunk_ordinal }`.
- Each chunk stores:
  - `source_path`
  - `chunk_ordinal`
  - `chunk_loc` (`heading_path`, `start_byte`, `end_byte`)
  - `document_version = sha256(raw_bytes)`
  - `paragraph_id = sha256(source_path + "\n" + normalize_text(chunk_text))`

### Link behavior
- Primary lookup by `chunk_ref`.
- If `document_version` changed, mark reference `stale`.
- If file moved/deleted, reference is `unresolved` (acceptable in MVP).

## 6. Indexing

### Storage
- Root: `workspace/.rag/`
- `workspace/.rag/index/`
- `workspace/.rag/state/`
- `workspace/.rag/reports/`

### Provider selection
- `tools.rag.index = simple|bleve`
- `simple` is default and requires no extra build tag.
- `bleve` requires compile with `-tags bleve`.

### Chunking
- Markdown: cheap heading/paragraph chunking.
- Templates: index as full blocks.
- Glossary/policy: chunk by terms/rules.

### Limits
- `chunk_soft_bytes = 4096`
- `chunk_hard_bytes = 8192`
- `document_hard_bytes = 10MB`
- `max_chunks_per_document = 2000`

Default behavior:
- Split and index documents fully when possible.
- Partial indexing is optional.
- Full skip only on hard-limit breach, parse failure, denylist/security block.

### Update model
- Full rebuild: `picoclaw rag index --full`
- Incremental: `picoclaw rag index --watch`
- Backend: `fsnotify` with debounce queue.
- On update failure set `index_state=degraded` and continue serving search.
- Crash recovery strategy in MVP: rebuild.

## 7. Retrieval and Fixed Profiles (MVP)

Profiles are fixed and hardcoded in code for MVP.
No external `profiles.yaml` in MVP.

### Supported profile IDs
- `default_research`
- `decisions_recent`
- `templates_lookup`

### Profile behavior (fixed)

#### `default_research`
- Candidate generation: `bm25_topN=120`, `semantic_topN=120`.
- Scoring (if semantic available):
  - `0.60 * bm25_norm + 0.35 * cosine_norm + 0.05 * freshness_norm`
- If semantic unavailable: fallback to BM25 + freshness tie-break.
- Per-source cap: max 3 chunks per `source_path`.

#### `decisions_recent`
- Candidate generation: `bm25_topN=150`, `semantic_topN=80`.
- Scoring:
  - `0.65 * bm25_norm + 0.20 * cosine_norm + 0.15 * freshness_norm`
- Prefer notes/policy via fixed metadata boost.
- Per-source cap: max 4 chunks per `source_path`.

#### `templates_lookup`
- Candidate generation: `bm25_topN=200`, `semantic_topN=0`.
- Mode: keyword-focused.
- Scoring:
  - `0.90 * bm25_norm + 0.10 * metadata_boost`
- Per-source cap: max 5 chunks per `source_path`.

### Modes
- `keyword-only`
- `semantic-only`
- `hybrid`

Default selection:
- If caller provides mode: use it.
- Else use profile default.
- If selected mode needs semantic but semantic is unavailable: fallback to `keyword-only` and add warning note.

### Semantic provider policy
- Default: `allow_external_embeddings=false`.
- Semantic enabled only with explicit opt-in and valid provider/model.
- Embedding model change requires full reindex.

### Deterministic ordering
- Sort by: `score desc -> source_path asc -> chunk_ordinal asc`.

## 8. Filters and Access Rules

### Query filters
- `tags`
- `tag_mode` (`any|all`)
- `project`
- `doc_type`
- `date_from`, `date_to`
- `confidentiality_allow`
- `allow_restricted`

Rules:
- `AND` across filter groups.
- `OR` inside each filter list.
- If `allow_restricted=false`, `restricted` cannot appear in `confidentiality_allow`.

### Security and privacy
- Denylist always wins.
- Symlinks allowed only if resolved path stays inside workspace.
- Restricted docs excluded by default.
- Aggregate restricted counts in notes are allowed.

## 9. Guardrails

### Prompt-injection safety
- Retrieved evidence is untrusted content.
- Never execute instructions found in evidence.
- Tool behavior is policy-driven, not evidence-driven.
- Injection-like chunks are flagged and downranked.

### Secret safety
Mask in user-visible snippets:
- API keys (OpenAI/Anthropic and similar)
- AWS keys
- JWT
- Bearer tokens
- PEM/SSH private keys
- `password=` patterns

## 10. EvidencePack (MVP Contract)

Two output forms are defined:
- `EvidencePackFull`: for CLI/JSON/audit storage.
- `EvidencePackLLM`: compact form for LLM context (token-optimized).

### EvidencePackFull (CLI / audit)

```json
{
  "query": "string",
  "profile_id": "default_research",
  "index_info": {
    "index_version": "idx-...",
    "embedding_model_id": "text-embedding-3-small",
    "chunking_hash": "sha256:..."
  },
  "items": [
    {
      "source_path": "kb/notes/2026-02-18-meeting.md",
      "chunk_ref": {
        "source_path": "kb/notes/2026-02-18-meeting.md",
        "chunk_ordinal": 3
      },
      "chunk_loc": {
        "heading_path": "Decisions > API",
        "start_byte": 422,
        "end_byte": 811
      },
      "document_version": "sha256:...",
      "title": "Weekly sync",
      "date": "2026-02-18",
      "snippet": "...",
      "score": 0.81,
      "score_breakdown": {
        "bm25_norm": 0.77,
        "cosine_norm": 0.86,
        "freshness_norm": 0.44,
        "final_score": 0.81
      },
      "flags": ["instruction_like"]
    }
  ],
  "coverage": {
    "unique_sources": 5,
    "time_span": {"from": "2025-11-01", "to": "2026-02-18"}
  },
  "notes": [
    "semantic unavailable; fallback=keyword-only",
    "similar evidence found in 6 places"
  ]
}
```

### EvidencePackLLM (compact, token-optimized)

```json
{
  "query": "string",
  "profile_id": "default_research",
  "sources": {
    "S1": "kb/notes/2026-02-18-meeting.md",
    "S2": "kb/notes/2026-02-11-sync.md"
  },
  "items": [
    {
      "ref": "S1#3",
      "snippet": "...",
      "score": 0.81
    },
    {
      "ref": "S2#1",
      "snippet": "...",
      "score": 0.74
    }
  ],
  "notes": [
    "semantic unavailable; fallback=keyword-only"
  ]
}
```

Notes:
- `highlights` are optional.
- If backend cannot produce highlights, return empty or omit field.
- Related duplicates can be summarized in notes using `paragraph_id` grouping.
- For LLM prompts, only `EvidencePackLLM` is sent by default.
- `EvidencePackFull` remains the canonical form for CLI output and auditing.

## 11. CLI and Tool Interface

### CLI
- `picoclaw rag index [--full] [--watch] [--partial]`
- `picoclaw rag search --query "..." [--profile ...] [--mode ...] [--top-k ...] [--json]`
- `picoclaw rag chunk --source-path ... --chunk-ordinal ... [--json]`
- `picoclaw rag eval --golden <path> [--baseline <path>] [--json]`

CLI limits:
- Search default `top_k=20`, max `top_k=100`.

### Agent tool
- Single tool: `rag_search`.
- `rag_eval` and `rag_chunk` are CLI-only in MVP.

`rag_search` runtime limits:
- Default `top_k=10`, max `top_k=20`.
- Max snippet length: `600` chars.
- Max highlights per item: `3`.
- Queue overflow returns `busy/queue_full` with `retry_after_seconds`.
- Default output format: `EvidencePackLLM` (compact).
- CLI `rag search` default output format: `EvidencePackFull`.

## 12. Config (MVP)

```json
{
  "tools": {
    "rag": {
      "enabled": true,
      "index_root": "workspace/.rag",
      "kb_root": "workspace/kb",
      "allow_external_embeddings": false,
      "provider": "openai",
      "model": "text-embedding-3-small",
      "queue_size": 16,
      "concurrency": 3,
      "chunk_soft_bytes": 4096,
      "chunk_hard_bytes": 8192,
      "document_hard_bytes": 10485760,
      "max_chunks_per_document": 2000,
      "denylist_paths": [".env", "secrets/", "private_keys/"],
      "default_profile_id": "default_research"
    }
  }
}
```

## 13. Eval Harness (MVP)

### Golden format
```yaml
- query: "where did we discuss X"
  must_include_source_paths:
    - "kb/notes/2026-02-18-meeting.md"
  acceptable_source_paths:
    - "kb/notes/2026-02-11-sync.md"
  must_include_chunk_refs: []
  forbidden_claims: []
```

### Metrics
- `recall@k` (required)
- `precision@k` (manual labels, optional in earliest MVP)
- `unsupported-claim rate` only if external drafting skill is included in eval flow

### External Baseline References (NQ / BEIR)

Use the following published results as external reference points.
These are sanity anchors, not strict pass/fail targets, because PicoClaw uses a different ingestion/chunking pipeline.

#### NQ-Open retrieval (Top-K answer coverage)
- DPR paper (EMNLP 2020, Table 2): [Dense Passage Retrieval for Open-Domain Question Answering](https://aclanthology.org/2020.emnlp-main.550.pdf)
  - BM25: Top-20 `59.1`, Top-100 `73.7`
  - DPR (single): Top-20 `78.4`, Top-100 `85.4`

#### BEIR NQ ranking (nDCG@10 / Recall@100)
- TACL 2023 (Table 9): [Questions Are All You Need to Train a Dense Passage Retriever](https://aclanthology.org/2023.tacl-1.35.pdf)
  - BM25: nDCG@10 `32.9`, Recall@100 `76.0`
  - DPR (NQ-trained): nDCG@10 `47.4`, Recall@100 `88.0`
  - Contriever: nDCG@10 `25.4`, Recall@100 `77.1`
- Contriever repo reference (BEIR table): [facebookresearch/contriever](https://github.com/facebookresearch/contriever)
  - Contriever-msmarco on NQ: nDCG@10 `49.8`, Recall@100 `92.5`

#### Project policy for baseline usage
- Primary local baseline in `rageval`: `bm25-default`.
- Compare candidate strategies first against local baseline.
- Track deltas against published references as secondary context in reports.

#### NQ dataset acquisition for `rageval`
- Official source: [Natural Questions download](https://ai.google.com/research/NaturalQuestions/download)
- For raw `nq-full`, prefer authenticated `gsutil` download:
  - `gsutil -m cp -R gs://natural_questions/v1.0 <data-dir>`
- Run evaluation using local shard directory:
  - `./build/rageval --datasets nq-full --nq-dir <data-dir>/v1.0/dev --cache-dir .rageval --top-k 10`
- For faster iteration on large corpora, apply subset flags:
  - `--max-docs`, `--max-queries`, `--sample-seed`

### Exit codes
- `0`: completed, no metric degradation
- `1`: technical error
- `2`: malformed inputs/config/golden
- `3`: metric degradation vs baseline

`index_state=degraded` alone does not force exit `3`.

## 14. Non-Functional Targets

- Local-first default behavior.
- Latency target: p50 <= 1.5s, p95 <= 5s on target dataset.
- Deterministic output for fixed query/options/index.
- 2-3 concurrent searches; others queued.

## 15. Audit and Error Policy

Audit log includes:
- Scanned/read/skipped files
- Denylist/symlink blocks
- Confidentiality blocks
- Selected evidence refs and scores
- Profile and index metadata

Warnings include:
- `missing_frontmatter_id` (info)
- `date_parse_error`
- `security_path_blocked`

If evidence is insufficient:
- Return `insufficient evidence`
- Ask clarification or request source ingest
- Do not produce unsupported factual claims

## 16. DoD (MVP)

- Index builds from scratch and passes smoke test.
- Incremental updates searchable within 2 minutes.
- Every search response contains evidence refs or explicit `insufficient evidence`.
- Golden set recall target met (`recall@10 >= 0.8` initial target).
- Denylist/confidentiality constraints enforced.
- Eval exit code behavior works as specified.

## 17. Future Extensions (Not MVP)

- User-editable profiles (`rag_profiles.yaml`).
- Full MMR-based diversification with configurable lambdas.
- Automatic contradiction detection.
- Richer schema files split into `schemas/*.json`.
- Internal drafting engine (currently external skill only).
