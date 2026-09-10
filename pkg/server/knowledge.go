package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/ckanthony/openapi-mcp/pkg/knowledge"
	"github.com/ckanthony/openapi-mcp/pkg/logx"
	"github.com/ckanthony/openapi-mcp/pkg/mcp"
)

var kLog = logx.Module("knowledge")

// knowledgeTrace is one recorded, successful tool call of a connection (only
// when the owning API has learning enabled).
type knowledgeTrace struct {
	At    time.Time
	API   string
	Tool  string
	Input map[string]interface{}
}

// knowledgeRoot returns the effective library root for an API definition.
func knowledgeRoot(def config.APIDefinition, persistPath string) string {
	if def.Knowledge.Root != "" {
		return def.Knowledge.Root
	}
	dir := ""
	if persistPath != "" {
		dir = filepath.Dir(persistPath)
	}
	if dir == "" {
		if h := os.Getenv("HOME"); h != "" {
			dir = filepath.Join(h, ".config", "openapi-mcp")
		} else {
			dir = "."
		}
	}
	return filepath.Join(dir, "knowledge", def.Name)
}

// knowledgeLoadOptions builds the anchor/step validation set for an entry.
func knowledgeLoadOptions(api *apiEntry) knowledge.LoadOptions {
	lang := api.Def.Knowledge.Language
	if lang == "" {
		lang = "en"
	}
	ops := map[string]bool{}
	if api.ToolSet != nil {
		for name := range api.ToolSet.Operations {
			ops[name] = true
			ops[api.Def.Name+"__"+name] = true
		}
	}
	schemas := map[string]bool{}
	if api.Doc != nil {
		for n := range api.Doc.Schemas {
			schemas[n] = true
		}
	}
	return knowledge.LoadOptions{
		API:        api.Def.Name,
		Language:   lang,
		Operations: ops,
		Schemas:    schemas,
	}
}

// apiEntryFor returns the API entry or an error. Callers must not hold r.mu.
// When the API has knowledge enabled and it has not been indexed yet, the
// library is loaded on demand so knowledge reads work right after a restart.
func (r *Registry) apiEntryFor(apiName string) (*apiEntry, error) {
	r.mu.RLock()
	entry, ok := r.apis[apiName]
	if !ok {
		r.mu.RUnlock()
		return nil, fmt.Errorf("API %q is not registered", apiName)
	}
	needs := entry.Def.Knowledge.Enabled && entry.Knowledge == nil
	r.mu.RUnlock()
	if needs {
		if err := r.ensureKnowledge(entry); err != nil {
			return nil, err
		}
	}
	return entry, nil
}

// KnowledgeStatus summarizes the knowledge configuration and library state.
func (r *Registry) KnowledgeStatus(apiName string) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	kc := entry.Def.Knowledge
	b := kc.ResolveKnowledgeBackend()
	var sb strings.Builder
	fmt.Fprintf(&sb, "api:            %s\n", apiName)
	fmt.Fprintf(&sb, "enabled:        %v\n", kc.Enabled)
	fmt.Fprintf(&sb, "language:       %s\n", orDefault(kc.Language, "en"))
	fmt.Fprintf(&sb, "root:           %s\n", knowledgeRoot(entry.Def, r.persistPath))
	fmt.Fprintf(&sb, "backend:        %s\n", b.Type)
	if b.Type == "git" {
		fmt.Fprintf(&sb, "repository:     %s\n", b.Repository)
		fmt.Fprintf(&sb, "branch:         %s\n", orDefault(b.Branch, "main"))
		fmt.Fprintf(&sb, "sync:           %s\n", b.Sync)
		fmt.Fprintf(&sb, "conflict:       %s\n", b.Conflict)
	}
	fmt.Fprintf(&sb, "learning:       %v\n", kc.Learning.Enabled)
	if entry.Knowledge == nil {
		sb.WriteString("library:        (not loaded; run knowledge_load)\n")
	} else {
		caps := len(entry.Knowledge.Capabilities())
		fmt.Fprintf(&sb, "documents:      %d\n", len(entry.Knowledge.Docs))
		fmt.Fprintf(&sb, "capabilities:   %d\n", caps)
		fmt.Fprintf(&sb, "warnings:       %d\n", len(entry.Knowledge.Warnings))
		for _, w := range entry.Knowledge.Warnings {
			fmt.Fprintf(&sb, "  - %s\n", w)
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// ensureKnowledge loads the library on demand when the API has knowledge
// enabled and it has not been indexed yet (e.g. right after a restart).
func (r *Registry) ensureKnowledge(entry *apiEntry) error {
	if entry == nil || !entry.Def.Knowledge.Enabled || entry.Knowledge != nil {
		return nil
	}
	b := entry.Def.Knowledge.ResolveKnowledgeBackend()
	if b.Type == "git" {
		return fmt.Errorf("API %q: git knowledge backend is not implemented yet; use type: local", entry.Def.Name)
	}
	root := knowledgeRoot(entry.Def, r.persistPath)
	lib, err := knowledge.LoadLocal(root, knowledgeLoadOptions(entry))
	if err != nil {
		return fmt.Errorf("API %q: failed to load knowledge library: %w", entry.Def.Name, err)
	}
	r.mu.Lock()
	entry.Knowledge = lib
	r.mu.Unlock()
	return nil
}

// LoadKnowledge indexes the API's knowledge library from disk (local backend).
// It reports the number of indexed documents and any library warnings.
func (r *Registry) LoadKnowledge(apiName string) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	if !entry.Def.Knowledge.Enabled {
		return "", fmt.Errorf("API %q has knowledge disabled (set knowledge.enabled and reload the API or use update_api_knowledge)", apiName)
	}
	if b := entry.Def.Knowledge.ResolveKnowledgeBackend(); b.Type == "git" {
		return "", fmt.Errorf("API %q: git knowledge backend is not implemented yet (phase 2); use type: local", apiName)
	}
	root := knowledgeRoot(entry.Def, r.persistPath)
	lib, err := knowledge.LoadLocal(root, knowledgeLoadOptions(entry))
	if err != nil {
		return "", fmt.Errorf("API %q: failed to load knowledge library: %w", apiName, err)
	}

	r.mu.Lock()
	entry.Knowledge = lib
	r.mu.Unlock()

	var b strings.Builder
	fmt.Fprintf(&b, "Loaded %d document(s) from %s\n", len(lib.Docs), root)
	for _, w := range lib.Warnings {
		fmt.Fprintf(&b, "WARN: %s\n", w)
	}
	if len(lib.Warnings) == 0 {
		b.WriteString("No warnings.")
	}
	return strings.TrimSpace(b.String()), nil
}

// mergedLibrary combines the persisted library with the per-connection overlay
// so lookups see session knowledge too (overlay wins by being appended last).
func (r *Registry) mergedLibrary(api *apiEntry, connID string) *knowledge.Library {
	base := api.Knowledge
	if base == nil {
		base = &knowledge.Library{}
	}
	m := *base
	m.Docs = append(append([]*knowledge.Doc{}, base.Docs...), r.overlayDocs(connID, api.Def.Name)...)
	m.Warnings = append([]string{}, base.Warnings...)
	m.ByID = base.ByID
	m.ByPath = base.ByPath
	return &m
}

func (r *Registry) overlayDocs(connID, apiName string) []*knowledge.Doc {
	r.mu.RLock()
	defer r.mu.RUnlock()
	byAPI := r.sessionKnowledge[connID]
	if byAPI == nil {
		return nil
	}
	docs := byAPI[apiName]
	out := make([]*knowledge.Doc, 0, len(docs))
	for _, d := range docs {
		out = append(out, d)
	}
	return out
}

// overlayPath returns where an overlay doc should be placed when persisted.
func overlayPathFor(doc *knowledge.Doc) string {
	switch doc.Kind {
	case knowledge.KindCapability:
		return "capabilities/" + doc.ID + ".md"
	case knowledge.KindGlossary:
		return "glossary/" + doc.ID + ".md"
	case knowledge.KindEndpoint:
		return "elements/endpoints/" + doc.ID + ".md"
	case knowledge.KindSchema:
		return "elements/schemas/" + doc.ID + ".md"
	case knowledge.KindField:
		return "elements/fields/" + doc.ID + ".md"
	default:
		return doc.ID + ".md"
	}
}

// KnowledgeUpsert adds or replaces a knowledge document. persist=false writes
// into the per-connection overlay; persist=true writes the Markdown file into
// the library and re-indexes.
func (r *Registry) KnowledgeUpsert(connID, apiName, content, docPath string, persist bool) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	if !entry.Def.Knowledge.Enabled {
		return "", fmt.Errorf("API %q has knowledge disabled", apiName)
	}
	doc, err := knowledge.ParseDoc([]byte(content))
	if err != nil {
		return "", fmt.Errorf("invalid knowledge document: %w", err)
	}
	if doc.ID == "" {
		doc.ID = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(doc.Title)), " ", "-")
	}
	if !validDocID(doc.ID) {
		return "", fmt.Errorf("invalid document id %q", doc.ID)
	}
	if doc.API == "" {
		doc.API = apiName
	}
	if doc.Title == "" {
		doc.Title = doc.ID
	}
	if doc.Language == "" {
		doc.Language = entry.Def.Knowledge.Language
	}

	if !persist {
		r.mu.Lock()
		if r.sessionKnowledge[connID] == nil {
			r.sessionKnowledge[connID] = map[string]map[string]*knowledge.Doc{}
		}
		if r.sessionKnowledge[connID][apiName] == nil {
			r.sessionKnowledge[connID][apiName] = map[string]*knowledge.Doc{}
		}
		r.sessionKnowledge[connID][apiName][doc.ID] = doc
		r.mu.Unlock()
		return fmt.Sprintf("Saved document %q in the session overlay (not persisted). Use persist=true to write it to the manual.", doc.ID), nil
	}

	b := entry.Def.Knowledge.ResolveKnowledgeBackend()
	if b.Type == "git" {
		return "", fmt.Errorf("git knowledge backend is not implemented yet (phase 2); use type: local")
	}
	data, err := knowledge.Serialize(doc)
	if err != nil {
		return "", err
	}
	if docPath == "" {
		docPath = overlayPathFor(doc)
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(knowledgeRoot(entry.Def, r.persistPath), docPath)), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(knowledgeRoot(entry.Def, r.persistPath), filepath.FromSlash(docPath)), data, 0o644); err != nil {
		return "", err
	}
	if _, err := r.LoadKnowledge(apiName); err != nil {
		return "", err
	}
	return fmt.Sprintf("Persisted document %q to %s and re-indexed.", doc.ID, docPath), nil
}

// KnowledgeInit scaffolds the knowledge library for an API from the spec:
// an index document, sample glossary/capability placeholders and skeleton docs
// for endpoints (optionally filtered by tag) and schemas, in the configured
// language.
func (r *Registry) KnowledgeInit(apiName string, tags []string) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	if !entry.Def.Knowledge.Enabled {
		return "", fmt.Errorf("API %q has knowledge disabled (set knowledge.enabled and reload the API or use update_api_knowledge)", apiName)
	}
	if b := entry.Def.Knowledge.ResolveKnowledgeBackend(); b.Type == "git" {
		return "", fmt.Errorf("git knowledge backend is not implemented yet (phase 2); use type: local")
	}
	lang := entry.Def.Knowledge.Language
	if lang == "" {
		lang = "en"
	}
	root := knowledgeRoot(entry.Def, r.persistPath)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	backend := knowledge.NewLocalBackend(root)

	wrote := 0
	write := func(rel, content string) error {
		if err := backend.WriteFile(rel, []byte(content)); err != nil {
			return err
		}
		wrote++
		return nil
	}

	if err := write("_index.md", knowledge.IndexSkeleton(apiName, lang)); err != nil {
		return "", err
	}
	if err := write("glossary/incidencias.md", knowledge.GlossarySkeleton(apiName, "incidencias", lang)); err != nil {
		return "", err
	}
	if err := write("capabilities/nueva-tarea.md", knowledge.CapabilitySkeleton(apiName, "Nueva tarea", lang)); err != nil {
		return "", err
	}

	tagSet := map[string]bool{}
	for _, t := range tags {
		tagSet[strings.ToLower(t)] = true
	}
	if entry.Doc != nil {
		for _, ep := range entry.Doc.Endpoints {
			if len(tagSet) > 0 && !hasAnyTag(ep.Tags, tagSet) {
				continue
			}
			params := make([]string, 0, len(ep.Parameters))
			for _, p := range ep.Parameters {
				params = append(params, p.Name)
			}
			content := knowledge.EndpointSkeleton(apiName, ep.OperationID, ep.Method, ep.Path, lang, params)
			if err := write("elements/endpoints/"+sanitizeFile(ep.OperationID)+".md", content); err != nil {
				return "", err
			}
		}
		names := make([]string, 0, len(entry.Doc.Schemas))
		for n := range entry.Doc.Schemas {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			props := schemaPropNames(entry.Doc.Schemas[n])
			content := knowledge.SchemaSkeleton(apiName, n, lang, props)
			if err := write("elements/schemas/"+sanitizeFile(n)+".md", content); err != nil {
				return "", err
			}
		}
	}

	if _, err := r.LoadKnowledge(apiName); err != nil {
		return "", err
	}
	return fmt.Sprintf("Scaffolded %d documents for API %q at %s (language %s). Edit them and run knowledge_load, or keep using knowledge_upsert.", wrote, apiName, root, lang), nil
}

func hasAnyTag(tags []string, set map[string]bool) bool {
	for _, t := range tags {
		if set[strings.ToLower(t)] {
			return true
		}
	}
	return false
}

func sanitizeFile(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

func schemaPropNames(s *mcp.SchemaDoc) []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.Properties))
	for n := range s.Properties {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// KnowledgeInitAlias keeps callers using either "knowledge_init" naming.
const KnowledgeInitAlias = "knowledge_init"

// DeleteKnowledge removes a document from the overlay or the library.
func (r *Registry) DeleteKnowledge(connID, apiName, docRef string, persist bool) (string, error) {
	return r.KnowledgeDelete(connID, apiName, docRef, persist)
}

// KnowledgeDelete removes a document from the overlay or the library.
func (r *Registry) KnowledgeDelete(connID, apiName, docRef string, persist bool) (string, error) {
	if persist {
		return "", fmt.Errorf("persisted delete is not supported yet; remove the file from the library manually and run knowledge_load")
	}
	r.mu.Lock()
	m := r.sessionKnowledge[connID][apiName]
	if m != nil {
		if _, ok := m[docRef]; ok {
			delete(m, docRef)
			r.mu.Unlock()
			return fmt.Sprintf("Removed overlay document %q.", docRef), nil
		}
	}
	r.mu.Unlock()
	return "", fmt.Errorf("no overlay document %q in this session (persisted docs must be removed from the library files)", docRef)
}

// KnowledgeGet returns a document (overlay first, then library) as Markdown.
func (r *Registry) KnowledgeGet(connID, apiName, docRef string) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	if doc, ok := r.overlayGet(connID, apiName, docRef); ok {
		return renderDoc(doc), nil
	}
	if entry.Knowledge != nil {
		if doc, ok := entry.Knowledge.Get(docRef); ok {
			return renderDoc(doc), nil
		}
	}
	return "", fmt.Errorf("document %q not found in API %q knowledge (loaded during this assistant session; run knowledge_load if it was added on disk)", docRef, apiName)
}

func (r *Registry) overlayGet(connID, apiName, docRef string) (*knowledge.Doc, bool) {
	if docRef == "" {
		return nil, false
	}
	for _, d := range r.overlayDocs(connID, apiName) {
		if d.ID == docRef || d.Path == docRef || strings.TrimPrefix(docRef, "/") == d.Path {
			return d, true
		}
	}
	return nil, false
}

// KnowledgeSearch ranks documents (library + overlay) by relevance to query.
func (r *Registry) KnowledgeSearch(connID, apiName, query string, limit int) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	merged := r.mergedLibrary(entry, connID)
	hits := merged.Search(query, limit)
	payload := make([]map[string]interface{}, 0, len(hits))
	for _, h := range hits {
		d := h.Doc
		payload = append(payload, map[string]interface{}{
			"id":       d.ID,
			"kind":     d.Kind,
			"title":    d.Title,
			"path":     d.Path,
			"summary":  d.Summary,
			"intents":  d.Intents,
			"params":   d.ParamNames(),
			"draft":    d.Draft,
			"language": d.Language,
			"score":    h.Score,
		})
	}
	body, _ := json.MarshalIndent(map[string]interface{}{
		"api":       apiName,
		"query":     query,
		"documents": payload,
	}, "", "  ")
	return string(body), nil
}

// KnowledgeCapabilities lists the capability documents (library + overlay).
func (r *Registry) KnowledgeCapabilities(connID, apiName string) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	caps := r.mergedLibrary(entry, connID).Capabilities()
	payload := make([]map[string]interface{}, 0, len(caps))
	for _, c := range caps {
		payload = append(payload, map[string]interface{}{
			"id":      c.ID,
			"title":   c.Title,
			"path":    c.Path,
			"summary": c.Summary,
			"intents": c.Intents,
			"params":  c.ParamNames(),
			"steps":   len(c.Steps),
			"draft":   c.Draft,
		})
	}
	body, _ := json.MarshalIndent(payload, "", "  ")
	return string(body), nil
}

// DiscoverTask resolves a natural-language intent against the capabilities and
// returns the best match with its parameters and a static plan (never executes).
func (r *Registry) DiscoverTask(connID, apiName, intent string) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	merged := r.mergedLibrary(entry, connID)
	lib := *merged
	// Only capabilities are candidate tasks.
	lib.Docs = lib.Capabilities()
	hits := lib.Search(intent, 1)
	var best *knowledge.Doc
	if len(hits) > 0 {
		best = hits[0].Doc
	}
	extras := merged.Search(intent, 3)
	related := make([]map[string]interface{}, 0, len(extras))
	for _, h := range extras {
		related = append(related, map[string]interface{}{"id": h.Doc.ID, "kind": h.Doc.Kind, "title": h.Doc.Title, "score": h.Score})
	}

	resp := map[string]interface{}{
		"api":         apiName,
		"intent":      intent,
		"related":     related,
		"matched":     best != nil,
		"suggestions": []string{},
	}
	if best == nil {
		resp["message"] = "No capability matches this intent. Run knowledge_search or add/describe a capability with knowledge_upsert."
		body, _ := json.MarshalIndent(resp, "", "  ")
		return string(body), nil
	}
	plan := make([]map[string]interface{}, 0, len(best.Steps))
	for i, st := range best.Steps {
		inputs := map[string]interface{}{}
		for k, in := range st.Inputs {
			inputs[k] = in.From
		}
		_, opValid := entry.ToolSet.Operations[st.Tool]
		if !opValid {
			_, opValid = entry.ToolSet.Operations[strings.TrimPrefix(st.Tool, entry.Def.Name+"__")]
		}
		plan = append(plan, map[string]interface{}{
			"step":      i + 1,
			"tool":      st.Tool,
			"inputs":    inputs,
			"outputs":   st.Outputs,
			"static_ok": opValid,
		})
	}
	resp["capability"] = best.ID
	resp["title"] = best.Title
	resp["params"] = best.ParamNames()
	resp["plan"] = plan
	body, _ := json.MarshalIndent(resp, "", "  ")
	return string(body), nil
}

// ClarifyKnowledge explains what knowledge is available for an intent.
func (r *Registry) ClarifyKnowledge(connID, apiName, intent string) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	hits := r.mergedLibrary(entry, connID).Search(intent, 5)
	if len(hits) == 0 {
		return fmt.Sprintf("No knowledge matching %q. Suggest adding a glossary term (knowledge_upsert, kind glossary) or endpoints docs (knowledge_init).", intent), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Closest documents for %q:\n", intent)
	for _, h := range hits {
		fmt.Fprintf(&b, "  - %s [%s] (score %d)\n", h.Doc.Title, h.Doc.Path, h.Score)
	}
	return strings.TrimSpace(b.String()), nil
}

// RememberSequence builds a capability draft in the overlay from the recorded
// tool calls of this connection for the given API.
func (r *Registry) RememberSequence(connID, apiName, name string) (string, error) {
	entry, err := r.apiEntryFor(apiName)
	if err != nil {
		return "", err
	}
	var traces []knowledgeTrace
	r.mu.RLock()
	traces = append(traces, r.sessionTraces[connID]...)
	r.mu.RUnlock()

	var calls []knowledgeTrace
	for _, t := range traces {
		if t.API == apiName {
			calls = append(calls, t)
		}
	}
	if len(calls) == 0 {
		return "", fmt.Errorf("no recorded tool calls for API %q in this session; enabling knowledge.learning records them automatically", apiName)
	}
	if name == "" {
		name = "draft-" + strconv.FormatInt(time.Now().Unix(), 10)
	}
	steps := make([]knowledge.Step, 0, len(calls))
	for _, t := range calls {
		inputs := map[string]knowledge.InputBinding{}
		for k, v := range t.Input {
			if k == targetArgName {
				continue
			}
			if s, ok := v.(string); ok {
				inputs[k] = knowledge.InputBinding{From: "param." + s}
			}
		}
		steps = append(steps, knowledge.Step{
			Tool:   t.Tool,
			Inputs: inputs,
		})
	}
	doc := &knowledge.Doc{
		ID:       name,
		Kind:     knowledge.KindCapability,
		API:      apiName,
		Language: entry.Def.Knowledge.Language,
		Title:    "Draft: " + name,
		Draft:    true,
		Steps:    steps,
	}
	r.mu.Lock()
	if r.sessionKnowledge[connID] == nil {
		r.sessionKnowledge[connID] = map[string]map[string]*knowledge.Doc{}
	}
	if r.sessionKnowledge[connID][apiName] == nil {
		r.sessionKnowledge[connID][apiName] = map[string]*knowledge.Doc{}
	}
	r.sessionKnowledge[connID][apiName][doc.ID] = doc
	r.mu.Unlock()
	return fmt.Sprintf("Created draft capability %q in the session overlay from %d call(s). Persist it with knowledge_upsert (persist=true).", doc.ID, len(calls)), nil
}

// RecordTrace records a successful tool call for learning (no-op unless enabled).
func (r *Registry) RecordTrace(connID, apiName, toolName string, input map[string]interface{}) {
	if connID == "" {
		return
	}
	r.mu.RLock()
	api := r.apis[apiName]
	if api == nil || !api.Def.Knowledge.Enabled || !api.Def.Knowledge.Learning.Enabled {
		r.mu.RUnlock()
		return
	}
	r.mu.RUnlock()

	t := knowledgeTrace{At: time.Now(), API: apiName, Tool: toolName, Input: input}
	r.mu.Lock()
	r.sessionTraces[connID] = append(r.sessionTraces[connID], t)
	r.mu.Unlock()
}

// UpdateKnowledgeConfig replaces an API's knowledge configuration in place,
// persists it and reloads the library.
func (r *Registry) UpdateKnowledgeConfig(apiName string, kc config.KnowledgeConfig) (string, error) {
	r.mu.Lock()
	entry, ok := r.apis[apiName]
	if !ok {
		r.mu.Unlock()
		return "", fmt.Errorf("API %q is not registered", apiName)
	}
	entry.Def.Knowledge = kc
	err := r.persist(r.apis)
	r.mu.Unlock()
	if err != nil {
		return "", err
	}
	if kc.Enabled {
		if _, err := r.LoadKnowledge(apiName); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("Knowledge config of API %q updated (enabled=%v, language=%q, type=%q).", apiName, kc.Enabled, kc.Language, kc.ResolveKnowledgeBackend().Type), nil
}

func validDocID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// renderDoc serializes a document back to its Markdown form (front-matter +
// body) for the agent to read.
func renderDoc(doc *knowledge.Doc) string {
	data, err := knowledge.Serialize(doc)
	if err != nil {
		return doc.Body
	}
	return string(data)
}
