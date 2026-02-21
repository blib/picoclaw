// rageval is a standalone binary for evaluating PicoClaw's RAG pipeline
// against standard IR benchmarks (BEIR datasets).
//
// Usage:
//
//	rageval [flags]
//	rageval --datasets scifact,nfcorpus --top-k 10 --output report.html
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/sipeed/picoclaw/pkg/rag/eval"
)

func main() {
	var (
		datasetsFlag string
		outputFlag   string
		cacheDir     string
		topK         int
		jsonOutput   bool
		listDatasets bool
	)

	flag.StringVar(&datasetsFlag, "datasets", "scifact", "comma-separated dataset names (scifact, nfcorpus, fiqa)")
	flag.StringVar(&outputFlag, "output", "rageval-report.html", "output file path (.html or .json)")
	flag.StringVar(&cacheDir, "cache-dir", ".rageval", "base directory for datasets, indices and reports")
	flag.IntVar(&topK, "top-k", 10, "max results per query")
	flag.BoolVar(&jsonOutput, "json", false, "output raw JSON instead of HTML")
	flag.BoolVar(&listDatasets, "list", false, "list available datasets and exit")
	flag.Parse()

	if listDatasets {
		fmt.Println("Available datasets:")
		for name := range eval.DatasetRegistry {
			fmt.Printf("  %s\n", name)
		}
		return
	}

	// Ensure base dir exists.
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create cache dir: %v\n", err)
		os.Exit(1)
	}

	dsNames := strings.Split(datasetsFlag, ",")
	var datasets []eval.Dataset
	for _, name := range dsNames {
		name = strings.TrimSpace(name)
		factory, ok := eval.DatasetRegistry[name]
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown dataset: %q\nAvailable: ", name)
			for k := range eval.DatasetRegistry {
				fmt.Fprintf(os.Stderr, "%s ", k)
			}
			fmt.Fprintln(os.Stderr)
			os.Exit(1)
		}
		datasets = append(datasets, factory())
	}

	strategies := eval.DefaultStrategies()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg := eval.RunConfig{
		CacheDir: cacheDir,
		TopK:     topK,
		LogFunc: func(msg string) {
			fmt.Println(msg)
		},
	}

	results, err := eval.Run(ctx, datasets, strategies, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "evaluation failed: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput || strings.HasSuffix(outputFlag, ".json") {
		writeJSON(outputFlag, results)
	} else {
		writeHTML(outputFlag, results)
	}
}

func writeJSON(path string, results []eval.DatasetResult) {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "JSON marshal: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", path, err)
		os.Exit(1)
	}
	fmt.Printf("JSON report written to %s\n", path)
}

func writeHTML(path string, results []eval.DatasetResult) {
	report := eval.NewReport(results)
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create %s: %v\n", path, err)
		os.Exit(1)
	}
	defer f.Close()

	if err := report.WriteHTML(f); err != nil {
		fmt.Fprintf(os.Stderr, "render HTML: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("HTML report written to %s (%d results)\n", path, len(results))
}
