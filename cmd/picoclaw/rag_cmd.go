package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sipeed/picoclaw/pkg/rag"
)

func ragCmd() {
	if len(os.Args) < 3 {
		ragHelp()
		return
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		os.Exit(1)
	}

	svc := rag.NewService(cfg.WorkspacePath(), cfg.Tools.RAG)
	sub := os.Args[2]
	args := os.Args[3:]

	switch sub {
	case "index":
		ragIndexCmd(svc, args)
	case "search":
		ragSearchCmd(svc, args)
	case "chunk":
		ragChunkCmd(svc, args)
	case "eval":
		ragEvalCmd(svc, args)
	default:
		fmt.Printf("Unknown rag command: %s\n", sub)
		ragHelp()
		os.Exit(1)
	}
}

func ragHelp() {
	fmt.Println("\nResearchRAG commands")
	fmt.Println()
	fmt.Println("Usage: picoclaw rag <subcommand> [options]")
	fmt.Println()
	fmt.Println("Subcommands:")
	fmt.Println("  index        Build/update local RAG index")
	fmt.Println("  search       Query indexed knowledge base")
	fmt.Println("  chunk        Fetch chunk text by source path + ordinal")
	fmt.Println("  eval         Evaluate retrieval with golden set")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  picoclaw rag index --full")
	fmt.Println("  picoclaw rag search --query \"where did we discuss caching\" --json")
	fmt.Println("  picoclaw rag chunk --source-path kb/notes/2026-02-18-meeting.md --chunk-ordinal 3")
	fmt.Println("  picoclaw rag eval --golden kb/golden.yml --baseline workspace/.rag/reports/last.json")
}

func ragIndexCmd(svc *rag.Service, args []string) {
	watch := false
	for _, a := range args {
		if a == "--watch" {
			watch = true
		}
	}

	ctx := context.Background()
	build := func() {
		info, err := svc.BuildIndex(ctx)
		if err != nil {
			fmt.Printf("Index error: %v\n", err)
			return
		}
		fmt.Printf("Index built: version=%s docs=%d chunks=%d warnings=%d\n", info.IndexVersion, info.TotalDocuments, info.TotalChunks, len(info.Warnings))
	}

	if !watch {
		build()
		return
	}

	build()
	fmt.Println("Watch mode enabled (rebuild every 30s). Press Ctrl+C to stop.")
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	for {
		select {
		case <-sig:
			fmt.Println("Stopping rag watcher")
			return
		case <-ticker.C:
			build()
		}
	}
}

func ragSearchCmd(svc *rag.Service, args []string) {
	req := rag.SearchRequest{}
	jsonOut := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--query":
			if i+1 < len(args) {
				req.Query = args[i+1]
				i++
			}
		case "--profile":
			if i+1 < len(args) {
				req.ProfileID = args[i+1]
				i++
			}
		case "--mode":
			if i+1 < len(args) {
				req.Mode = rag.SearchMode(args[i+1])
				i++
			}
		case "--top-k":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					req.TopK = n
				}
				i++
			}
		case "--json":
			jsonOut = true
		}
	}

	if strings.TrimSpace(req.Query) == "" {
		fmt.Println("Error: --query is required")
		os.Exit(1)
	}

	res, err := svc.Search(context.Background(), req)
	if err != nil {
		if rag.IsQueueFull(err) {
			fmt.Printf("busy/queue_full retry_after_seconds=%d\n", svc.RetryAfterSeconds())
			os.Exit(1)
		}
		fmt.Printf("Search error: %v\n", err)
		os.Exit(1)
	}

	if jsonOut {
		b, _ := json.MarshalIndent(res.Full, "", "  ")
		fmt.Println(string(b))
		return
	}

	fmt.Printf("Query: %s\n", res.Full.Query)
	fmt.Printf("Profile: %s\n", res.Full.ProfileID)
	fmt.Printf("Items: %d | Sources: %d\n", len(res.Full.Items), res.Full.Coverage.UniqueSources)
	for i, item := range res.Full.Items {
		fmt.Printf("%d. %s#%d score=%.3f\n", i+1, item.SourcePath, item.ChunkRef.ChunkOrdinal, item.Score)
		fmt.Printf("   %s\n", item.Snippet)
	}
	if len(res.Full.Notes) > 0 {
		fmt.Println("Notes:")
		for _, n := range res.Full.Notes {
			fmt.Printf("- %s\n", n)
		}
	}
}

func ragChunkCmd(svc *rag.Service, args []string) {
	sourcePath := ""
	ordinal := 0
	jsonOut := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--source-path":
			if i+1 < len(args) {
				sourcePath = args[i+1]
				i++
			}
		case "--chunk-ordinal":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					ordinal = n
				}
				i++
			}
		case "--json":
			jsonOut = true
		}
	}

	if sourcePath == "" || ordinal <= 0 {
		fmt.Println("Usage: picoclaw rag chunk --source-path <path> --chunk-ordinal <n> [--json]")
		os.Exit(1)
	}

	chunk, err := svc.FetchChunk(context.Background(), sourcePath, ordinal)
	if err != nil {
		fmt.Printf("Chunk error: %v\n", err)
		os.Exit(1)
	}

	if jsonOut {
		b, _ := json.MarshalIndent(chunk, "", "  ")
		fmt.Println(string(b))
		return
	}

	fmt.Printf("%s#%d\n", chunk.SourcePath, chunk.ChunkOrdinal)
	fmt.Printf("Heading: %s\n", chunk.ChunkLoc.HeadingPath)
	fmt.Printf("Text:\n%s\n", chunk.Text)
}

func ragEvalCmd(svc *rag.Service, args []string) {
	golden := ""
	baseline := ""
	profile := ""
	jsonOut := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--golden":
			if i+1 < len(args) {
				golden = args[i+1]
				i++
			}
		case "--baseline":
			if i+1 < len(args) {
				baseline = args[i+1]
				i++
			}
		case "--profile":
			if i+1 < len(args) {
				profile = args[i+1]
				i++
			}
		case "--json":
			jsonOut = true
		}
	}

	if golden == "" {
		fmt.Println("Error: --golden is required")
		os.Exit(2)
	}

	report, code, err := svc.Eval(context.Background(), golden, baseline, profile)
	if err != nil {
		fmt.Printf("Eval error: %v\n", err)
		os.Exit(code)
	}

	if jsonOut {
		b, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(b))
	} else {
		fmt.Printf("Eval run: %s\n", report.RunID)
		fmt.Printf("Profile: %s\n", report.ProfileID)
		fmt.Printf("Recall@k: %.4f\n", report.Metrics.RecallAtK)
		if report.Degradation {
			fmt.Println("Degradation: true")
			for _, r := range report.DegradationReasons {
				fmt.Printf("- %s\n", r)
			}
		} else {
			fmt.Println("Degradation: false")
		}
	}

	os.Exit(code)
}
