// Package formatting provides channel-specific text formatters that convert
// LLM output (typically Markdown) into the target channel's native format.
package formatting

import (
	"regexp"
	"strings"
)

// Precompiled regexes for Markdown → WhatsApp text conversion.
//
// WhatsApp natively supports (as of 2024):
//
//	*bold*  _italic_  ~strikethrough~  ```monospace block```
//	`inline code`  > blockquote  - bullet list  1. numbered list
//
// The formatter converts Markdown-only syntax (**, ~~, [text](url), # headers,
// horizontal rules, * list markers) into the WhatsApp-native equivalents and
// strips anything WhatsApp cannot render.
var (
	mdBoldRe          = regexp.MustCompile(`\*\*(.+?)\*\*`)
	mdBoldUnderRe     = regexp.MustCompile(`__(.+?)__`)
	mdStrikeRe        = regexp.MustCompile(`~~(.+?)~~`)
	mdImageRe         = regexp.MustCompile(`!\[(?:[^\]]*)\]\((?P<url>[^)]+)\)`)
	mdLinkRe          = regexp.MustCompile(`\[(?P<label>[^\]]+)\]\((?P<url>[^)]+)\)`)
	mdLinkFallbackRe  = regexp.MustCompile(`\[([^\]]*)\]\s*\(\s*([^)]*)\s*\)`)
	mdHrRe            = regexp.MustCompile(`(?m)^\s*(?:[-*_]\s*){3,}$`)
	mdHeaderRe        = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)$`)
	mdListMarkerRe    = regexp.MustCompile(`(?m)^(\s*)[*+]\s+`)
	mdCodeFenceLangRe = regexp.MustCompile("(?m)^```[a-zA-Z0-9]+\\s*$")
	excessiveNewlines = regexp.MustCompile(`\n{3,}`)
	// Literal "bold"/"italic" sometimes emitted by LLMs next to *; strip only when adjacent.
	literalBoldBeforeStarRe  = regexp.MustCompile(`(?i)\bbold\s*\*`)
	literalItalicAfterStarRe = regexp.MustCompile(`\*\s*(?i)italic\b`)
	// Line starts with * but has no closing * on same line (WhatsApp needs *text* for bold).
	unclosedBoldAtLineStartRe = regexp.MustCompile(`(?m)^(\s*\*)([^\n*]+)(\s*)$`)
)

// WhatsApp converts Markdown produced by the LLM into WhatsApp-native
// formatting. It is intentionally aggressive: even if the system prompt asks
// the model not to use Markdown, models frequently ignore the instruction.
func WhatsApp(s string) string {
	if s == "" {
		return s
	}

	out := s

	// 1. Image syntax → raw URL (before links, since images have ! prefix).
	out = mdImageRe.ReplaceAllString(out, `${url}`)

	// 2. Markdown links → raw URL (WhatsApp auto-links bare URLs).
	out = mdLinkRe.ReplaceAllStringFunc(out, func(m string) string {
		sub := mdLinkRe.FindStringSubmatch(m)
		if len(sub) == 0 {
			return m
		}
		label := strings.TrimSpace(sub[mdLinkRe.SubexpIndex("label")])
		u := strings.TrimSpace(sub[mdLinkRe.SubexpIndex("url")])
		if label == "" || label == u {
			return u
		}
		return label + "\n" + u
	})

	// 2b. Fallback for any remaining [x](y) patterns.
	out = mdLinkFallbackRe.ReplaceAllStringFunc(out, func(m string) string {
		sub := mdLinkFallbackRe.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		label := strings.TrimSpace(sub[1])
		u := strings.TrimSpace(sub[2])
		if strings.HasPrefix(strings.ToLower(u), "http://") || strings.HasPrefix(strings.ToLower(u), "https://") {
			return u
		}
		if label != "" {
			return label
		}
		return u
	})

	// 3. Convert * and + list markers to - before bold conversion. This avoids
	//    ambiguity when a list item starts with bold text (e.g. "* **Heading**").
	out = mdListMarkerRe.ReplaceAllString(out, `$1- `)

	// 4. Inline formatting.
	out = mdBoldRe.ReplaceAllString(out, `*$1*`)
	out = mdBoldUnderRe.ReplaceAllString(out, `*$1*`)
	out = mdStrikeRe.ReplaceAllString(out, `~$1~`)

	// 5. Convert # headers: strip markers and bold-wrap the text.
	out = mdHeaderRe.ReplaceAllStringFunc(out, func(m string) string {
		sub := mdHeaderRe.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		text := strings.TrimSpace(sub[1])
		if text == "" {
			return ""
		}
		if strings.HasPrefix(text, "*") && strings.HasSuffix(text, "*") {
			return text
		}
		return "*" + text + "*"
	})

	// 6. Remove horizontal rules ("* * *", "---", "***", "_ _ _", etc.).
	out = mdHrRe.ReplaceAllString(out, "")

	// 7. Strip language tags from code fences (WhatsApp supports ``` but
	//    not ```python).
	out = mdCodeFenceLangRe.ReplaceAllString(out, "```")

	// 8. Strip literal "bold"/"italic" when adjacent to * (LLM/tool pollution).
	out = literalBoldBeforeStarRe.ReplaceAllString(out, "*")
	out = literalItalicAfterStarRe.ReplaceAllString(out, "*")

	// 9. Close unclosed bold at line start: *Heading: → *Heading:*
	out = unclosedBoldAtLineStartRe.ReplaceAllString(out, "$1$2*$3")

	// 10. Collapse excessive blank lines.
	out = excessiveNewlines.ReplaceAllString(out, "\n\n")

	return strings.TrimSpace(out)
}
