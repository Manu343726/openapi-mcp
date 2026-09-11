package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/ckanthony/openapi-mcp/pkg/knowledge"
	"github.com/ckanthony/openapi-mcp/pkg/mcp"
	"github.com/ckanthony/openapi-mcp/pkg/script"
)

// scriptTag is the synthetic exposure tag under which every per-API script is
// bucketed, so update_*_exposure can toggle a whole API's scripts at once
// (disabled_tags: ["script"] / active_tags: ["script"]).
const scriptTag = "script"

// scriptRef is one registered kind: script knowledge doc, surfaced as an MCP
// tool. Scripts are resolved outside the operation index (ResolveTool is
// operation-only) and dispatched by runScript.
type scriptRef struct {
	scope string // API name, or metaAPIName for a global script
	doc   *knowledge.Doc
	exec  *script.Executor
	tool  mcp.Tool // bare tool (Name = doc.ID)
	meta  bool     // true for _meta__* scripts (always exposed)
}

// refreshScriptsLocked re-scans every loaded knowledge library (per-API and the
// meta base) and rebuilds the script-tool map. Callers must hold r.mu.
func (r *Registry) refreshScriptsLocked() {
	refs := map[string]*scriptRef{}
	if r.metaLibrary != nil {
		for _, doc := range r.metaLibrary.Scripts() {
			full := toolFullName(metaAPIName, doc.ID)
			refs[full] = r.newScriptRef(metaAPIName, doc, true)
		}
	}
	for _, entry := range r.apis {
		if entry.Knowledge == nil {
			continue
		}
		for _, doc := range entry.Knowledge.Scripts() {
			full := toolFullName(entry.Def.Name, doc.ID)
			refs[full] = r.newScriptRef(entry.Def.Name, doc, false)
		}
	}
	r.scriptTools = refs
}

// RefreshScripts re-indexes scripts and republishes the tool snapshot. Called
// after a knowledge library load/change.
func (r *Registry) RefreshScripts() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refreshScriptsLocked()
	tools, index, err := r.rebuild(r.apis)
	if err != nil {
		return err
	}
	r.tools, r.index = tools, index
	r.notifyToolsListChanged()
	return nil
}

// newScriptRef builds the runtime reference (tool + executor) for a script doc.
// Callers must hold r.mu (it reads r.meta without locking).
func (r *Registry) newScriptRef(scope string, doc *knowledge.Doc, meta bool) *scriptRef {
	perms := script.Permissions{}
	for _, m := range doc.Permissions {
		perms[m] = true
	}
	sc := r.meta.Scripting
	timeout := sc.DefaultTimeout()
	if doc.TimeoutS > 0 {
		timeout = time.Duration(doc.TimeoutS) * time.Second
	}
	ref := &scriptRef{scope: scope, doc: doc, meta: meta}
	ref.exec = script.NewExecutor(script.Options{
		Permissions:   perms,
		ParamNames:    doc.ParamNames(),
		Timeout:       timeout,
		ExecAllowlist: sc.ExecAllowlist,
		FSReadRoots:   sc.FSReadRoots,
		HTTPAllowlist: sc.HTTPAllowlist,
	})
	ref.tool = buildScriptTool(doc)
	return ref
}

// buildScriptTool derives the MCP tool definition from a script doc: parameters
// become the input schema; the description carries the [script] marker and the
// declared permissions.
func buildScriptTool(doc *knowledge.Doc) mcp.Tool {
	props := map[string]mcp.Schema{}
	required := []string{}
	for _, p := range doc.Params {
		if strings.TrimSpace(p.Name) == "" {
			continue
		}
		t := p.Type
		if t == "" {
			t = "string"
		}
		props[p.Name] = mcp.Schema{Type: t, Description: p.Description}
		if p.Required {
			required = append(required, p.Name)
		}
	}
	desc := strings.TrimSpace(doc.Summary)
	if desc == "" {
		desc = doc.ID
	}
	perms := "none"
	if len(doc.Permissions) > 0 {
		perms = strings.Join(doc.Permissions, ", ")
	}
	desc = fmt.Sprintf("%s [script] Runs tengo in the MCP sandbox with declared permissions: %s.", desc, perms)
	return mcp.Tool{
		Name:        doc.ID,
		Description: desc,
		InputSchema: mcp.Schema{Type: "object", Properties: props, Required: required},
	}
}

// exposeScriptTool returns the client-facing copy (fully qualified name).
func exposeScriptTool(ref *scriptRef, fullName string) mcp.Tool {
	out := ref.tool
	out.Name = fullName
	return out
}

// scriptExposedLocked evaluates a script's runtime exposure. Meta scripts are
// always exposed; per-API scripts follow the owning API's (session-effective)
// exposure projection, bucketed under the "script" tag. Callers hold r.mu.
func (r *Registry) scriptExposedLocked(ref *scriptRef, connID string) bool {
	if ref.meta {
		return true
	}
	api := r.apis[ref.scope]
	if api == nil {
		return false
	}
	ex := api.Exposure
	if override, ok := r.sessionExposure[connID][ref.scope]; ok {
		ex = override
	}
	return exposureExposesOp(ex.NormalizeDefaults(), ref.doc.ID, []string{scriptTag})
}

// scriptToolFor returns the script ref for a fully qualified tool name.
func (r *Registry) scriptToolFor(fullName string) (*scriptRef, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ref, ok := r.scriptTools[fullName]
	return ref, ok
}

// scriptsForScopeLocked returns the script refs belonging to an API scope,
// sorted by fully qualified name. Callers hold r.mu.
func (r *Registry) scriptsForScopeLocked(scope string) []*scriptRef {
	names := make([]string, 0, len(r.scriptTools))
	for name, ref := range r.scriptTools {
		if ref.scope == scope {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := make([]*scriptRef, 0, len(names))
	for _, name := range names {
		out = append(out, r.scriptTools[name])
	}
	return out
}

// appendScriptsLocked appends the scripts exposed to connID (global baseline
// when connID is empty) to tools, in deterministic order. Callers hold r.mu.
func (r *Registry) appendScriptsLocked(tools []mcp.Tool, connID string) []mcp.Tool {
	names := make([]string, 0, len(r.scriptTools))
	for name := range r.scriptTools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ref := r.scriptTools[name]
		if r.scriptExposedLocked(ref, connID) {
			tools = append(tools, exposeScriptTool(ref, name))
		}
	}
	return tools
}

// RunScript executes a script tool for a session and returns its textual result.
// It enforces the per-session exposure gate before running.
func (r *Registry) RunScript(connID, fullName string, args map[string]interface{}) (string, error) {
	ref, ok := r.scriptToolFor(fullName)
	if !ok {
		return "", fmt.Errorf("script tool %q is not registered", fullName)
	}
	if !r.IsToolExposedForSession(connID, fullName) {
		return "", fmt.Errorf("%s; re-list tools to refresh the client's tool set", exposeActivationHint(fullName, ref.scope))
	}
	host := script.Host{
		Key: connID,
		Call: func(_ context.Context, tool string, in map[string]interface{}) (string, error) {
			return r.CallTool(connID, tool, in)
		},
		Resolve: func(tool string) (map[string]interface{}, bool) {
			return r.ToolInfo(tool)
		},
	}
	return ref.exec.Run(context.Background(), ref.doc.Source, args, host)
}

// ToolInfo reports lightweight metadata about any tool kind (operation, script
// or management); it backs mcp.resolve and script introspection.
func (r *Registry) ToolInfo(fullName string) (map[string]interface{}, bool) {
	if api, tool, ok := r.ResolveTool(fullName); ok {
		return map[string]interface{}{
			"name":         fullName,
			"kind":         "operation",
			"api":          api.Def.Name,
			"operation_id": tool.Name,
		}, true
	}
	if ref, ok := r.scriptToolFor(fullName); ok {
		return map[string]interface{}{
			"name":        fullName,
			"kind":        "script",
			"api":         ref.scope,
			"script_id":   ref.doc.ID,
			"permissions": ref.doc.Permissions,
		}, true
	}
	if r.IsManagementTool(fullName) {
		return map[string]interface{}{"name": fullName, "kind": "management"}, true
	}
	return nil, false
}

// CallTool executes any tool by fully qualified name and returns its textual// result: management tools, scripts and registered API operations all funnel
// here (it is the bridge the mcp script module calls).
func (r *Registry) CallTool(connID, fullName string, args map[string]interface{}) (string, error) {
	if _, ok := r.scriptToolFor(fullName); ok {
		return r.RunScript(connID, fullName, args)
	}
	if r.IsManagementTool(fullName) {
		res := r.runManagementTool(connID, fullName, args)
		if !res.ok {
			return res.text, fmt.Errorf("%s", res.text)
		}
		return res.text, nil
	}
	if _, _, ok := r.ResolveTool(fullName); ok {
		if !r.IsToolExposedForSession(connID, fullName) {
			api, _, _ := r.ResolveTool(fullName)
			return "", fmt.Errorf("%s; re-list tools to refresh the client's tool set", exposeActivationHint(fullName, api.Def.Name))
		}
		resp, err := executeRegisteredTool(r, connID, &ToolCallParams{ToolName: fullName, Input: args})
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return string(body), fmt.Errorf("tool %q returned HTTP %d", fullName, resp.StatusCode)
		}
		return string(body), nil
	}
	return "", fmt.Errorf("unknown tool %q", fullName)
}

// scriptView is the client-facing description of a script tool.
type scriptView struct {
	Name        string   `json:"name"` // fully qualified MCP tool name
	API         string   `json:"api"`
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Summary     string   `json:"summary,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	Params      []string `json:"params,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Source      string   `json:"source,omitempty"`
	Exposed     bool     `json:"exposed"`
}

// ListScripts returns the registered script tools, optionally scoped to one API
// (metaAPIName lists the global scripts). It backs script_list.
func (r *Registry) ListScripts(apiName string) (string, error) {
	r.mu.RLock()
	names := make([]string, 0, len(r.scriptTools))
	for name := range r.scriptTools {
		names = append(names, name)
	}
	sort.Strings(names)
	views := make([]scriptView, 0, len(names))
	for _, name := range names {
		ref := r.scriptTools[name]
		if apiName != "" && ref.scope != apiName {
			continue
		}
		views = append(views, r.scriptViewLocked(ref, name, ""))
	}
	r.mu.RUnlock()
	if len(views) == 0 {
		if apiName != "" {
			return "", fmt.Errorf("no scripts are registered for API %q", apiName)
		}
		return "No scripts are registered.", nil
	}
	body, _ := json.MarshalIndent(views, "", "  ")
	return string(body), nil
}

// DescribeScript returns the full definition of one script (by fully qualified
// tool name, or by api + id). It backs script_describe.
func (r *Registry) DescribeScript(apiName, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	r.mu.RLock()
	defer r.mu.RUnlock()
	var found *scriptRef
	var foundName string
	for name, sref := range r.scriptTools {
		if name == ref {
			found, foundName = sref, name
			break
		}
		if sref.doc.ID == ref && (apiName == "" || sref.scope == apiName) {
			found, foundName = sref, name
			break
		}
	}
	if found == nil {
		return "", fmt.Errorf("script %q is not registered", ref)
	}
	body, _ := json.MarshalIndent(r.scriptViewLocked(found, foundName, found.doc.Source), "", "  ")
	return string(body), nil
}

// scriptViewLocked builds the client view of a script ref. Callers hold r.mu.
func (r *Registry) scriptViewLocked(ref *scriptRef, fullName, source string) scriptView {
	return scriptView{
		Name:        fullName,
		API:         ref.scope,
		ID:          ref.doc.ID,
		Kind:        string(ref.doc.Kind),
		Summary:     ref.doc.Summary,
		Permissions: ref.doc.Permissions,
		Params:      ref.doc.ParamNames(),
		Tags:        ref.doc.Tags,
		Source:      source,
		Exposed:     r.scriptExposedLocked(ref, ""),
	}
}
