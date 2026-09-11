// Package knowledge implements the semantic knowledge layer: a library of
// Markdown documents (glossary, endpoint/schema/field annotations and
// high-level "capability" tasks) that is indexed by the server so knowledge can
// be extended during a session and used to discover/execute high-level tasks.
//
// Each document combines YAML front-matter (the machine-readable part: kind,
// intents, tasks steps, relations) with a Markdown body (the human-readable part
// that uses relative Markdown links to relate the concepts).
package knowledge

import (
	"path"
	"regexp"
	"strings"
)

// Kind identifies what a document describes.
type Kind string

const (
	KindIndex      Kind = "index"
	KindGlossary   Kind = "glossary"
	KindEndpoint   Kind = "endpoint"
	KindSchema     Kind = "schema"
	KindField      Kind = "field"
	KindCapability Kind = "capability"
	KindPattern    Kind = "pattern" // reusable, API-agnostic procedure
	KindTool       Kind = "tool"    // documents a tool / integration / command
	KindIdea       Kind = "idea"    // free-form note; never executed
	KindScript     Kind = "script"  // executable tengo: surfaced as an MCP tool
)

// Doc is a single knowledge document. The YAML front-matter holds the machine
// part; Body holds the human prose with relative Markdown links.
type Doc struct {
	ID          string   `yaml:"id,omitempty"`
	Kind        Kind     `yaml:"kind,omitempty"`
	API         string   `yaml:"api,omitempty"`
	Language    string   `yaml:"language,omitempty"`
	Summary     string   `yaml:"summary,omitempty"`
	Anchor      string   `yaml:"anchor,omitempty"`
	Tags        []string `yaml:"tags,omitempty"`
	Intents     []string `yaml:"intents,omitempty"`
	Params      []Param  `yaml:"params,omitempty"`
	Steps       []Step   `yaml:"steps,omitempty"`
	Related     []Rel    `yaml:"related,omitempty"`
	Permissions []string `yaml:"permissions,omitempty"` // required capability scopes for kind: script
	Draft       bool     `yaml:"draft,omitempty"`

	Body string `yaml:"-"`

	// Path is the library-relative path of the document (e.g.
	// "endpoints/create-user.md"). Links use this as the stable target base.
	Path string `yaml:"-"`
	// Title is the first "# Heading" of the body, or the ID when absent.
	Title string `yaml:"-"`
}

// Param is a free-form parameter a capability task accepts from natural language.
type Param struct {
	Name     string `yaml:"name"`
	Required bool   `yaml:"required"`
}

// Step is one executable step of a capability task. Inputs bind task parameters
// or earlier outputs into the step's tool arguments; Outputs record where a
// value produced by the response is to be captured for later steps.
type Step struct {
	Tool    string                  `yaml:"tool"`
	Inputs  map[string]InputBinding `yaml:"inputs"`
	Outputs map[string]string       `yaml:"outputs"`
}

// InputBinding describes where the value for a tool argument comes from:
// "param.<name>" (a task parameter) or "step.<N>.<jsonpath>" (output of a
// previous step). A plain value is used verbatim.
type InputBinding struct {
	From string `yaml:"from"`
}

// Rel is a typed relationship from this document to another one.
type Rel struct {
	Type   string `yaml:"type"`
	Target string `yaml:"target"` // library-relative path of the target doc
}

// headingRe matches the first Markdown heading.
var headingRe = regexp.MustCompile(`(?m)^\s*#\s+(.+)$`)

// ExtractTitle returns the first "# heading" of the body, trimmed.
func ExtractTitle(body string) string {
	if m := headingRe.FindStringSubmatch(body); len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// Fields returns the ordered list of the document's parameter names.
func (d *Doc) ParamNames() []string {
	out := make([]string, 0, len(d.Params))
	for _, p := range d.Params {
		out = append(out, p.Name)
	}
	return out
}

// NormalizedPath joins a base library path with a (possibly "../"-relative)
// link target and cleans it, so links can be resolved to canonical paths.
func NormalizedPath(base, target string) string {
	if target == "" {
		return ""
	}
	// Strip an optional #anchor suffix; heading anchors live within the target
	// file and are not part of the file path.
	if i := strings.IndexByte(target, '#'); i >= 0 {
		target = target[:i]
	}
	dir := path.Dir(base)
	if dir == "." {
		dir = ""
	}
	return path.Clean(path.Join(dir, target))
}
