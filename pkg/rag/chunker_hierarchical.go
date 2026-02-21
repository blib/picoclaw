package rag

import "strings"

// HierarchicalChunker produces parent and child chunks. Parents are section-
// level chunks (up to ParentMaxSize bytes) that provide context. Children are
// smaller chunks (up to ChildMaxSize bytes) that are indexed for search.
//
// Output is a flat []ChunkLocAndText where parents come first per section,
// followed by their children. Each child's ChunkLoc.ParentIndex points to the
// parent's position in the returned slice.
type HierarchicalChunker struct {
	ParentMaxSize int
	ChildMaxSize  int
}

func (c HierarchicalChunker) Name() string { return "hierarchical" }

func (c HierarchicalChunker) Chunk(content string) []ChunkLocAndText {
	parentMax := c.ParentMaxSize
	if parentMax <= 0 {
		parentMax = 4096
	}
	childMax := c.ChildMaxSize
	if childMax <= 0 {
		childMax = 1024
	}

	units := ParseMarkdownUnits(content)
	if len(units) == 0 {
		return nil
	}

	// group units into sections by top-level headings
	type section struct {
		heading string
		units   []MarkdownUnit
	}
	sections := make([]section, 0, 16)
	currentSection := section{heading: ""}

	for _, u := range units {
		if u.Type == UnitHeading && u.Level <= 2 {
			// flush current section
			if len(currentSection.units) > 0 {
				sections = append(sections, currentSection)
			}
			currentSection = section{heading: u.Text, units: nil}
			continue
		}
		currentSection.units = append(currentSection.units, u)
	}
	if len(currentSection.units) > 0 {
		sections = append(sections, currentSection)
	}

	chunks := make([]ChunkLocAndText, 0, 64)

	for _, sec := range sections {
		if len(sec.units) == 0 {
			continue
		}

		// build parent text (truncate at parentMax)
		parentText := buildSectionText(sec.units, parentMax)
		parentIdx := len(chunks)
		parentStart := sec.units[0].StartByte
		parentEnd := sec.units[len(sec.units)-1].EndByte

		chunks = append(chunks, ChunkLocAndText{
			Loc: ChunkLoc{
				HeadingPath: sec.heading,
				StartByte:   parentStart,
				EndByte:     parentEnd,
			},
			Text: parentText,
		})

		// build children by paragraph-packing within the section
		children := packChildren(sec.units, sec.heading, childMax, parentIdx)
		chunks = append(chunks, children...)
	}

	return chunks
}

// buildSectionText concatenates unit texts up to maxBytes.
func buildSectionText(units []MarkdownUnit, maxBytes int) string {
	var buf strings.Builder
	for _, u := range units {
		if buf.Len()+len(u.Text)+1 > maxBytes && buf.Len() > 0 {
			break
		}
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(u.Text)
	}
	return strings.TrimSpace(buf.String())
}

// packChildren packs section units into child-sized chunks, each with a
// ParentIndex pointing back to the parent chunk.
func packChildren(units []MarkdownUnit, heading string, maxSize int, parentIdx int) []ChunkLocAndText {
	children := make([]ChunkLocAndText, 0, 16)
	var buf strings.Builder
	startByte := 0
	endByte := 0
	started := false

	flush := func() {
		text := strings.TrimSpace(buf.String())
		if text != "" {
			pi := parentIdx
			children = append(children, ChunkLocAndText{
				Loc: ChunkLoc{
					HeadingPath: heading,
					StartByte:   startByte,
					EndByte:     endByte,
					ParentIndex: &pi,
				},
				Text: text,
			})
		}
		buf.Reset()
		started = false
	}

	for _, u := range units {
		if u.Type == UnitHeading {
			continue
		}

		uSize := len(u.Text)
		sep := 0
		if buf.Len() > 0 {
			sep = 1
		}

		// oversized single unit
		if uSize > maxSize {
			flush()
			pi := parentIdx
			children = append(children, ChunkLocAndText{
				Loc: ChunkLoc{
					HeadingPath: heading,
					StartByte:   u.StartByte,
					EndByte:     u.EndByte,
					ParentIndex: &pi,
				},
				Text: strings.TrimSpace(u.Text),
			})
			continue
		}

		if buf.Len()+sep+uSize > maxSize && buf.Len() > 0 {
			flush()
		}

		if !started {
			startByte = u.StartByte
			started = true
		}
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(u.Text)
		endByte = u.EndByte
	}
	flush()
	return children
}
