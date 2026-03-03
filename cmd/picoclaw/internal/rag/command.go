package rag

import (
	"github.com/spf13/cobra"
)

func NewRagCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rag",
		Short: "Manage the local ResearchRAG knowledge base",
		Long: `ResearchRAG commands for building, querying, and inspecting the
local knowledge-base index. Documents are read from the configured
kb_root directory and indexed into index_root.`,
	}

	cmd.AddCommand(
		newIndexCommand(),
		newSearchCommand(),
		newChunkCommand(),
		newInfoCommand(),
		newListCommand(),
	)

	return cmd
}

func newIndexCommand() *cobra.Command {
	var watch bool

	cmd := &cobra.Command{
		Use:   "index",
		Short: "Build or update the local RAG index",
		Example: `  picoclaw rag index
  picoclaw rag index --watch`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ragIndexCmd(watch)
		},
	}

	cmd.Flags().BoolVar(&watch, "watch", false, "Rebuild index every 30s until interrupted")
	return cmd
}

func newSearchCommand() *cobra.Command {
	var (
		query   string
		profile string
		mode    string
		topK    int
		jsonOut bool
	)

	cmd := &cobra.Command{
		Use:   "search",
		Short: "Query indexed knowledge base",
		Example: `  picoclaw rag search --query "where did we discuss caching"
  picoclaw rag search --query "auth flow" --profile decisions_recent --json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ragSearchCmd(query, profile, mode, topK, jsonOut)
		},
	}

	cmd.Flags().StringVarP(&query, "query", "q", "", "Search query (required)")
	cmd.Flags().StringVar(&profile, "profile", "", "Search profile (default_research, decisions_recent, templates_lookup)")
	cmd.Flags().StringVar(&mode, "mode", "", "Search mode override")
	cmd.Flags().IntVar(&topK, "top-k", 0, "Max results to return (0 = use profile default)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	_ = cmd.MarkFlagRequired("query")

	return cmd
}

func newChunkCommand() *cobra.Command {
	var (
		sourcePath   string
		chunkOrdinal int
		jsonOut      bool
	)

	cmd := &cobra.Command{
		Use:   "chunk",
		Short: "Fetch chunk text by source path and ordinal",
		Example: `  picoclaw rag chunk --source-path kb/notes/meeting.md --chunk-ordinal 3`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ragChunkCmd(sourcePath, chunkOrdinal, jsonOut)
		},
	}

	cmd.Flags().StringVar(&sourcePath, "source-path", "", "Document source path (required)")
	cmd.Flags().IntVar(&chunkOrdinal, "chunk-ordinal", 0, "Chunk ordinal number (required, >= 1)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	_ = cmd.MarkFlagRequired("source-path")
	_ = cmd.MarkFlagRequired("chunk-ordinal")

	return cmd
}

func newInfoCommand() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:     "info",
		Short:   "Show index status and configuration",
		Example: `  picoclaw rag info --json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ragInfoCmd(jsonOut)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func newListCommand() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List indexed documents",
		Example: `  picoclaw rag list --json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ragListCmd(jsonOut)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}
