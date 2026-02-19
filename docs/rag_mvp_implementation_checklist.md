# ResearchRAG MVP Implementation Checklist

Status: actionable v0.1 checklist
Spec source: `/Users/blib/blib/picoclaw/SPEC.md`

## 1. Scope Lock (do first)

- [ ] Freeze MVP scope from spec:
  - fixed profiles only (`default_research`, `decisions_recent`, `templates_lookup`)
  - no editable profile file in MVP
  - no internal draft generation
  - no PDF fact extraction
- [ ] Add a short "v0.1 scope lock" note to PR description template for this feature branch.

Acceptance:
- Team agrees no extra scope is merged into v0.1.

## 2. Repo Wiring and Command Surface

Files:
- `/Users/blib/blib/picoclaw/cmd/picoclaw/main.go`

Tasks:
- [ ] Add top-level CLI command `rag`.
- [ ] Add `rag` help output with subcommands:
  - `index`
  - `search`
  - `chunk`
  - `eval`
- [ ] Keep style consistent with existing `cron`/`skills` command handlers.

Acceptance:
- `picoclaw rag --help` prints valid usage.
- Unknown subcommand returns non-zero and help text.

## 3. Config Plumbing (minimal MVP fields)

Files:
- `/Users/blib/blib/picoclaw/pkg/config/config.go`
- `/Users/blib/blib/picoclaw/config/config.example.json`
- `/Users/blib/blib/picoclaw/docs/tools_configuration.md`

Tasks:
- [ ] Add `Tools.RAG` config struct (MVP fields only):
  - `enabled`
  - `index_root`
  - `kb_root`
  - `allow_external_embeddings`
  - `embedding_provider`
  - `embedding_model_id`
  - `queue_size`
  - `concurrency`
  - `chunk_soft_bytes`
  - `chunk_hard_bytes`
  - `document_hard_bytes`
  - `max_chunks_per_document`
  - `denylist_paths`
  - `default_profile_id`
- [ ] Set sane defaults in `DefaultConfig()`.
- [ ] Document fields in example config + docs.

Acceptance:
- Config loads with zero panics using defaults.
- Missing `tools.rag` block still works (defaults applied).

## 4. New Package Skeleton

Create package:
- `/Users/blib/blib/picoclaw/pkg/rag/`

Create files:
- `/Users/blib/blib/picoclaw/pkg/rag/types.go`
- `/Users/blib/blib/picoclaw/pkg/rag/profiles.go`
- `/Users/blib/blib/picoclaw/pkg/rag/paths.go`
- `/Users/blib/blib/picoclaw/pkg/rag/chunker.go`
- `/Users/blib/blib/picoclaw/pkg/rag/indexer.go`
- `/Users/blib/blib/picoclaw/pkg/rag/search.go`
- `/Users/blib/blib/picoclaw/pkg/rag/eval.go`
- `/Users/blib/blib/picoclaw/pkg/rag/mask.go`
- `/Users/blib/blib/picoclaw/pkg/rag/queue.go`

Tasks:
- [ ] Keep package APIs small and testable:
  - `BuildIndex`
  - `UpdateIndex`
  - `Search`
  - `FetchChunk`
  - `Eval`
- [ ] Use simplified identity model:
  - `source_path` as primary source identifier
  - `chunk_ref = {source_path, chunk_ordinal}`
- [ ] Implement fixed profile table in code (no external profile parser).

Acceptance:
- Package compiles without command/tool integration.

## 5. Indexing Core (Bleve)

Tasks:
- [ ] Implement canonical source path resolver rooted at `kb/`.
- [ ] Implement denylist + symlink safety gate before read.
- [ ] Parse markdown + frontmatter (required/optional fields).
- [ ] Chunk content with ordinal assignment.
- [ ] Enforce `chunk_soft_bytes`/`chunk_hard_bytes` and document skip behavior.
- [ ] Write chunk docs into Bleve with fields needed by search + EvidencePack.
- [ ] Persist index state metadata in `workspace/.rag/state/`:
  - `index_version`
  - `index_state` (`healthy`/`degraded`)
  - `embedding_model_id`
  - `chunking_hash`

Acceptance:
- `picoclaw rag index --full` builds index from non-empty `kb/`.
- Re-running index is idempotent for same input.

## 6. Search Core

Tasks:
- [ ] Implement retrieval modes:
  - keyword-only
  - semantic-only (if enabled and available)
  - hybrid
- [ ] Implement fixed profile defaults (candidate sizes + weights).
- [ ] Enforce deterministic order:
  - `score desc -> source_path asc -> chunk_ordinal asc`
- [ ] Apply filters:
  - tags (`any`/`all`)
  - project
  - doc_type
  - date range
  - confidentiality + `allow_restricted`
- [ ] On semantic unavailable path, add warning note and fallback.
- [ ] Build `EvidencePackFull` shape for CLI/audit with:
  - `source_path`
  - `chunk_ref`
  - `chunk_loc`
  - `document_version`
  - `snippet`
  - `score`
  - `score_breakdown`
  - `notes`
- [ ] Build `EvidencePackLLM` compact shape for tool/prompt with:
  - `sources` alias map (`S1 -> source_path`)
  - compact item refs (`S1#3`)
  - `snippet`
  - `score`
  - `notes`

Acceptance:
- `picoclaw rag search --query "..."` returns stable, parseable output.
- Same query/options/index produce same ordering.

## 7. Guardrails and Safety

Tasks:
- [ ] Add injection-like heuristics flagging.
- [ ] Add risk downrank hook (small penalty in scoring).
- [ ] Implement masking in user-visible snippets:
  - API key patterns
  - JWT
  - bearer token
  - AWS key patterns
  - PEM/SSH private keys
  - password patterns
- [ ] Ensure denied/restricted files do not leak snippet text.

Acceptance:
- Unit tests prove masking for representative secret samples.
- Restricted docs excluded unless explicitly allowed.

## 8. Queue and Concurrency

Tasks:
- [ ] Add bounded search queue using config:
  - `queue_size`
  - `concurrency`
- [ ] Overflow behavior:
  - return `busy/queue_full`
  - include `retry_after_seconds`
- [ ] Add timeout/cancel handling from context.

Acceptance:
- Load test with > concurrency requests shows bounded queue and clean rejection.

## 9. CLI Subcommands Implementation

Files:
- `/Users/blib/blib/picoclaw/cmd/picoclaw/main.go`
- new helper file recommended: `/Users/blib/blib/picoclaw/cmd/picoclaw/rag_cmd.go`

Tasks:
- [ ] `rag index`:
  - `--full`
  - `--watch`
  - `--partial` (optional path list)
- [ ] `rag search`:
  - `--query`
  - `--profile`
  - `--mode`
  - `--top-k`
  - `--json`
- [ ] `rag chunk`:
  - `--source-path`
  - `--chunk-ordinal`
  - `--json`
- [ ] `rag eval`:
  - `--golden`
  - `--baseline`
  - `--json`
  - proper exit codes 0/1/2/3

Acceptance:
- All four subcommands return expected exit code behavior.

## 10. Tool Integration (`rag_search`)

Files:
- `/Users/blib/blib/picoclaw/pkg/tools/rag_search.go` (new)
- `/Users/blib/blib/picoclaw/pkg/agent/loop.go`

Tasks:
- [ ] Add tool `rag_search` implementing current tool interface.
- [ ] Register in `createToolRegistry`.
- [ ] Enforce tool runtime limits:
  - default `top_k=10`
  - max `top_k=20`
  - `snippet` <= 600 chars
  - highlights <= 3
- [ ] Keep `rag_eval` and `rag_chunk` CLI-only.

Acceptance:
- Tool is visible in startup tool summaries and callable by provider tool loop.

## 11. Eval Harness

Tasks:
- [ ] Parse golden format:
  - `query`
  - `must_include_source_paths`
  - `acceptable_source_paths`
  - `must_include_chunk_refs`
  - `forbidden_claims`
- [ ] Compute `recall@k` always.
- [ ] Compute `precision@k` when labels exist.
- [ ] Compare to baseline and set exit code:
  - 3 on metric degradation
  - degraded index alone is warning, not automatic failure

Acceptance:
- `picoclaw rag eval --golden ...` produces deterministic report and correct exit code.

## 12. Test Plan (must-have)

Create tests:
- `/Users/blib/blib/picoclaw/pkg/rag/*_test.go`
- `/Users/blib/blib/picoclaw/pkg/tools/rag_search_test.go`
- `/Users/blib/blib/picoclaw/cmd/picoclaw/rag_cmd_test.go` (or integration test style)

Minimum cases:
- [ ] canonical path + symlink safety
- [ ] denylist block
- [ ] chunk ordinal stability for unchanged file
- [ ] fallback from semantic to keyword-only
- [ ] filter semantics (`AND` groups, `OR` values, tag any/all)
- [ ] queue overflow behavior
- [ ] insufficient evidence response
- [ ] eval exit codes (0/1/2/3)

Acceptance:
- `go test ./...` passes with new tests included.

## 13. Rollout Sequence (recommended)

1. Add config + package skeleton + fixed profiles.
2. Implement `rag index` + `rag search` core.
3. Add CLI command wiring.
4. Add `rag_search` tool registration.
5. Add `rag chunk` and `rag eval`.
6. Add guardrails/masking and finalize tests.

## 14. Explicitly Deferred from MVP

- User-editable profile file.
- Advanced MMR tuning controls.
- Automatic contradiction detection beyond simple metadata hooks.
- Internal drafting engine.
- Full schema split into `schemas/*.json`.
