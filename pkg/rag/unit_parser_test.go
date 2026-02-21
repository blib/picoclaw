package rag

import (
	"strings"
	"testing"
)

func TestParseMarkdownUnits_Headings(t *testing.T) {
	content := "# Title\n\nSome text\n\n## Sub\n\nMore text"
	units := ParseMarkdownUnits(content)

	wantTypes := []UnitType{UnitHeading, UnitParagraph, UnitHeading, UnitParagraph}
	if len(units) != len(wantTypes) {
		t.Fatalf("got %d units, want %d", len(units), len(wantTypes))
	}
	for i, wt := range wantTypes {
		if units[i].Type != wt {
			t.Errorf("unit[%d].Type = %d, want %d", i, units[i].Type, wt)
		}
	}
	if units[0].Level != 1 {
		t.Errorf("heading level = %d, want 1", units[0].Level)
	}
	if units[0].Text != "Title" {
		t.Errorf("heading text = %q, want %q", units[0].Text, "Title")
	}
	if units[2].Level != 2 {
		t.Errorf("heading level = %d, want 2", units[2].Level)
	}
}

func TestParseMarkdownUnits_CodeBlock(t *testing.T) {
	content := "Before\n\n```go\nfunc main() {}\n```\n\nAfter"
	units := ParseMarkdownUnits(content)

	wantTypes := []UnitType{UnitParagraph, UnitCodeBlock, UnitParagraph}
	if len(units) != len(wantTypes) {
		t.Fatalf("got %d units, want %d: %+v", len(units), len(wantTypes), units)
	}
	for i, wt := range wantTypes {
		if units[i].Type != wt {
			t.Errorf("unit[%d].Type = %d, want %d", i, units[i].Type, wt)
		}
	}
	if !strings.Contains(units[1].Text, "func main()") {
		t.Errorf("code block should contain func main(), got %q", units[1].Text)
	}
}

func TestParseMarkdownUnits_TildeCodeBlock(t *testing.T) {
	content := "~~~\ncode\n~~~"
	units := ParseMarkdownUnits(content)

	if len(units) != 1 || units[0].Type != UnitCodeBlock {
		t.Fatalf("expected 1 code block, got %d units: %+v", len(units), units)
	}
}

func TestParseMarkdownUnits_UnclosedFence(t *testing.T) {
	content := "```\nnever closed"
	units := ParseMarkdownUnits(content)
	if len(units) != 1 || units[0].Type != UnitCodeBlock {
		t.Fatalf("expected 1 code block for unclosed fence, got %+v", units)
	}
}

func TestParseMarkdownUnits_Table(t *testing.T) {
	content := "Text\n\n| A | B |\n|---|---|\n| 1 | 2 |\n\nMore"
	units := ParseMarkdownUnits(content)

	wantTypes := []UnitType{UnitParagraph, UnitTable, UnitParagraph}
	if len(units) != len(wantTypes) {
		t.Fatalf("got %d units, want %d: %+v", len(units), len(wantTypes), units)
	}
	for i, wt := range wantTypes {
		if units[i].Type != wt {
			t.Errorf("unit[%d].Type = %d, want %d", i, units[i].Type, wt)
		}
	}
	if !strings.Contains(units[1].Text, "| 1 | 2 |") {
		t.Errorf("table should contain data row, got %q", units[1].Text)
	}
}

func TestParseMarkdownUnits_ListItems(t *testing.T) {
	content := "- item one\n- item two\n* star\n1. ordered"
	units := ParseMarkdownUnits(content)

	for _, u := range units {
		if u.Type != UnitListItem {
			t.Errorf("expected UnitListItem, got type %d for %q", u.Type, u.Text)
		}
	}
	if len(units) != 4 {
		t.Fatalf("got %d units, want 4: %+v", len(units), units)
	}
}

func TestParseMarkdownUnits_ListContinuation(t *testing.T) {
	content := "- item one\n  continued\n- item two"
	units := ParseMarkdownUnits(content)

	if len(units) != 2 {
		t.Fatalf("got %d units, want 2: %+v", len(units), units)
	}
	if !strings.Contains(units[0].Text, "continued") {
		t.Errorf("first list item should include continuation, got %q", units[0].Text)
	}
}

func TestParseMarkdownUnits_Mixed(t *testing.T) {
	content := `# Intro

Welcome.

## Config

| Key | Value |
|-----|-------|
| a   | 1     |

- step one
- step two

` + "```bash\necho hi\n```" + `

Done.`

	units := ParseMarkdownUnits(content)

	wantTypes := []UnitType{
		UnitHeading,    // # Intro
		UnitParagraph,  // Welcome.
		UnitHeading,    // ## Config
		UnitTable,      // table
		UnitListItem,   // - step one
		UnitListItem,   // - step two
		UnitCodeBlock,  // ```bash...```
		UnitParagraph,  // Done.
	}

	if len(units) != len(wantTypes) {
		for i, u := range units {
			t.Logf("  unit[%d] type=%d text=%q", i, u.Type, u.Text)
		}
		t.Fatalf("got %d units, want %d", len(units), len(wantTypes))
	}
	for i, wt := range wantTypes {
		if units[i].Type != wt {
			t.Errorf("unit[%d].Type = %d, want %d (text=%q)", i, units[i].Type, wt, units[i].Text)
		}
	}
}

func TestParseMarkdownUnits_ByteOffsets(t *testing.T) {
	content := "Hello\n\nWorld"
	units := ParseMarkdownUnits(content)

	if len(units) != 2 {
		t.Fatalf("got %d units, want 2", len(units))
	}
	// "Hello" at bytes [0, 6) — "Hello\n"
	if units[0].StartByte != 0 {
		t.Errorf("unit[0].StartByte = %d, want 0", units[0].StartByte)
	}
	// "World" starts after "Hello\n\n" = 7
	if units[1].StartByte < 7 {
		t.Errorf("unit[1].StartByte = %d, want >= 7", units[1].StartByte)
	}
}

func TestHeadingPathAt(t *testing.T) {
	content := "# A\n\n## B\n\ntext\n\n## C\n\nmore"
	units := ParseMarkdownUnits(content)

	// find "text" paragraph
	textIdx := -1
	moreIdx := -1
	for i, u := range units {
		if u.Text == "text" {
			textIdx = i
		}
		if u.Text == "more" {
			moreIdx = i
		}
	}
	if textIdx < 0 || moreIdx < 0 {
		t.Fatal("could not find expected units")
	}

	path := HeadingPathAt(units, textIdx)
	if path != "A > B" {
		t.Errorf("HeadingPathAt(text) = %q, want %q", path, "A > B")
	}

	path = HeadingPathAt(units, moreIdx)
	if path != "A > C" {
		t.Errorf("HeadingPathAt(more) = %q, want %q", path, "A > C")
	}
}

func TestUnitBytesAndJoin(t *testing.T) {
	units := []MarkdownUnit{
		{Text: "abc"},
		{Text: "de"},
	}
	if got := UnitBytes(units); got != 6 { // 3 + 2 + 1 separator
		t.Errorf("UnitBytes = %d, want 6", got)
	}
	if got := JoinUnits(units); got != "abc\nde" {
		t.Errorf("JoinUnits = %q, want %q", got, "abc\nde")
	}
}

func TestParseMarkdownUnits_EmptyContent(t *testing.T) {
	units := ParseMarkdownUnits("")
	if len(units) != 0 {
		t.Errorf("got %d units for empty content, want 0", len(units))
	}
}

func TestParseMarkdownUnits_BlankLinesOnly(t *testing.T) {
	units := ParseMarkdownUnits("\n\n\n")
	if len(units) != 0 {
		t.Errorf("got %d units for blank-lines-only, want 0", len(units))
	}
}
