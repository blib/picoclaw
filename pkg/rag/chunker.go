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
			limit := softLimit
			if len(text) <= hardLimit {
				if len(text) < softLimit {
					limit = len(text)
				}
			} else {
				limit = hardLimit
			}
			part := strings.TrimSpace(text[:limit])
			chunks = append(chunks, ChunkLocAndText{
				Loc:  ChunkLoc{HeadingPath: headingPath, StartChar: start, EndChar: end},
				Text: part,
			})
			if limit >= len(text) {
				text = ""
			} else {
				text = strings.TrimSpace(text[limit:])
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

func normalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSpace(s)
	spaceRE := regexp.MustCompile(`\s+`)
	return strings.TrimSpace(spaceRE.ReplaceAllString(s, " "))
}
