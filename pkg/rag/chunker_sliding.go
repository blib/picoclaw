package rag

import "strings"

// UnitSlidingWindow produces overlapping chunks by sliding a window of
// WindowUnits semantic units with a stride of StrideUnits. If the
// concatenated window exceeds MaxBytes, it is truncated at the last full
// unit that fits.
type UnitSlidingWindow struct {
	WindowUnits int
	StrideUnits int
	MaxBytes    int
}

func (c UnitSlidingWindow) Name() string { return "sliding" }

func (c UnitSlidingWindow) Chunk(content string) []ChunkLocAndText {
	window := c.WindowUnits
	if window <= 0 {
		window = 5
	}
	stride := c.StrideUnits
	if stride <= 0 {
		stride = 2
	}
	if stride > window {
		stride = window
	}
	maxBytes := c.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 4096
	}

	allUnits := ParseMarkdownUnits(content)

	// separate headings from content units, track heading context
	type contentUnit struct {
		unit    MarkdownUnit
		origIdx int
	}
	contentUnits := make([]contentUnit, 0, len(allUnits))
	for i, u := range allUnits {
		if u.Type == UnitHeading {
			continue
		}
		contentUnits = append(contentUnits, contentUnit{unit: u, origIdx: i})
	}

	if len(contentUnits) == 0 {
		return nil
	}

	chunks := make([]ChunkLocAndText, 0, (len(contentUnits)/stride)+1)

	for start := 0; start < len(contentUnits); start += stride {
		end := start + window
		if end > len(contentUnits) {
			end = len(contentUnits)
		}

		// truncate to MaxBytes
		actualEnd := start
		size := 0
		for i := start; i < end; i++ {
			uSize := len(contentUnits[i].unit.Text)
			if i > start {
				uSize++ // newline separator
			}
			if size+uSize > maxBytes && i > start {
				break
			}
			size += uSize
			actualEnd = i + 1
		}
		if actualEnd <= start {
			actualEnd = start + 1
		}

		windowSlice := contentUnits[start:actualEnd]

		var buf strings.Builder
		for i, cu := range windowSlice {
			if i > 0 {
				buf.WriteByte('\n')
			}
			buf.WriteString(cu.unit.Text)
		}
		text := strings.TrimSpace(buf.String())
		if text == "" {
			continue
		}

		heading := HeadingPathAt(allUnits, windowSlice[0].origIdx)

		chunks = append(chunks, ChunkLocAndText{
			Loc: ChunkLoc{
				HeadingPath: heading,
				StartByte:   windowSlice[0].unit.StartByte,
				EndByte:     windowSlice[len(windowSlice)-1].unit.EndByte,
			},
			Text: text,
		})

		if end >= len(contentUnits) {
			break
		}
	}
	return chunks
}
