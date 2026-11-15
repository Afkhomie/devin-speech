package tts

import (
	"strings"
)

// SplitSentences splits text into speakable sentences: strips markdown,
// splits on sentence-ending punctuation, caps each at ~300 chars.
func SplitSentences(text string) []string {
	clean := stripMarkdown(text)
	clean = strings.Join(strings.Fields(clean), " ")
	if clean == "" {
		return nil
	}

	var out []string
	var cur strings.Builder
	flush := func() {
		s := strings.TrimSpace(cur.String())
		cur.Reset()
		if s == "" {
			return
		}
		// hard-cap very long sentences
		for len(s) > 300 {
			cut := strings.LastIndex(s[:300], " ")
			if cut < 100 {
				cut = 300
			}
			out = append(out, strings.TrimSpace(s[:cut]))
			s = strings.TrimSpace(s[cut:])
		}
		if s != "" {
			out = append(out, s)
		}
	}

	for _, r := range clean {
		cur.WriteRune(r)
		if strings.ContainsRune(".!?\n", r) {
			// avoid splitting on abbreviations/ellipses crudely: require next char space/end handled naturally
			flush()
		}
	}
	flush()
	return out
}

func stripMarkdown(s string) string {
	var b strings.Builder
	inCode := false
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		// headings
		trimmed = strings.TrimLeft(trimmed, "#")
		trimmed = strings.TrimSpace(trimmed)
		// bullets
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			trimmed = trimmed[2:]
		}
		// links [text](url) -> text
		trimmed = stripLinks(trimmed)
		// bold/italic markers
		trimmed = strings.ReplaceAll(trimmed, "**", "")
		trimmed = strings.ReplaceAll(trimmed, "__", "")
		trimmed = strings.ReplaceAll(trimmed, "`", "")
		trimmed = strings.Trim(trimmed, "*_")
		b.WriteString(trimmed)
		b.WriteByte('\n')
	}
	return b.String()
}

func stripLinks(s string) string {
	for {
		i := strings.Index(s, "[")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "](")
		if j < 0 {
			return s
		}
		j += i
		k := strings.Index(s[j:], ")")
		if k < 0 {
			return s
		}
		k += j
		s = s[:i] + s[i+1:j] + s[k+1:]
	}
}

