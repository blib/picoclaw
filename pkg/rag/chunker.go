package rag

import (
	"regexp"
	"strings"
)

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
