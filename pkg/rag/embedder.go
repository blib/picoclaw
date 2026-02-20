package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// Shared HTTP clients for connection pooling.
// Created once at package initialization to enable connection reuse across requests.
var (
	// ollamaClient is used for quick API checks (tags, status)
	ollamaClient = &http.Client{Timeout: 5 * time.Second}
	// ollamaPullClient has a long timeout for model pulls (can take several minutes)
	ollamaPullClient = &http.Client{Timeout: 10 * time.Minute}
)

// Embedder computes dense vector representations for text chunks.
// Implementations must be safe for concurrent use.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dims() int
}

// embeddingProviderInfo holds defaults for each supported provider.
type embeddingProviderInfo struct {
	BaseURL      string
	DefaultModel string
	Dims         int
	NeedsKey     bool
}

// embeddingProviders maps provider names to their configuration defaults.
// Model choices balance quality vs size for resource-constrained devices:
//
//	openai  text-embedding-3-small  1536d  62M params, $0.02/1M tok — best quality/$ ratio
//	ollama  nomic-embed-text        768d   137M, runs local on 512MB RAM
//	nvidia  NV-Embed-QA             1024d  hosted, free tier
//	zhipu   embedding-3             2048d  hosted, free tier for low volume
//	vllm    (user picks model)      —      self-hosted, any HF model
var embeddingProviders = map[string]embeddingProviderInfo{
	"openai": {
		BaseURL:      "https://api.openai.com/v1",
		DefaultModel: "text-embedding-3-small",
		Dims:         1536,
		NeedsKey:     true,
	},
	"ollama": {
		BaseURL:      "http://localhost:11434/v1",
		DefaultModel: "nomic-embed-text",
		Dims:         768,
		NeedsKey:     false,
	},
	"nvidia": {
		BaseURL:      "https://integrate.api.nvidia.com/v1",
		DefaultModel: "NV-Embed-QA",
		Dims:         1024,
		NeedsKey:     true,
	},
	"zhipu": {
		BaseURL:      "https://open.bigmodel.cn/api/paas/v4",
		DefaultModel: "embedding-3",
		Dims:         2048,
		NeedsKey:     true,
	},
	"vllm": {
		BaseURL:      "",
		DefaultModel: "",
		Dims:         0,
		NeedsKey:     false,
	},
}

// newEmbedder constructs an Embedder from RAG config fields. Returns nil with
// a logged warning when the provider is unsupported or unconfigured — callers
// must fall back to keyword-only search.
func newEmbedder(provider, model, apiBase, apiKey string, allowExternal bool) Embedder {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return nil
	}

	if !allowExternal {
		logger.Info("rag embedder disabled: allow_external_embeddings=false")
		return nil
	}

	info, supported := embeddingProviders[provider]
	if !supported {
		logger.Warn(fmt.Sprintf("rag embedding provider %q unsupported; falling back to keyword-only", provider))
		return nil
	}

	if apiBase == "" {
		apiBase = info.BaseURL
	}
	if apiBase == "" {
		logger.Warn(fmt.Sprintf("rag embedding provider %q requires api_base; falling back to keyword-only", provider))
		return nil
	}

	if apiKey == "" && info.NeedsKey {
		logger.Warn(fmt.Sprintf("rag embedding provider %q requires api_key; falling back to keyword-only", provider))
		return nil
	}

	if model == "" {
		model = info.DefaultModel
	}
	if model == "" {
		logger.Warn(fmt.Sprintf("rag embedding provider %q requires embedding_model_id; falling back to keyword-only", provider))
		return nil
	}

	e := &httpEmbedder{
		apiBase:  strings.TrimRight(apiBase, "/"),
		apiKey:   apiKey,
		model:    model,
		provider: provider,
		dims:     info.Dims, // pre-set from provider config; 0 means discover on first call
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}

	// Ollama: auto-pull model if not available
	if provider == "ollama" {
		ollamaPullIfNeeded(e.ollamaBase(), model)
	}

	return e
}

// httpEmbedder calls an OpenAI-compatible /v1/embeddings endpoint.
type httpEmbedder struct {
	apiBase  string
	apiKey   string
	model    string
	provider string
	client   *http.Client
	dimsOnce sync.Once
	dims     int // set once from provider config or first API response
}

// ollamaBase returns the Ollama native API base (without /v1 suffix).
func (e *httpEmbedder) ollamaBase() string {
	base := e.apiBase
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/v1") {
		base = strings.TrimSuffix(base, "/v1")
	}
	return base
}

// ollamaPullIfNeeded checks if model exists in Ollama and pulls it if not.
// Best-effort: logs and continues on failure — Embed will fail with a clear
// error if the model truly isn't available.
func ollamaPullIfNeeded(ollamaBase, model string) {
	// Check if model is already available via /api/tags
	resp, err := ollamaClient.Get(ollamaBase + "/api/tags")
	if err != nil {
		logger.Info(fmt.Sprintf("ollama not reachable at %s: %v", ollamaBase, err))
		return
	}
	defer resp.Body.Close()

	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return
	}

	for _, m := range tags.Models {
		// Ollama model names may include :tag suffix
		name := strings.Split(m.Name, ":")[0]
		if name == model || m.Name == model {
			return // already pulled
		}
	}

	logger.Info(fmt.Sprintf("pulling ollama embedding model %q (first time only)...", model))
	pullBody, _ := json.Marshal(map[string]interface{}{
		"name":   model,
		"stream": false,
	})
	pullResp, err := ollamaPullClient.Post(ollamaBase+"/api/pull", "application/json", bytes.NewReader(pullBody))
	if err != nil {
		logger.Warn(fmt.Sprintf("ollama pull %q failed: %v", model, err))
		return
	}
	defer pullResp.Body.Close()
	io.ReadAll(pullResp.Body) // drain

	if pullResp.StatusCode == http.StatusOK {
		logger.Info(fmt.Sprintf("ollama model %q pulled successfully", model))
	} else {
		logger.Warn(fmt.Sprintf("ollama pull %q returned status %d", model, pullResp.StatusCode))
	}
}

type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

func (e *httpEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(embeddingRequest{
		Model: e.model,
		Input: texts,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal embedding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", e.apiBase+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("embedding API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}

	if len(result.Data) != len(texts) {
		return nil, fmt.Errorf("embedding response has %d vectors for %d inputs", len(result.Data), len(texts))
	}

	vecs := make([][]float32, len(texts))
	for _, d := range result.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("embedding response index %d out of range", d.Index)
		}
		vecs[d.Index] = d.Embedding
	}

	if e.dims == 0 && len(vecs) > 0 && len(vecs[0]) > 0 {
		e.dimsOnce.Do(func() { e.dims = len(vecs[0]) })
	}

	return vecs, nil
}

func (e *httpEmbedder) Dims() int {
	return e.dims
}


