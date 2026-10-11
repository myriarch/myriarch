// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package refdoc

import (
	"fmt"
	"regexp"
	"strings"
)

// frontMatter returns the front matter block of a generated page (keys in the order the docs check requires).
func frontMatter(title string, weight int, description string) string {
	return fmt.Sprintf("---\ntitle: %q\nweight: %d\ndescription: %q\n---\n\n", title, weight, description)
}

// proseTokenRE finds what needs more than escaping in running text: a web address (with the quotes around it, if any) and a
// mention of a documentation file ("docs/operations/authentication.md").
var proseTokenRE = regexp.MustCompile(`"?https?://[^\s"'),;]+"?|docs/[A-Za-z0-9_./-]+?\.md\b`)

// proseEscaper escapes the characters that Markdown or Hugo would interpret in running text. Angle brackets matter most: Hugo
// would drop <base> as raw HTML.
var proseEscaper = strings.NewReplacer(`\`, `\\`, `*`, `\*`, `_`, `\_`, `<`, `\<`, `>`, `\>`, `[`, `\[`, `]`, `\]`, `|`, `\|`)

// inline turns the text of a Go doc comment into one line of Markdown, for running text and table cells: the lines are joined
// with single spaces, characters with a meaning in Markdown are escaped outside of code spans (inside a code span only the pipe,
// which would end a table cell), a web address becomes a code span (the addresses in the descriptions are example values, and a
// bare address is a lint error), and a mention of a documentation file becomes a relative link (the page lives in docs/reference).
// A backtick that is not paired is an error.
func inline(text string) (string, error) {
	text = strings.Join(strings.Fields(text), " ")
	if strings.Count(text, "`")%2 != 0 {
		return "", fmt.Errorf("unpaired backtick in %q", text)
	}
	var b strings.Builder
	for i, part := range strings.Split(text, "`") {
		if i%2 == 1 {
			b.WriteString("`" + strings.ReplaceAll(part, "|", `\|`) + "`")
			continue
		}
		last := 0
		for _, loc := range proseTokenRE.FindAllStringIndex(part, -1) {
			b.WriteString(proseEscaper.Replace(part[last:loc[0]]))
			last = loc[1]
			tok := part[loc[0]:loc[1]]
			if !strings.Contains(tok, "://") {
				b.WriteString("[" + proseEscaper.Replace(tok) + "](" + docLinkTarget(tok) + ")")
				continue
			}
			lead, trail := strings.HasPrefix(tok, `"`), len(tok) > 1 && strings.HasSuffix(tok, `"`)
			core := strings.Trim(tok, `"`)
			tail := ""
			for strings.HasSuffix(core, ".") || strings.HasSuffix(core, ":") {
				tail = core[len(core)-1:] + tail
				core = core[:len(core)-1]
			}
			switch {
			case lead && trail && tail == "":
				b.WriteString(code(`"` + core + `"`))
			default:
				if lead {
					b.WriteString(`"`)
				}
				b.WriteString(code(core) + proseEscaper.Replace(tail))
				if trail {
					b.WriteString(`"`)
				}
			}
		}
		b.WriteString(proseEscaper.Replace(part[last:]))
	}
	return b.String(), nil
}

// docLinkTarget returns the link from a page in docs/reference to a documentation file ("docs/operations/x.md").
func docLinkTarget(path string) string {
	rest := strings.TrimPrefix(path, "docs/")
	if strings.HasPrefix(rest, "reference/") {
		return strings.TrimPrefix(rest, "reference/")
	}
	return "../" + rest
}

// code returns s as a code span for a table cell or a sentence (only the pipe is escaped).
func code(s string) string {
	return "`" + strings.ReplaceAll(s, "|", `\|`) + "`"
}

// cell makes text safe for one table cell: no line breaks.
func cell(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// table renders a Markdown table.
func table(header []string, rows [][]string) string {
	var b strings.Builder
	b.WriteString("| " + strings.Join(header, " | ") + " |\n")
	b.WriteString("|" + strings.Repeat("---|", len(header)) + "\n")
	for _, r := range rows {
		cells := make([]string, len(r))
		for i, c := range r {
			cells[i] = cell(c)
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
	return b.String()
}

// shortcodeRE finds text that Hugo would take for a shortcode.
var shortcodeRE = regexp.MustCompile(`\{\{\s*[<%]`)

// finish checks a generated page and normalizes its end to exactly one newline.
func finish(page string) ([]byte, error) {
	if loc := shortcodeRE.FindStringIndex(page); loc != nil {
		return nil, fmt.Errorf("the page contains a Hugo shortcode opener near %q", page[loc[0]:min(len(page), loc[0]+20)])
	}
	return []byte(strings.TrimRight(page, "\n") + "\n"), nil
}
