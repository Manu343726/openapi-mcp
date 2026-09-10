package knowledge

import "regexp"

// linkRe matches inline Markdown links: [text](target).
var linkRe = regexp.MustCompile(`\[([^\]]*)\]\(([^)\s]+)\)`)

// LinkRef is one Markdown link found in a document body.
type LinkRef struct {
	Text   string
	Target string
}

// ExtractLinks returns every inline markdown link in body.
func ExtractLinks(body string) []LinkRef {
	matches := linkRe.FindAllStringSubmatch(body, -1)
	out := make([]LinkRef, 0, len(matches))
	for _, m := range matches {
		out = append(out, LinkRef{Text: m[1], Target: m[2]})
	}
	return out
}
