package rag

import (
	"regexp"
	"strings"
)

// Chunker splits document content into chunks for indexing.
// Implementations must be deterministic for reproducible evaluations.
type Chunker interface {
	// Name returns a short identifier (e.g. "markdown", "fixed-size").
	Name() string
	// Chunk splits content into located text chunks.
	Chunk(content string) []ChunkLocAndText
}

// MarkdownChunker splits markdown content by sections, keeping code blocks
// and tables atomic. Uses the unit parser to identify semantic boundaries,
// then packs units into chunks respecting soft/hard byte limits with
// sentence-boundary breaking for oversized paragraphs.
type MarkdownChunker struct {
	SoftLimit int
	HardLimit int
}

func (c MarkdownChunker) Name() string { return "markdown" }
func (c MarkdownChunker) Chunk(content string) []ChunkLocAndText {
	return splitMarkdownChunksV2(content, c.SoftLimit, c.HardLimit)
}

// splitMarkdownChunksV2 uses the unit parser for atomic boundary detection,
// then groups units into chunks by heading sections. Falls back to sentence-
// boundary breaking for oversized units.
func splitMarkdownChunksV2(content string, softLimit, hardLimit int) []ChunkLocAndText {
	if softLimit <= 0 {
		softLimit = 4096
	}
	if hardLimit <= 0 {
		hardLimit = 8192
	}

	units := ParseMarkdownUnits(content)
	if len(units) == 0 {
		return nil
	}

	chunks := make([]ChunkLocAndText, 0, 32)
	var buf strings.Builder
	headingPath := ""
	startByte := 0
	endByte := 0

	flush := func() {
		text := strings.TrimSpace(buf.String())
		if text == "" {
			buf.Reset()
			return
		}
		// break oversized chunks at sentence boundaries
		for len(text) > 0 {
			runes := []rune(text)
			limit := len(runes)
			if limit > hardLimit {
				limit = hardLimit
			} else if limit > softLimit {
				// try sentence boundary
				best := -1
				for i := softLimit - 1; i >= softLimit/2; i-- {
					if i < len(runes) && (runes[i] == '.' || runes[i] == '!' || runes[i] == '?') {
						best = i + 1
						break
					}
				}
				if best > 0 {
					limit = best
				} else {
					limit = softLimit
				}
			}
			part := strings.TrimSpace(string(runes[:limit]))
			if part != "" {
				chunks = append(chunks, ChunkLocAndText{
					Loc:  ChunkLoc{HeadingPath: headingPath, StartByte: startByte, EndByte: endByte},
					Text: part,
				})
			}
			if limit >= len(runes) {
				break
			}
			text = strings.TrimSpace(string(runes[limit:]))
		}
		buf.Reset()
	}

	for _, u := range units {
		switch u.Type {
		case UnitHeading:
			flush()
			headingPath = u.Text
			startByte = u.EndByte
			endByte = u.EndByte

		case UnitCodeBlock, UnitTable:
			// atomic: flush current, emit as standalone if it fits, else
			// append to current buffer
			if buf.Len()+len(u.Text) > softLimit && buf.Len() > 0 {
				flush()
				startByte = u.StartByte
			}
			if buf.Len() > 0 {
				buf.WriteByte('\n')
			}
			buf.WriteString(u.Text)
			endByte = u.EndByte
			// if this single unit exceeds soft limit, flush immediately
			if buf.Len() >= softLimit {
				flush()
				startByte = u.EndByte
			}

		default: // paragraph, list item
			if buf.Len()+len(u.Text)+1 > softLimit && buf.Len() > 0 {
				flush()
				startByte = u.StartByte
			}
			if buf.Len() > 0 {
				buf.WriteByte('\n')
			}
			buf.WriteString(u.Text)
			endByte = u.EndByte
		}
	}
	flush()
	return chunks
}

// FixedSizeChunker splits content into fixed-size byte chunks with optional
// overlap, breaking at whitespace boundaries when possible.
type FixedSizeChunker struct {
	Size    int
	Overlap int
}

func (c FixedSizeChunker) Name() string { return "fixed" }
func (c FixedSizeChunker) Chunk(content string) []ChunkLocAndText {
	size := c.Size
	if size <= 0 {
		size = 1024
	}
	overlap := c.Overlap
	if overlap < 0 {
		overlap = 0
	}
	if overlap >= size {
		overlap = size / 4
	}

	data := []byte(content)
	chunks := make([]ChunkLocAndText, 0, len(data)/size+1)
	pos := 0
	for pos < len(data) {
		end := pos + size
		if end > len(data) {
			end = len(data)
		}
		// Try to break at whitespace.
		if end < len(data) {
			best := -1
			start := end - size/4
			if start < pos {
				start = pos
			}
			for i := end - 1; i >= start; i-- {
				if data[i] == ' ' || data[i] == '\n' || data[i] == '\t' {
					best = i + 1
					break
				}
			}
			if best > pos {
				end = best
			}
		}
		text := strings.TrimSpace(string(data[pos:end]))
		if text != "" {
			chunks = append(chunks, ChunkLocAndText{
				Loc:  ChunkLoc{StartByte: pos, EndByte: end},
				Text: text,
			})
		}
		advance := end - pos - overlap
		if advance <= 0 {
			advance = 1
		}
		pos = pos + advance
	}
	return chunks
}

var headingRE = regexp.MustCompile(`^(#{1,6})\s+(.+)$`)

func splitMarkdownChunks(content string, softLimit, hardLimit int) []ChunkLocAndText {
	if softLimit <= 0 {
		softLimit = 4096
	}
	if hardLimit <= 0 {
		hardLimit = 8192
	}

	lines := strings.Split(content, "\n")
	chunks := make([]ChunkLocAndText, 0, 32)
	headingPath := ""
	cursor := 0
	start := 0
	buf := strings.Builder{}

	flush := func(end int) {
		text := strings.TrimSpace(buf.String())
		if text == "" {
			buf.Reset()
			start = end
			return
		}
		for len(text) > 0 {
			runes := []rune(text)
			runeSoft := softLimit
			if runeSoft > len(runes) {
				runeSoft = len(runes)
			}
			runeHard := hardLimit
			if runeHard > len(runes) {
				runeHard = len(runes)
			}

			limit := runeSoft
			if len(runes) > runeHard {
				limit = runeHard
			} else if len(runes) <= runeSoft {
				limit = len(runes)
			}

			// Try to break at sentence boundary (. ! ?) within soft limit.
			if limit < len(runes) {
				bestBreak := -1
				for i := limit - 1; i >= limit/2; i-- {
					if runes[i] == '.' || runes[i] == '!' || runes[i] == '?' {
						bestBreak = i + 1
						break
					}
				}
				if bestBreak > 0 {
					limit = bestBreak
				}
			}

			part := strings.TrimSpace(string(runes[:limit]))
			chunks = append(chunks, ChunkLocAndText{
				Loc:  ChunkLoc{HeadingPath: headingPath, StartByte: start, EndByte: end},
				Text: part,
			})
			if limit >= len(runes) {
				text = ""
			} else {
				text = strings.TrimSpace(string(runes[limit:]))
			}
		}
		buf.Reset()
		start = end
	}

	for _, line := range lines {
		lineLen := len(line) + 1 // + newline
		if m := headingRE.FindStringSubmatch(strings.TrimSpace(line)); len(m) == 3 {
			flush(cursor)
			headingPath = m[2]
			cursor += lineLen
			continue
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flush(cursor)
			cursor += lineLen
			continue
		}

		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(line)
		cursor += lineLen
	}

	flush(cursor)
	return chunks
}

type ChunkLocAndText struct {
	Loc  ChunkLoc
	Text string
}

// hspaceRE matches horizontal whitespace (spaces and tabs) but preserves
// newlines so code blocks and structured content retain visual structure.
var hspaceRE = regexp.MustCompile(`[^\S\n]+`)

func normalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSpace(s)
	s = hspaceRE.ReplaceAllString(s, " ")
	// collapse runs of blank lines into a single newline
	s = regexp.MustCompile(`\n{3,}`).ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
