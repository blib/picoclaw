package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	"github.com/sipeed/picoclaw/pkg/rag"
)

// ANSI helpers.
const (
	cReset  = "\033[0m"
	cBold   = "\033[1m"
	cDim    = "\033[2m"
	cGreen  = "\033[32m"
	cYellow = "\033[33m"
	cCyan   = "\033[36m"
	cRed    = "\033[31m"
	cWhite  = "\033[37m"
)

func newService() (*rag.Service, error) {
	cfg, err := internal.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("error loading config: %w", err)
	}
	return rag.NewService(cfg.WorkspacePath(), cfg.Tools.RAG, cfg.Providers), nil
}

// --- index ---

func ragIndexCmd(watch bool) error {
	svc, err := newService()
	if err != nil {
		return err
	}
	defer svc.Close()

	ctx := context.Background()
	build := func() {
		info, err := svc.BuildIndex(ctx)
		if err != nil {
			fmt.Printf("%s❌ Index error:%s %v\n", cRed, cReset, err)
			return
		}
		fmt.Printf("%s✅ Index built:%s version=%s docs=%d chunks=%d warnings=%d\n",
			cGreen, cReset, info.IndexVersion, info.TotalDocuments, info.TotalChunks, len(info.Warnings))
	}

	if !watch {
		build()
		return nil
	}

	build()
	fmt.Printf("%s👀 Watch mode enabled (rebuild every 30s). Press Ctrl+C to stop.%s\n", cYellow, cReset)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	for {
		select {
		case <-sig:
			fmt.Printf("%s🛑 Stopping rag watcher%s\n", cYellow, cReset)
			return nil
		case <-ticker.C:
			build()
		}
	}
}

// --- search ---

func ragSearchCmd(query, profile, mode string, topK int, jsonOut bool) error {
	svc, err := newService()
	if err != nil {
		return err
	}
	defer svc.Close()

	req := rag.SearchRequest{
		Query:     query,
		ProfileID: profile,
		Mode:      rag.SearchMode(mode),
		TopK:      topK,
	}

	res, err := svc.Search(context.Background(), req)
	if err != nil {
		if rag.IsQueueFull(err) {
			fmt.Printf("%s⏳ busy/queue_full retry_after_seconds=%d%s\n", cYellow, svc.RetryAfterSeconds(), cReset)
			return nil
		}
		return fmt.Errorf("search error: %w", err)
	}

	if jsonOut {
		b, _ := json.MarshalIndent(res.Full, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	fmt.Printf("%s🔍 Query:%s %s\n", cBold, cReset, res.Full.Query)
	fmt.Printf("%s📋 Profile:%s %s\n", cBold, cReset, res.Full.ProfileID)
	fmt.Printf("%s📊 Items:%s %d %s│%s Sources: %d\n", cBold, cReset,
		len(res.Full.Items), cDim, cReset, res.Full.Coverage.UniqueSources)
	fmt.Println()
	for i, item := range res.Full.Items {
		fmt.Printf("  %s%d.%s %s%s#%d%s  score=%s%.3f%s\n",
			cBold, i+1, cReset,
			cCyan, item.SourcePath, item.ChunkRef.ChunkOrdinal, cReset,
			cGreen, item.Score, cReset)
		fmt.Printf("     %s%s%s\n", cDim, safePreview(item.Text, 120), cReset)
	}
	if len(res.Full.Notes) > 0 {
		fmt.Printf("\n%s📝 Notes:%s\n", cYellow, cReset)
		for _, n := range res.Full.Notes {
			fmt.Printf("  %s•%s %s\n", cYellow, cReset, n)
		}
	}
	return nil
}

// --- chunk ---

func ragChunkCmd(sourcePath string, ordinal int, jsonOut bool) error {
	svc, err := newService()
	if err != nil {
		return err
	}
	defer svc.Close()

	chunk, err := svc.FetchChunk(context.Background(), sourcePath, ordinal)
	if err != nil {
		return fmt.Errorf("chunk error: %w", err)
	}

	if jsonOut {
		b, _ := json.MarshalIndent(chunk, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	fmt.Printf("%s📄 %s#%d%s\n", cCyan, chunk.SourcePath, chunk.ChunkOrdinal, cReset)
	fmt.Printf("%s🏷  Heading:%s %s\n", cBold, cReset, chunk.ChunkLoc.HeadingPath)
	fmt.Printf("\n%s\n", chunk.Text)
	return nil
}

// --- info ---

func ragInfoCmd(jsonOut bool) error {
	svc, err := newService()
	if err != nil {
		return err
	}
	defer svc.Close()

	cfg := svc.Config()
	ctx := context.Background()
	info, infoErr := svc.GetIndexInfo(ctx)

	if jsonOut {
		out := map[string]any{
			"config": map[string]any{
				"enabled":                   cfg.Enabled,
				"index_provider":            cfg.IndexProvider,
				"chunk_strategy":            svc.ChunkerName(),
				"chunk_soft_bytes":          cfg.ChunkSoftBytes,
				"chunk_hard_bytes":          cfg.ChunkHardBytes,
				"chunk_overlap_bytes":       cfg.ChunkOverlapBytes,
				"sliding_window_units":      cfg.SlidingWindowUnits,
				"sliding_stride_units":      cfg.SlidingStrideUnits,
				"hierarchical_child_bytes":  cfg.HierarchicalChildBytes,
				"semantic_drift_threshold":  cfg.SemanticDriftThreshold,
				"kb_root":                   cfg.KBRoot,
				"index_root":                cfg.IndexRoot,
				"allow_external_embeddings": cfg.AllowExternalEmbeddings,
				"embedding_provider":        cfg.EmbeddingProvider,
				"embedding_model":           cfg.EmbeddingModelID,
				"queue_size":                cfg.QueueSize,
				"concurrency":               cfg.Concurrency,
				"document_hard_bytes":       cfg.DocumentHardBytes,
				"max_chunks_per_document":   cfg.MaxChunksPerDocument,
				"default_profile_id":        cfg.DefaultProfileID,
			},
		}
		if info != nil {
			out["index"] = info
		} else if infoErr != nil {
			out["index_error"] = infoErr.Error()
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	// Pretty print
	fmt.Printf("\n%s📚 ResearchRAG Info%s\n\n", cBold+cCyan, cReset)

	fmt.Printf("%s⚙️  Configuration%s\n", cBold+cWhite, cReset)
	fmt.Printf("  %-28s %s%s%s\n", "Enabled:", cGreen, boolIcon(cfg.Enabled), cReset)

	provider := cfg.IndexProvider
	if provider == "" {
		provider = "simple"
	}
	fmt.Printf("  %-28s %s%s%s\n", "Index provider:", cCyan, provider, cReset)
	fmt.Printf("  %-28s %s%s%s\n", "Chunk strategy:", cCyan, svc.ChunkerName(), cReset)
	fmt.Printf("  %-28s %d\n", "Chunk soft bytes:", cfg.ChunkSoftBytes)
	fmt.Printf("  %-28s %d\n", "Chunk hard bytes:", cfg.ChunkHardBytes)

	switch svc.ChunkerName() {
	case "fixed":
		fmt.Printf("  %-28s %d\n", "Overlap bytes:", cfg.ChunkOverlapBytes)
	case "sliding":
		fmt.Printf("  %-28s %d\n", "Window units:", cfg.SlidingWindowUnits)
		fmt.Printf("  %-28s %d\n", "Stride units:", cfg.SlidingStrideUnits)
	case "hierarchical":
		child := cfg.HierarchicalChildBytes
		if child <= 0 {
			child = cfg.ChunkSoftBytes / 4
		}
		fmt.Printf("  %-28s %d\n", "Child chunk bytes:", child)
	case "semantic":
		fmt.Printf("  %-28s %.3f\n", "Drift threshold:", cfg.SemanticDriftThreshold)
	}

	fmt.Printf("  %-28s %s\n", "KB root:", cfg.KBRoot)
	fmt.Printf("  %-28s %s\n", "Index root:", cfg.IndexRoot)
	fmt.Printf("  %-28s %s%s%s\n", "External embeddings:", cGreen, boolIcon(cfg.AllowExternalEmbeddings), cReset)
	if cfg.AllowExternalEmbeddings {
		fmt.Printf("  %-28s %s / %s\n", "Embedding:", cfg.EmbeddingProvider, cfg.EmbeddingModelID)
	}
	fmt.Printf("  %-28s %d / %d\n", "Queue / concurrency:", cfg.QueueSize, cfg.Concurrency)
	fmt.Printf("  %-28s %s\n", "Default profile:", cfg.DefaultProfileID)
	fmt.Println()

	if infoErr != nil {
		fmt.Printf("%s📦 Index%s\n", cBold+cWhite, cReset)
		if infoErr.Error() == "rag index not built" {
			fmt.Printf("  %s⚠️  Not built yet%s — run: picoclaw rag index\n", cYellow, cReset)
		} else {
			fmt.Printf("  %s❌ Error:%s %v\n", cRed, cReset, infoErr)
		}
		fmt.Println()
		return nil
	}

	fmt.Printf("%s📦 Index%s\n", cBold+cWhite, cReset)
	stateColor := cGreen
	stateIcon := "✅"
	if info.IndexState != "healthy" {
		stateColor = cYellow
		stateIcon = "⚠️"
	}
	fmt.Printf("  %-28s %s%s %s%s\n", "State:", stateColor, stateIcon, info.IndexState, cReset)
	fmt.Printf("  %-28s %s\n", "Version:", info.IndexVersion)
	fmt.Printf("  %-28s %s\n", "Built at:", info.BuiltAt)
	fmt.Printf("  %-28s %s%d%s\n", "Documents:", cCyan, info.TotalDocuments, cReset)
	fmt.Printf("  %-28s %s%d%s\n", "Chunks:", cCyan, info.TotalChunks, cReset)
	if info.ChunkingHash != "" {
		fmt.Printf("  %-28s %s%s%s\n", "Chunking hash:", cDim, info.ChunkingHash, cReset)
	}
	if info.EmbeddingModelID != "" {
		fmt.Printf("  %-28s %s\n", "Embedding model:", info.EmbeddingModelID)
	}
	if len(info.Warnings) > 0 {
		fmt.Printf("  %s⚠️  Warnings:%s\n", cYellow, cReset)
		for _, w := range info.Warnings {
			fmt.Printf("    %s•%s %s\n", cYellow, cReset, w)
		}
	}
	fmt.Println()
	return nil
}

// --- list ---

func ragListCmd(jsonOut bool) error {
	svc, err := newService()
	if err != nil {
		return err
	}
	defer svc.Close()

	docs, err := svc.ListDocuments(context.Background())
	if err != nil {
		if err.Error() == "rag index not built" {
			fmt.Printf("%s⚠️  Index not built%s — run: picoclaw rag index\n", cYellow, cReset)
			return nil
		}
		return fmt.Errorf("list error: %w", err)
	}

	if len(docs) == 0 {
		fmt.Printf("%s📭 No documents indexed%s\n", cYellow, cReset)
		return nil
	}

	if jsonOut {
		b, _ := json.MarshalIndent(docs, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	fmt.Printf("\n%s📚 Indexed Documents (%d)%s\n\n", cBold+cCyan, len(docs), cReset)
	for i, doc := range docs {
		sizeStr := formatBytes(doc.TotalBytes)
		fmt.Printf("  %s%d.%s %s📄 %s%s", cDim, i+1, cReset, cCyan, doc.SourcePath, cReset)
		fmt.Printf("  %s(%d chunks, %s)%s\n", cDim, doc.Chunks, sizeStr, cReset)

		if doc.Title != "" {
			fmt.Printf("     %s🏷  %s%s\n", cDim, doc.Title, cReset)
		}
		if len(doc.Tags) > 0 {
			fmt.Printf("     %s🔖 %s%s\n", cDim, strings.Join(doc.Tags, ", "), cReset)
		}
	}
	fmt.Println()
	return nil
}

// --- helpers ---

func boolIcon(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func formatBytes(b int) string {
	switch {
	case b >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func safePreview(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "..."
}
