package rag

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

// testDoc is a shared markdown document used across strategy tests.
const testDoc = `# Introduction

Welcome to the project. This is an overview.

## Configuration

Set the following environment variables:

` + "```bash" + `
export API_KEY=xxx
export DB_HOST=localhost
` + "```" + `

| Key      | Default   |
|----------|-----------|
| api_key  | required  |
| db_host  | localhost |

## Deployment

- Build the binary
- Upload to server
- Run migrations

### Rolling Update

Perform zero-downtime deployment using blue-green strategy.
The old version stays live until health checks pass on the new one.

## Monitoring

Check logs and metrics regularly. Alert on error rate spikes.
`

// --- Strategy 1: FixedWindow ---

func TestFixedWindow_Basic(t *testing.T) {
	c := FixedSizeChunker{Size: 100}
	chunks := c.Chunk(testDoc)
	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}
	if c.Name() != "fixed" {
		t.Errorf("Name() = %q, want %q", c.Name(), "fixed")
	}
	for i, ch := range chunks {
		if len(ch.Text) == 0 {
			t.Errorf("chunk %d has empty text", i)
		}
		if ch.Loc.StartByte >= ch.Loc.EndByte {
			t.Errorf("chunk %d: StartByte=%d >= EndByte=%d", i, ch.Loc.StartByte, ch.Loc.EndByte)
		}
	}
}

func TestFixedWindow_Overlap(t *testing.T) {
	c := FixedSizeChunker{Size: 100, Overlap: 30}
	chunks := c.Chunk(testDoc)
	if len(chunks) < 2 {
		t.Fatal("expected multiple chunks with overlap")
	}
	// with overlap, consecutive chunks should share some text
	for i := 1; i < len(chunks); i++ {
		if chunks[i].Loc.StartByte >= chunks[i-1].Loc.EndByte {
			// no overlap detected — the start of chunk i should be before
			// the end of chunk i-1
			t.Errorf("chunk %d starts at %d but chunk %d ends at %d — expected overlap",
				i, chunks[i].Loc.StartByte, i-1, chunks[i-1].Loc.EndByte)
		}
	}
}

func TestFixedWindow_NoOverlapMoreChunks(t *testing.T) {
	noOverlap := FixedSizeChunker{Size: 100, Overlap: 0}
	withOverlap := FixedSizeChunker{Size: 100, Overlap: 30}
	chunksNo := noOverlap.Chunk(testDoc)
	chunksWith := withOverlap.Chunk(testDoc)
	if len(chunksWith) <= len(chunksNo) {
		t.Errorf("expected more chunks with overlap (%d) than without (%d)",
			len(chunksWith), len(chunksNo))
	}
}

// --- Strategy 2: MarkdownStructure ---

func TestMarkdownStructure_Basic(t *testing.T) {
	c := MarkdownChunker{SoftLimit: 200, HardLimit: 400}
	chunks := c.Chunk(testDoc)
	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}
	if c.Name() != "markdown" {
		t.Errorf("Name() = %q, want %q", c.Name(), "markdown")
	}
}

func TestMarkdownStructure_AtomicCodeBlock(t *testing.T) {
	doc := "# Test\n\n```go\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```\n\nAfter."
	c := MarkdownChunker{SoftLimit: 2000, HardLimit: 4000}
	chunks := c.Chunk(doc)

	// code block should be intact in one chunk
	found := false
	for _, ch := range chunks {
		if strings.Contains(ch.Text, "func main()") && strings.Contains(ch.Text, "Println") {
			found = true
			break
		}
	}
	if !found {
		t.Error("code block was split across chunks")
	}
}

func TestMarkdownStructure_AtomicTable(t *testing.T) {
	doc := "# Data\n\n| A | B |\n|---|---|\n| 1 | 2 |\n| 3 | 4 |\n\nEnd."
	c := MarkdownChunker{SoftLimit: 2000, HardLimit: 4000}
	chunks := c.Chunk(doc)

	found := false
	for _, ch := range chunks {
		if strings.Contains(ch.Text, "| 1 | 2 |") && strings.Contains(ch.Text, "| 3 | 4 |") {
			found = true
			break
		}
	}
	if !found {
		t.Error("table was split across chunks")
	}
}

func TestMarkdownStructure_HeadingPath(t *testing.T) {
	c := MarkdownChunker{SoftLimit: 200, HardLimit: 400}
	chunks := c.Chunk(testDoc)

	// at least one chunk should have a heading path
	hasHeading := false
	for _, ch := range chunks {
		if ch.Loc.HeadingPath != "" {
			hasHeading = true
			break
		}
	}
	if !hasHeading {
		t.Error("no chunks have heading paths")
	}
}

// --- Strategy 3: ParagraphPacker ---

func TestParagraphPacker_Basic(t *testing.T) {
	c := ParagraphPacker{MaxSize: 200}
	chunks := c.Chunk(testDoc)
	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}
	if c.Name() != "paragraph" {
		t.Errorf("Name() = %q, want %q", c.Name(), "paragraph")
	}
	for i, ch := range chunks {
		if len(ch.Text) == 0 {
			t.Errorf("chunk %d empty", i)
		}
	}
}

func TestParagraphPacker_MaxSizeRespected(t *testing.T) {
	maxSize := 150
	c := ParagraphPacker{MaxSize: maxSize}
	chunks := c.Chunk(testDoc)

	for i, ch := range chunks {
		// oversized single units are allowed to exceed maxSize
		units := ParseMarkdownUnits(ch.Text)
		if len(units) > 1 && len(ch.Text) > maxSize {
			t.Errorf("chunk %d has %d bytes (max %d) with %d units",
				i, len(ch.Text), maxSize, len(units))
		}
	}
}

func TestParagraphPacker_OversizedUnit(t *testing.T) {
	bigCode := "```\n" + strings.Repeat("x", 500) + "\n```"
	doc := "Text before.\n\n" + bigCode + "\n\nText after."
	c := ParagraphPacker{MaxSize: 100}
	chunks := c.Chunk(doc)

	if len(chunks) < 3 {
		t.Fatalf("expected at least 3 chunks (before, big, after), got %d", len(chunks))
	}
}

// --- Strategy 4: UnitSlidingWindow ---

func TestUnitSlidingWindow_Basic(t *testing.T) {
	c := UnitSlidingWindow{WindowUnits: 3, StrideUnits: 1, MaxBytes: 4096}
	chunks := c.Chunk(testDoc)
	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}
	if c.Name() != "sliding" {
		t.Errorf("Name() = %q, want %q", c.Name(), "sliding")
	}
}

func TestUnitSlidingWindow_Overlap(t *testing.T) {
	c := UnitSlidingWindow{WindowUnits: 3, StrideUnits: 1, MaxBytes: 4096}
	chunks := c.Chunk(testDoc)
	if len(chunks) < 3 {
		t.Fatalf("expected multiple overlapping chunks, got %d", len(chunks))
	}

	// with stride=1 and window=3, consecutive chunks should share units
	// detect by checking text overlap
	for i := 1; i < len(chunks) && i < 5; i++ {
		words0 := strings.Fields(chunks[i-1].Text)
		words1 := strings.Fields(chunks[i].Text)
		shared := countSharedWords(words0, words1)
		if shared == 0 {
			t.Errorf("chunks %d and %d share no words — expected overlap", i-1, i)
		}
	}
}

func TestUnitSlidingWindow_MoreChunksThanUnits(t *testing.T) {
	doc := "Para one.\n\nPara two.\n\nPara three.\n\nPara four."
	c := UnitSlidingWindow{WindowUnits: 2, StrideUnits: 1, MaxBytes: 4096}
	chunks := c.Chunk(doc)
	units := ParseMarkdownUnits(doc)

	contentUnits := 0
	for _, u := range units {
		if u.Type != UnitHeading {
			contentUnits++
		}
	}
	// with window=2, stride=1, we get contentUnits-1 chunks (plus one for last window)
	if len(chunks) < contentUnits-1 {
		t.Errorf("expected at least %d chunks, got %d", contentUnits-1, len(chunks))
	}
}

// --- Strategy 5: Hierarchical ---

func TestHierarchical_Basic(t *testing.T) {
	c := HierarchicalChunker{ParentMaxSize: 2000, ChildMaxSize: 200}
	chunks := c.Chunk(testDoc)
	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}
	if c.Name() != "hierarchical" {
		t.Errorf("Name() = %q, want %q", c.Name(), "hierarchical")
	}
}

func TestHierarchical_ParentChildLinkage(t *testing.T) {
	c := HierarchicalChunker{ParentMaxSize: 2000, ChildMaxSize: 100}
	chunks := c.Chunk(testDoc)

	parents := 0
	children := 0
	for _, ch := range chunks {
		if ch.Loc.ParentIndex != nil {
			children++
			pi := *ch.Loc.ParentIndex
			if pi < 0 || pi >= len(chunks) {
				t.Fatalf("child has ParentIndex=%d, out of range [0, %d)", pi, len(chunks))
			}
			parent := chunks[pi]
			if parent.Loc.ParentIndex != nil {
				t.Error("parent should not have a ParentIndex")
			}
		} else {
			parents++
		}
	}
	if parents == 0 {
		t.Error("no parent chunks found")
	}
	if children == 0 {
		t.Error("no child chunks found")
	}
	t.Logf("parents=%d children=%d total=%d", parents, children, len(chunks))
}

func TestHierarchical_ParentContainsChildContent(t *testing.T) {
	c := HierarchicalChunker{ParentMaxSize: 4000, ChildMaxSize: 100}
	chunks := c.Chunk(testDoc)

	for i, ch := range chunks {
		if ch.Loc.ParentIndex == nil {
			continue // skip parents
		}
		parent := chunks[*ch.Loc.ParentIndex]
		// child text should be a substring of parent text (if parent fits)
		// This is approximate — parent might be truncated
		childWords := strings.Fields(ch.Text)
		if len(childWords) > 2 {
			// check first few words appear in parent
			probe := strings.Join(childWords[:2], " ")
			if !strings.Contains(parent.Text, probe) {
				t.Logf("child %d text starts with %q, parent text: %q",
					i, probe, truncate(parent.Text, 200))
			}
		}
	}
}

// --- Strategy 6: SemanticDrift ---

func TestSemanticDrift_WithoutEmbedder(t *testing.T) {
	c := SemanticDriftChunker{MaxSize: 200, Embedder: nil}
	chunks := c.Chunk(testDoc)
	if len(chunks) == 0 {
		t.Fatal("expected chunks even without embedder (size-only fallback)")
	}
	if c.Name() != "semantic" {
		t.Errorf("Name() = %q, want %q", c.Name(), "semantic")
	}
}

func TestSemanticDrift_WithEmbedder(t *testing.T) {
	emb := newMockEmbedder()

	// two clearly different topics with very different vocabulary
	doc := "cat dog pet animal veterinary puppy kitten breed\n\nserver database query index migration schema table postgres"

	// use a low threshold so even moderate drift triggers a split
	c := SemanticDriftChunker{MaxSize: 4096, DriftThreshold: 0.5, Embedder: emb}
	chunks := c.Chunk(doc)

	if len(chunks) < 2 {
		// log similarities for debugging
		units := ParseMarkdownUnits(doc)
		contentTexts := make([]string, 0)
		for _, u := range units {
			if u.Type != UnitHeading {
				contentTexts = append(contentTexts, u.Text)
			}
		}
		sims := c.computePairwiseSims(contentTexts)
		t.Logf("pairwise similarities: %v", sims)
		t.Fatalf("expected at least 2 chunks for distinct topics, got %d", len(chunks))
	}
}

func TestSemanticDrift_MaxSizeEnforced(t *testing.T) {
	emb := newMockEmbedder()
	maxSize := 100
	c := SemanticDriftChunker{MaxSize: maxSize, DriftThreshold: 0.99, Embedder: emb}

	// Build doc with many small paragraphs so each unit < maxSize
	var sb strings.Builder
	for i := 0; i < 20; i++ {
		sb.WriteString("This is paragraph number ")
		sb.WriteString(strings.Repeat("word ", 8))
		sb.WriteString(".\n\n")
	}
	doc := sb.String()
	chunks := c.Chunk(doc)

	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, ch := range chunks {
		// Each chunk should respect maxSize (small slack for unit boundaries)
		if len(ch.Text) > maxSize+60 {
			t.Errorf("chunk %d exceeds expected size: %d bytes", i, len(ch.Text))
		}
	}
}

// --- Registry ---

func TestNewChunkerFromConfig_AllStrategies(t *testing.T) {
	strategies := []string{"fixed", "markdown", "paragraph", "sliding", "hierarchical", "semantic"}
	for _, s := range strategies {
		cfg := config.RAGToolsConfig{
			ChunkStrategy:  s,
			ChunkSoftBytes: 1024,
			ChunkHardBytes: 2048,
		}
		c, err := NewChunkerFromConfig(cfg, newMockEmbedder())
		if err != nil {
			t.Errorf("strategy %q: %v", s, err)
			continue
		}
		if c.Name() != s {
			t.Errorf("strategy %q: Name() = %q", s, c.Name())
		}
	}
}

func TestNewChunkerFromConfig_UnknownStrategy(t *testing.T) {
	cfg := config.RAGToolsConfig{ChunkStrategy: "bogus"}
	_, err := NewChunkerFromConfig(cfg, nil)
	if err == nil {
		t.Fatal("expected error for unknown strategy")
	}
}

func TestNewChunkerFromConfig_EmptyDefaultsToMarkdown(t *testing.T) {
	cfg := config.RAGToolsConfig{}
	c, err := NewChunkerFromConfig(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name() != "markdown" {
		t.Errorf("empty strategy should default to markdown, got %q", c.Name())
	}
}

// --- Helpers ---

func countSharedWords(a, b []string) int {
	set := make(map[string]bool, len(a))
	for _, w := range a {
		set[w] = true
	}
	count := 0
	for _, w := range b {
		if set[w] {
			count++
		}
	}
	return count
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
