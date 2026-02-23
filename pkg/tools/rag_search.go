package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/rag"
)

type RAGSearchTool struct {
	service *rag.Service
	watcher *rag.Watcher
}

// NewRAGSearchTool returns nil when disabled so deployments can keep one binary
// while enforcing explicit opt-in for local RAG access.
func NewRAGSearchTool(workspace string, cfg config.RAGToolsConfig, providers config.ProvidersConfig) *RAGSearchTool {
	if !cfg.Enabled {
		return nil
	}
	svc := rag.NewService(workspace, cfg, providers)
	tool := &RAGSearchTool{service: svc}

	go func() {
		ctx := context.Background()
		svc.EnsureIndex(ctx)

		w, err := rag.NewWatcher(svc)
		if err != nil {
			// Non-fatal: search works, live updates don't.
			fmt.Fprintf(os.Stderr, "rag watcher init: %v\n", err)
			return
		}
		tool.watcher = w
		w.Start(ctx)
	}()

	return tool
}

// Stop shuts down the background file watcher, flushing any dirty index
// state to disk. Safe to call on a nil receiver or when no watcher is running.
func (t *RAGSearchTool) Stop() {
	if t != nil && t.watcher != nil {
		t.watcher.Stop()
	}
}

// Name keeps a stable tool identifier required by prompts and registry wiring.
func (t *RAGSearchTool) Name() string {
	return "rag_search"
}

// Description clarifies the compact-output contract to reduce token cost in agent loops.
func (t *RAGSearchTool) Description() string {
	return "Search local knowledge base and return compact evidence pack for LLM use"
}

// Parameters defines a strict input schema so invalid calls fail early instead
// of producing ambiguous retrieval behavior.
func (t *RAGSearchTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query",
			},
			"profile_id": map[string]interface{}{
				"type":        "string",
				"description": "Fixed profile id: default_research, decisions_recent, templates_lookup",
			},
			"mode": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"keyword-only", "semantic-only", "hybrid"},
				"description": "Retrieval mode",
			},
			"top_k": map[string]interface{}{
				"type":        "integer",
				"description": "Number of results (tool max 20)",
			},
			"filters": map[string]interface{}{
				"type":        "object",
				"description": "Optional filters to narrow search results",
				"properties": map[string]interface{}{
					"tags": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Filter by document tags (from frontmatter)",
					},
					"tag_mode": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"any", "all"},
						"description": "Match any or all tags (default: any)",
					},
					"date_from": map[string]interface{}{
						"type":        "string",
						"description": "Include docs dated on or after (YYYY-MM-DD)",
					},
					"date_to": map[string]interface{}{
						"type":        "string",
						"description": "Include docs dated on or before (YYYY-MM-DD)",
					},
				},
			},
		},
		"required": []string{"query"},
	}
}

// Execute enforces tool-safe limits and returns compact JSON to keep context
// predictable for downstream LLM reasoning.
func (t *RAGSearchTool) Execute(ctx context.Context, args map[string]interface{}) *ToolResult {
	if t.service == nil {
		return ErrorResult("rag_search is not configured")
	}

	query, _ := args["query"].(string)
	if strings.TrimSpace(query) == "" {
		return ErrorResult("query is required")
	}

	req := rag.SearchRequest{Query: query}
	if profile, ok := args["profile_id"].(string); ok {
		req.ProfileID = profile
	}
	if mode, ok := args["mode"].(string); ok {
		req.Mode = rag.SearchMode(mode)
	}
	if topK, ok := args["top_k"].(float64); ok {
		req.TopK = int(topK)
	}
	if req.TopK <= 0 {
		req.TopK = 10
	}
	if req.TopK > 20 {
		req.TopK = 20
	}

	if filtersRaw, ok := args["filters"].(map[string]interface{}); ok {
		req.Filters = parseRAGFilters(filtersRaw)
	}

	result, err := t.service.Search(ctx, req)
	if err != nil {
		if rag.IsQueueFull(err) {
			return ErrorResult(fmt.Sprintf("busy/queue_full retry_after_seconds=%d", t.service.RetryAfterSeconds()))
		}
		return ErrorResult(fmt.Sprintf("rag_search failed: %v", err))
	}

	payload := result.LLM
	if payload == nil {
		payload = &rag.EvidencePackLLM{Query: query, ProfileID: req.ProfileID, Sources: map[string]string{}, Items: nil, Notes: []string{"no results"}}
	}

	jsonBytes, _ := json.Marshal(payload)
	return SilentResult(string(jsonBytes))
}

func parseRAGFilters(raw map[string]interface{}) rag.SearchFilters {
	f := rag.SearchFilters{}
	f.Tags = toStringSlice(raw["tags"])
	f.TagMode, _ = raw["tag_mode"].(string)
	f.DateFrom, _ = raw["date_from"].(string)
	f.DateTo, _ = raw["date_to"].(string)
	return f
}

func toStringSlice(v interface{}) []string {
	if v == nil {
		return nil
	}
	switch typed := v.(type) {
	case []string:
		return typed
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, it := range typed {
			if s, ok := it.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
