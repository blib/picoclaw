package rag

import "strings"

// ParagraphPacker builds chunks by packing consecutive semantic units (from the
// unit parser) until adding the next unit would exceed MaxSize. Code blocks and
// tables are kept whole — a single oversized unit becomes its own chunk.
type ParagraphPacker struct {
	MaxSize int
}

func (c ParagraphPacker) Name() string { return "paragraph" }

func (c ParagraphPacker) Chunk(content string) []ChunkLocAndText {
	maxSize := c.MaxSize
	if maxSize <= 0 {
		maxSize = 4096
	}

	units := ParseMarkdownUnits(content)
	if len(units) == 0 {
		return nil
	}

	chunks := make([]ChunkLocAndText, 0, 32)
	var buf strings.Builder
	startByte := 0
	endByte := 0
	headingPath := ""
	chunkHeading := ""

	flush := func() {
		text := strings.TrimSpace(buf.String())
		if text != "" {
			chunks = append(chunks, ChunkLocAndText{
				Loc:  ChunkLoc{HeadingPath: chunkHeading, StartByte: startByte, EndByte: endByte},
				Text: text,
			})
		}
		buf.Reset()
		chunkHeading = headingPath
	}

	for _, u := range units {
		if u.Type == UnitHeading {
			headingPath = u.Text
			continue
		}

		unitSize := len(u.Text)

		// oversized single unit → flush current, emit unit as its own chunk
		if unitSize > maxSize {
			flush()
			startByte = u.StartByte
			chunks = append(chunks, ChunkLocAndText{
				Loc:  ChunkLoc{HeadingPath: headingPath, StartByte: u.StartByte, EndByte: u.EndByte},
				Text: strings.TrimSpace(u.Text),
			})
			startByte = u.EndByte
			endByte = u.EndByte
			continue
		}

		// would adding this unit exceed max?
		sep := 0
		if buf.Len() > 0 {
			sep = 1
		}
		if buf.Len()+sep+unitSize > maxSize && buf.Len() > 0 {
			flush()
			startByte = u.StartByte
		}

		if buf.Len() == 0 {
			chunkHeading = headingPath
			startByte = u.StartByte
		}
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(u.Text)
		endByte = u.EndByte
	}
	flush()
	return chunks
}
