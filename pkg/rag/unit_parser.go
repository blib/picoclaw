package rag

import (
	"strings"
)

// UnitType classifies a semantic unit extracted from markdown content.
type UnitType int

const (
	UnitParagraph UnitType = iota
	UnitHeading
	UnitCodeBlock
	UnitTable
	UnitListItem
)

// MarkdownUnit is an atomic semantic unit extracted from markdown content.
// Units preserve their original text and byte offsets within the source.
type MarkdownUnit struct {
	Type      UnitType
	Level     int // heading level (1-6); 0 for non-headings
	Text      string
	StartByte int
	EndByte   int
}

// ParseMarkdownUnits splits markdown content into typed semantic units in a
// single pass. The returned units cover the entire input without gaps or
// overlaps. Blank lines between units are absorbed — they are not emitted as
// separate units.
func ParseMarkdownUnits(content string) []MarkdownUnit {
	lines := strings.Split(content, "\n")
	units := make([]MarkdownUnit, 0, 64)

	cursor := 0 // byte offset into content
	var buf strings.Builder
	bufStart := 0
	bufType := UnitParagraph
	inFence := false
	fenceMarker := ""

	flushBuf := func() {
		text := strings.TrimSpace(buf.String())
		if text != "" {
			units = append(units, MarkdownUnit{
				Type:      bufType,
				Text:      text,
				StartByte: bufStart,
				EndByte:   cursor,
			})
		}
		buf.Reset()
		bufType = UnitParagraph
		bufStart = cursor
	}

	for i, line := range lines {
		lineLen := len(line)
		if i < len(lines)-1 {
			lineLen++ // account for newline separator
		}
		trimmed := strings.TrimSpace(line)

		// --- fenced code block handling ---
		if inFence {
			if buf.Len() > 0 {
				buf.WriteByte('\n')
			}
			buf.WriteString(line)
			if isFenceClose(trimmed, fenceMarker) {
				inFence = false
				cursor += lineLen
				flushBuf()
				continue
			}
			cursor += lineLen
			continue
		}

		if marker, ok := isFenceOpen(trimmed); ok {
			flushBuf()
			bufType = UnitCodeBlock
			bufStart = cursor
			buf.WriteString(line)
			inFence = true
			fenceMarker = marker
			cursor += lineLen
			continue
		}

		// --- heading ---
		if m := headingRE.FindStringSubmatch(trimmed); len(m) == 3 {
			flushBuf()
			level := len(m[1])
			units = append(units, MarkdownUnit{
				Type:      UnitHeading,
				Level:     level,
				Text:      m[2],
				StartByte: cursor,
				EndByte:   cursor + lineLen,
			})
			cursor += lineLen
			bufStart = cursor
			continue
		}

		// --- table row ---
		if isTableLine(trimmed) {
			if bufType != UnitTable {
				flushBuf()
				bufType = UnitTable
				bufStart = cursor
			}
			if buf.Len() > 0 {
				buf.WriteByte('\n')
			}
			buf.WriteString(line)
			cursor += lineLen
			continue
		}
		if bufType == UnitTable {
			flushBuf()
		}

		// --- list item ---
		if isListItem(trimmed) {
			// Each list item is its own unit for fine-grained chunking.
			// Continuation lines (indented non-item) are appended to the
			// current list item.
			flushBuf()
			bufType = UnitListItem
			bufStart = cursor
			buf.WriteString(line)
			cursor += lineLen
			continue
		}
		// continuation of list item (indented, non-blank, non-heading)
		if bufType == UnitListItem && len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			if buf.Len() > 0 {
				buf.WriteByte('\n')
			}
			buf.WriteString(line)
			cursor += lineLen
			continue
		}
		if bufType == UnitListItem {
			flushBuf()
		}

		// --- blank line ---
		if trimmed == "" {
			flushBuf()
			cursor += lineLen
			bufStart = cursor
			continue
		}

		// --- paragraph text ---
		if bufType != UnitParagraph {
			flushBuf()
			bufStart = cursor
		}
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(line)
		cursor += lineLen
	}

	// handle unclosed fence gracefully
	if inFence {
		flushBuf()
	} else {
		flushBuf()
	}

	return units
}

// HeadingPathAt returns the accumulated heading path for the given unit index
// by scanning backwards through the units list. The path concatenates the most
// recent heading at each level (e.g. "Section > Subsection").
func HeadingPathAt(units []MarkdownUnit, idx int) string {
	headings := make([]string, 7) // levels 1-6
	for i := idx; i >= 0; i-- {
		u := units[i]
		if u.Type != UnitHeading || u.Level < 1 || u.Level > 6 {
			continue
		}
		if headings[u.Level] == "" {
			headings[u.Level] = u.Text
		}
	}
	parts := make([]string, 0, 6)
	for _, h := range headings[1:] {
		if h != "" {
			parts = append(parts, h)
		}
	}
	return strings.Join(parts, " > ")
}

// UnitBytes returns the total byte count of the given units' text.
func UnitBytes(units []MarkdownUnit) int {
	n := 0
	for _, u := range units {
		n += len(u.Text)
	}
	// separators between units
	if len(units) > 1 {
		n += len(units) - 1 // one \n between each
	}
	return n
}

// JoinUnits concatenates unit texts separated by newlines.
func JoinUnits(units []MarkdownUnit) string {
	if len(units) == 0 {
		return ""
	}
	var b strings.Builder
	for i, u := range units {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(u.Text)
	}
	return b.String()
}

// --- helpers ---

func isFenceOpen(trimmed string) (marker string, ok bool) {
	if strings.HasPrefix(trimmed, "```") {
		return "```", true
	}
	if strings.HasPrefix(trimmed, "~~~") {
		return "~~~", true
	}
	return "", false
}

func isFenceClose(trimmed, marker string) bool {
	return strings.HasPrefix(trimmed, marker) && strings.TrimSpace(strings.TrimPrefix(trimmed, marker)) == ""
}

func isTableLine(trimmed string) bool {
	return len(trimmed) > 0 && trimmed[0] == '|'
}

func isListItem(trimmed string) bool {
	if len(trimmed) < 2 {
		return false
	}
	// unordered: - , * , + followed by space
	if (trimmed[0] == '-' || trimmed[0] == '*' || trimmed[0] == '+') && trimmed[1] == ' ' {
		return true
	}
	// ordered: digit(s). space
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] >= '0' && trimmed[i] <= '9' {
			continue
		}
		if trimmed[i] == '.' && i > 0 && i+1 < len(trimmed) && trimmed[i+1] == ' ' {
			return true
		}
		break
	}
	return false
}
