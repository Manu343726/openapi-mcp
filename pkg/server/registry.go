package server

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/ckanthony/openapi-mcp/pkg/logx"
	"github.com/ckanthony/openapi-mcp/pkg/mcp"
	"github.com/ckanthony/openapi-mcp/pkg/parser"
)

var regLog = logx.Module("registry")

const (
	// toolNameSep separates an API name from an operation id in the flat MCP
	// tool namespace (e.g. "weather__getCurrent"). Targets are intentionally NOT
	// part of the tool namespace.
	toolNameSep = "__"

	// targetArgName is the synthetic input property injected into every tool of
	// an API that has targets. It selects which target (server) handles the call.
	targetArgName = "target"

	// monitorPollInterval is how often the optional spec watcher re-checks the
	// monitored APIs' spec sources for changes (file mtime / HTTP Last-Modified).
	monitorPollInterval = 5 * time.Second

	// monitorLogger is the RFC5424 logger name attached to the
	// notifications/message events emitted when a monitored spec changes.
	monitorLogger = "openapi-mcp.monitoring"
)

var (
	apiIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
)

// apiEntry is the runtime state for one registered API.
type apiEntry struct {
	Def          config.APIDefinition
	SpecTitle    string
	SpecVersion  string
	RegisteredAt time.Time
	// SpecTimestamp is the last-modified time of the spec *source* (file mtime
	// or HTTP Last-Modified) at the moment it was loaded. Zero means the source
	// has no usable timestamp (e.g. an inline spec). It lets callers detect that
	// the loaded tools are stale relative to the source.
	SpecTimestamp time.Time
	ToolSet       *mcp.ToolSet // tools keyed by bare operation names
	Doc           *mcp.ApiDoc  // normalized spec documentation for introspection

	// tokenMu guards loginTokens (per-target login session cache).
	tokenMu     sync.Mutex
	loginTokens map[string]*tokenCacheEntry

	// lastSpecNotified is the spec-source mtime for which a monitoring change
	// (notification and/or auto-reload) was last acted on. It prevents the
	// watcher from re-notifying on every poll while the source is unchanged.
	// Not persisted.
	lastSpecNotified time.Time
}

// toolRef locates the owning API for a fully qualified (prefixed) tool name.
type toolRef struct {
	api  *apiEntry
	tool mcp.Tool // bare (unprefixed) tool
}

// APISummary is a client-safe view of a registered API and its targets. It never
// contains credentials.
type APISummary struct {
	Name          string   `json:"name"`
	Source        string   `json:"source,omitempty"`
	Title         string   `json:"title,omitempty"`
	SpecVersion   string   `json:"spec_version,omitempty"`
	SpecTimestamp string   `json:"spec_timestamp,omitempty"` // spec source last-modified at load (RFC3339)
	ToolCount     int      `json:"tool_count"`
	Tools         []string `json:"tools,omitempty"`
	ActiveTarget  string   `json:"active_target,omitempty"`
	Targets       []string `json:"targets,omitempty"`
	RegisteredAt  string   `json:"registered_at,omitempty"`
}

// Registry owns the set of registered APIs and their targets. It is safe for
// concurrent use. When configured with a persistPath, every mutation is
// atomically written back to the config file so registrations survive restarts.
type Registry struct {
	mu    sync.RWMutex
	apis  map[string]*apiEntry
	tools []mcp.Tool          // merged snapshot returned by tools/list
	index map[string]*toolRef // fully qualified tool name -> owning API/tool

	persistPath string
	server      config.ServerConfig
	// sessionTargets maps session (connection) id -> api name -> active target.
	// A per-session active target overrides the global one for that session only,
	// and is cleared when the session disconnects.
	sessionTargets map[string]map[string]string

	// Optional spec watcher (started on demand when an API opts into monitoring).
	monitorCtx    context.Context
	monitorCancel context.CancelFunc
	monitorWG     sync.WaitGroup
}

// NewRegistry creates an empty registry. If persistPath is non-empty, runtime
// registrations (APIs, targets, active target selection) are persisted there.
func NewRegistry(persistPath string) *Registry {
	r := &Registry{
		apis:           make(map[string]*apiEntry),
		index:          make(map[string]*toolRef),
		persistPath:    persistPath,
		sessionTargets: make(map[string]map[string]string),
	}
	// Seed the snapshot with the management tools so tools/list always surfaces
	// them, even when no API is registered yet.
	r.tools = append([]mcp.Tool{}, managementTools...)
	return r
}

// SetServerConfig records process-level server settings so they survive a
// config-file rewrite.
func (r *Registry) SetServerConfig(s config.ServerConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.server = s
}

// ServerConfig returns the configured server settings.
func (r *Registry) ServerConfig() config.ServerConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.server
}

// PersistencePath returns the config file path registrations are persisted to
// ("" when persistence is disabled).
func (r *Registry) PersistencePath() string {
	return r.persistPath
}

// --- Tool namespace helpers ---

func validateAPIName(name string) error {
	if name == "" {
		return nil
	}
	if !apiIDPattern.MatchString(name) {
		return fmt.Errorf("invalid API name %q: must start with an alphanumeric character and contain only letters, digits, '_' or '-'", name)
	}
	return nil
}

func toolFullName(apiName, toolName string) string {
	if apiName == "" {
		return toolName
	}
	return apiName + toolNameSep + toolName
}

// normalizeDefinition trims fields, defaults target names, validates the API's
// auth config, and drops an active_target that no longer refers to a registered
// target. def must be copied before calling when its content must not be
// mutated.
func (r *Registry) normalizeDefinition(def *config.APIDefinition) error {
	def.Name = strings.TrimSpace(def.Name)
	if err := validateAPIName(def.Name); err != nil {
		return err
	}
	def.Source = strings.TrimSpace(def.Source)
	def.ActiveTarget = strings.TrimSpace(def.ActiveTarget)
	def.Auth.LoginOperation = strings.TrimSpace(def.Auth.LoginOperation)
	def.Auth.TokenURL = strings.TrimSpace(def.Auth.TokenURL)

	// Automatic reload implies monitoring: there is nothing to auto-reload
	// without watching the source.
	if def.Monitoring.AutoReload && !def.Monitoring.Enabled {
		def.Monitoring.Enabled = true
	}

	if def.Auth.IsConfigured() {
		if _, err := config.ParseAPIKeyLocation(def.Auth.In); err != nil {
			return fmt.Errorf("API %q: invalid auth.in: %w", def.Name, err)
		}
		switch def.Auth.Type {
		case config.AuthAPIKey, config.AuthHTTP, config.AuthOAuth2, config.AuthOpenID, config.AuthCustomLogin:
		default:
			return fmt.Errorf("API %q: invalid auth.type %q", def.Name, def.Auth.Type)
		}
	}

	seen := make(map[string]bool, len(def.Targets))
	for i := range def.Targets {
		t := &def.Targets[i]
		t.Name = strings.TrimSpace(t.Name)
		if t.Name == "" {
			t.Name = "default"
		}
		if seen[t.Name] {
			return fmt.Errorf("API %q declares target %q more than once", def.Name, t.Name)
		}
		seen[t.Name] = true
	}
	if def.ActiveTarget != "" {
		if !seen[def.ActiveTarget] {
			def.ActiveTarget = ""
		}
	}
	// A single target is a natural default: select it so callers do not have to
	// pass an explicit 'target' argument. Callers can clear it later.
	if def.ActiveTarget == "" && len(def.Targets) == 1 {
		def.ActiveTarget = def.Targets[0].Name
	}
	return nil
}

// --- Snapshot & persistence ---

// rebuild computes the merged tools snapshot and the lookup index for a given
// set of APIs. It detects tool name collisions (between APIs and with the
// management tools). Callers must hold r.mu.
func (r *Registry) rebuild(apis map[string]*apiEntry) ([]mcp.Tool, map[string]*toolRef, error) {
	names := make([]string, 0, len(apis))
	for name := range apis {
		names = append(names, name)
	}
	sort.Strings(names)

	tools := make([]mcp.Tool, 0, len(managementTools)+64)
	tools = append(tools, managementTools...)
	index := make(map[string]*toolRef)
	used := make(map[string]string, len(managementTools))
	for _, mt := range managementTools {
		used[mt.Name] = "(management tool)"
	}

	for _, apiName := range names {
		api := apis[apiName]
		for _, tool := range api.ToolSet.Tools {
			fullName := toolFullName(apiName, tool.Name)
			if owner, exists := used[fullName]; exists {
				return nil, nil, fmt.Errorf("tool name collision: API %q operation %q maps to tool %q already exposed by %s", apiName, tool.Name, fullName, owner)
			}
			used[fullName] = fmt.Sprintf("API %q", apiName)
			index[fullName] = &toolRef{api: api, tool: tool}
			tools = append(tools, exposeTool(api, tool, fullName))
		}
	}
	return tools, index, nil
}

// exposeTool returns the client-facing copy of a tool for a given API. The tool
// name is fully qualified, its description is enriched with the underlying
// endpoint and input requirements (so an agent can use it from the tool surface
// alone), and, when the API has targets, a synthetic "target" input property is
// injected so callers can pick which server handles the call.
func exposeTool(api *apiEntry, tool mcp.Tool, fullName string) mcp.Tool {
	out := tool
	out.Name = fullName
	out.Description = buildToolDescription(api, tool, fullName)
	if len(api.Def.Targets) == 0 {
		return out
	}

	names := make([]interface{}, 0, len(api.Def.Targets))
	for _, t := range api.Def.Targets {
		names = append(names, t.Name)
	}

	desc := "Target (server) implementing this API to call. "
	desc += "Options: " + strings.Join(apiTargetNames(api), ", ") + ". "
	desc += "Defaults to the API's active target when omitted."

	schema := out.InputSchema
	props := make(map[string]mcp.Schema, len(schema.Properties)+1)
	for k, v := range schema.Properties {
		props[k] = v
	}
	props[targetArgName] = mcp.Schema{
		Type:        "string",
		Description: desc,
		Enum:        names,
	}

	required := append([]string{}, schema.Required...)
	if !apiHasActiveTarget(api) {
		required = append(required, targetArgName)
		sort.Strings(required)
	}

	out.InputSchema = mcp.Schema{
		Type:        schema.Type,
		Description: schema.Description,
		Properties:  props,
		Required:    required,
		Items:       schema.Items,
		Format:      schema.Format,
		Enum:        schema.Enum,
	}
	return out
}

// buildToolDescription produces a self-describing tool description that lets an
// AI agent understand the tool from the tool surface alone: the API, the
// underlying endpoint (method + path), the operation, and the input parameters
// (location, requirement, type). It always references the fully qualified tool
// name so endpoint docs and tools can be cross-mapped.
func buildToolDescription(api *apiEntry, tool mcp.Tool, fullName string) string {
	var b strings.Builder

	// Lead with any spec-provided summary/description.
	if tool.Description != "" {
		b.WriteString(tool.Description)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "API: %s. Valid tool name: %s.\n", quoteIfNeeded(api.Def.Name), fullName)

	// Underlying endpoint.
	loc := map[string]string{}
	op, ok := api.ToolSet.Operations[tool.Name]
	if !ok {
		b.WriteString("Endpoint: unknown.\n")
	} else {
		fmt.Fprintf(&b, "Endpoint: %s %s\n", op.Method, quoteIfNeeded(op.BaseURL+op.Path))
		for _, p := range op.Parameters {
			loc[p.Name] = p.In
		}
	}

	// Parameters and body from the input schema.
	reqSet := map[string]bool{}
	for _, r := range tool.InputSchema.Required {
		reqSet[r] = true
	}
	if len(tool.InputSchema.Properties) > 0 {
		b.WriteString("Input parameters:\n")
		for name, prop := range tool.InputSchema.Properties {
			in := loc[name]
			if in == "" {
				in = "body"
			}
			req := "optional"
			if reqSet[name] {
				req = "required"
			}
			fmt.Fprintf(&b, "  - %s (%s, %s): %s", name, in, req, prop.Type)
			if prop.Description != "" {
				b.WriteString(" — " + strings.TrimSuffix(prop.Description, "."))
			}
			if len(prop.Enum) > 0 {
				b.WriteString(" (one of: " + joinEnum(prop.Enum) + ")")
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// quoteIfNeeded wraps a value in double quotes when it is empty or contains
// special characters, to keep tool descriptions unambiguous.
func quoteIfNeeded(s string) string {
	if s == "" {
		return `""`
	}
	return `"` + s + `"`
}

func joinEnum(vals []interface{}) string {
	parts := make([]string, 0, len(vals))
	for _, v := range vals {
		parts = append(parts, fmt.Sprintf("%v", v))
	}
	return strings.Join(parts, ", ")
}

func apiTargetNames(api *apiEntry) []string {
	names := make([]string, 0, len(api.Def.Targets))
	for _, t := range api.Def.Targets {
		names = append(names, t.Name)
	}
	return names
}

func apiHasActiveTarget(api *apiEntry) bool {
	if api.Def.ActiveTarget == "" {
		return false
	}
	for _, t := range api.Def.Targets {
		if t.Name == api.Def.ActiveTarget {
			return true
		}
	}
	return false
}

// persist writes the current set of APIs to the config file. It is a no-op when
// persistence is disabled. Callers must hold r.mu.
func (r *Registry) persist(apis map[string]*apiEntry) error {
	if r.persistPath == "" {
		return nil
	}
	names := make([]string, 0, len(apis))
	for name := range apis {
		names = append(names, name)
	}
	sort.Strings(names)

	fc := &config.FileConfig{
		Server: r.server,
		APIs:   make([]config.APIDefinition, 0, len(names)),
	}
	for _, name := range names {
		fc.APIs = append(fc.APIs, apis[name].Def)
	}
	return config.SaveFile(r.persistPath, fc)
}

func (r *Registry) cloneAPIs() map[string]*apiEntry {
	out := make(map[string]*apiEntry, len(r.apis)+1)
	for k, v := range r.apis {
		out[k] = v
	}
	return out
}

func (r *Registry) notifyToolsListChanged() {
	broadcastToolsListChanged()
}

// --- Optional spec monitoring ---

// ensureMonitorRunningLocked starts the spec watcher goroutine when at least one
// registered API has monitoring enabled and the watcher is not already running.
// Callers must hold r.mu.
func (r *Registry) ensureMonitorRunningLocked() {
	if r.monitorCtx != nil {
		return
	}
	for _, entry := range r.apis {
		if !entry.Def.Monitoring.IsConfigured() {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		r.monitorCtx = ctx
		r.monitorCancel = cancel
		r.monitorWG.Add(1)
		go r.monitorLoop(ctx)
		regLog.Info("started spec watcher", "api", entry.Def.Name, "interval", monitorPollInterval.String())
		return
	}
}

// monitorLoop polls the registered APIs' spec sources until ctx is cancelled.
func (r *Registry) monitorLoop(ctx context.Context) {
	defer r.monitorWG.Done()
	ticker := time.NewTicker(monitorPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			regLog.Debug("spec watcher stopped")
			return
		case <-ticker.C:
			r.checkMonitoredAPIs(ctx)
		}
	}
}

// StopMonitoring stops the spec watcher goroutine (if running) and waits for it
// to exit. Safe to call multiple times; a no-op when monitoring was never
// started.
func (r *Registry) StopMonitoring() {
	r.mu.Lock()
	cancel := r.monitorCancel
	r.monitorCtx = nil
	r.monitorCancel = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
		r.monitorWG.Wait()
	}
}

// checkMonitoredAPIs scans every API with monitoring enabled and reacts when its
// spec source moved past the timestamp it was loaded at (and past the last mtime
// already acted upon): it notifies clients and, for auto_reload APIs, re-loads.
//
// Called by monitorLoop; also directly by tests.
func (r *Registry) checkMonitoredAPIs(ctx context.Context) {
	type monitored struct {
		name       string
		source     string
		autoReload bool
		loaded     time.Time
		lastSeen   time.Time
	}
	var targets []monitored
	r.mu.RLock()
	for _, entry := range r.apis {
		if !entry.Def.Monitoring.Enabled || entry.Def.Source == "" {
			continue // monitoring disabled, or nothing to watch (inline spec)
		}
		targets = append(targets, monitored{
			name:       entry.Def.Name,
			source:     entry.Def.Source,
			autoReload: entry.Def.Monitoring.AutoReload,
			loaded:     entry.SpecTimestamp,
			lastSeen:   entry.lastSpecNotified,
		})
	}
	r.mu.RUnlock()

	for _, t := range targets {
		if ctx.Err() != nil {
			return
		}
		current := parser.SpecSourceModified(t.source)
		if current.IsZero() {
			continue // source unreadable or carries no timestamp this tick
		}
		// React only when the source moved past both the load-time timestamp and
		// the last mtime we already reacted to (prevents per-poll re-notifies).
		guard := t.loaded
		if t.lastSeen.After(guard) {
			guard = t.lastSeen
		}
		if !current.After(guard) {
			continue
		}
		r.reactToSpecChange(t.name, t.source, t.autoReload, current)
	}
}

// reactToSpecChange notifies clients that an API's spec source changed and, when
// auto reload is enabled, re-loads the API. current is the source mtime that
// changed. Clients are notified first (logging channel), then the reload happens
// (which additionally broadcasts tools/list_changed).
func (r *Registry) reactToSpecChange(apiName, source string, autoReload bool, current time.Time) {
	regLog.Info("spec source changed", "api", apiName, "source", source, "mtime", current.UTC().Format(time.RFC3339))
	r.logSpecChanged(apiName, source, current, autoReload)

	// Record that this mtime was acted on so later polls do not re-fire.
	r.mu.Lock()
	if entry := r.apis[apiName]; entry != nil {
		entry.lastSpecNotified = current
	}
	r.mu.Unlock()

	if autoReload {
		if _, err := r.ReloadAPI(apiName); err != nil {
			regLog.Warn("auto-reload of API failed", "api", apiName, "error", err)
		}
	}
}

// logSpecChanged surfaces a monitoring event to clients through the standard MCP
// logging channel (a notifications/message at "notice" level). Clients that have
// opted into logging via logging/setLevel receive it and may present it to the
// user/agent.
func (r *Registry) logSpecChanged(apiName, source string, changedAt time.Time, autoReload bool) {
	broadcastLogMessage("notice", monitorLogger, map[string]interface{}{
		"api":         apiName,
		"source":      source,
		"changed_at":  changedAt.UTC().Format(time.RFC3339),
		"auto_reload": autoReload,
	})
}

// --- Spec loading ---

// loadToolSet parses an API definition into a ToolSet and reports the detected
// spec version. The resulting tools use bare operation names; namespacing and
// target injection happen when the snapshot is built.
func (r *Registry) loadToolSet(def config.APIDefinition) (*mcp.ToolSet, *mcp.ApiDoc, string, error) {
	var (
		specDoc interface{}
		version string
		err     error
	)
	switch {
	case def.Source != "":
		specDoc, version, err = parser.LoadSwagger(def.Source)
	case def.Spec != "":
		specDoc, version, err = parser.LoadSwaggerFromBytes([]byte(def.Spec), "inline spec for API "+def.Name)
	default:
		return nil, nil, "", fmt.Errorf("API %q requires either a 'source' (path/URL) or an inline 'spec'", def.Name)
	}
	if err != nil {
		return nil, nil, "", err
	}
	toolSet, err := parser.GenerateToolSet(specDoc, version, def.ToConfig())
	if err != nil {
		return nil, nil, "", fmt.Errorf("API %q: failed generating tools from spec: %w", def.Name, err)
	}
	if def.Source != "" && !strings.HasPrefix(def.Source, "http://") && !strings.HasPrefix(def.Source, "https://") {
		toolSet.BaseDir = filepath.Dir(def.Source)
	}
	if strings.HasPrefix(def.Source, "http://") || strings.HasPrefix(def.Source, "https://") {
		toolSet.BaseURL = def.Source
	}
	return toolSet, parser.BuildApiDoc(specDoc, version, toolSet), version, nil
}

// specTimestampFor returns the source last-modified time recorded for a spec
// (file mtime / HTTP Last-Modified), or the zero time when the source has no
// usable timestamp (inline spec, or probe failure). Zero is treated as
// "unknown"/untracked everywhere downstream.
func specTimestampFor(def config.APIDefinition) time.Time {
	if def.Source == "" {
		return time.Time{}
	}
	return parser.SpecSourceModified(def.Source)
}

func (r *Registry) newEntry(def config.APIDefinition) (*apiEntry, error) {
	toolSet, doc, version, err := r.loadToolSet(def)
	if err != nil {
		return nil, err
	}
	// Infer the API's auth from the spec's security schemes unless the caller
	// supplied an explicit auth config (including an explicit "none").
	if !def.Auth.IsConfigured() && len(toolSet.Security) > 0 {
		def.Auth = InferAuthConfig(toolSet.Security)
	}
	title := toolSet.Name
	if def.Source != "" {
		title = fmt.Sprintf("%s (%s)", toolSet.Name, def.Source)
	}
	return &apiEntry{
		Def:           def,
		SpecTitle:     title,
		SpecVersion:   version,
		RegisteredAt:  time.Now().UTC(),
		SpecTimestamp: specTimestampFor(def),
		ToolSet:       toolSet,
		Doc:           doc,
		loginTokens:   make(map[string]*tokenCacheEntry),
	}, nil
}

func apiSummary(api *apiEntry) APISummary {
	toolNames := make([]string, 0, len(api.ToolSet.Tools))
	for _, t := range api.ToolSet.Tools {
		toolNames = append(toolNames, toolFullName(api.Def.Name, t.Name))
	}
	specTimestamp := ""
	if !api.SpecTimestamp.IsZero() {
		specTimestamp = api.SpecTimestamp.UTC().Format(time.RFC3339)
	}
	return APISummary{
		Name:          api.Def.Name,
		Source:        api.Def.Source,
		Title:         api.SpecTitle,
		SpecVersion:   api.SpecVersion,
		SpecTimestamp: specTimestamp,
		ToolCount:     len(toolNames),
		Tools:         toolNames,
		ActiveTarget:  api.Def.ActiveTarget,
		Targets:       apiTargetNames(api),
		RegisteredAt:  api.RegisteredAt.Format(time.RFC3339),
	}
}

// --- API lifecycle (exported) ---

// RegisterAPI loads and registers an API from its definition. When replace is
// true and an API with the same name exists, it is replaced. Mutations are
// persisted to the config file when persistence is enabled.
func (r *Registry) RegisterAPI(def config.APIDefinition, replace bool) (*APISummary, error) {
	if err := r.normalizeDefinition(&def); err != nil {
		return nil, err
	}
	entry, err := r.newEntry(def)
	if err != nil {
		return nil, err
	}
	return r.registerEntry(entry, replace)
}

// registerParsedAPI registers an API from an already-parsed ToolSet (used by
// tests and internal callers that bypass spec loading).
func (r *Registry) registerParsedAPI(def config.APIDefinition, toolSet *mcp.ToolSet, version string, replace bool) (*APISummary, error) {
	if toolSet == nil {
		return nil, fmt.Errorf("toolSet is required")
	}
	if err := r.normalizeDefinition(&def); err != nil {
		return nil, err
	}
	if !def.Auth.IsConfigured() && len(toolSet.Security) > 0 {
		def.Auth = InferAuthConfig(toolSet.Security)
	}
	title := toolSet.Name
	if def.Source != "" {
		title = fmt.Sprintf("%s (%s)", toolSet.Name, def.Source)
	}
	entry := &apiEntry{
		Def:           def,
		SpecTitle:     title,
		SpecVersion:   version,
		RegisteredAt:  time.Now().UTC(),
		SpecTimestamp: specTimestampFor(def),
		ToolSet:       toolSet,
		Doc:           parser.BuildApiDoc(nil, version, toolSet),
		loginTokens:   make(map[string]*tokenCacheEntry),
	}
	return r.registerEntry(entry, replace)
}

func (r *Registry) registerEntry(entry *apiEntry, replace bool) (*APISummary, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.apis[entry.Def.Name]; exists && !replace {
		return nil, fmt.Errorf("API %q is already registered (unregister it first, or register with update=true)", entry.Def.Name)
	}
	if replace {
		if prev, exists := r.apis[entry.Def.Name]; exists {
			if len(entry.Def.Targets) == 0 {
				entry.Def.Targets = prev.Def.Targets
			} else {
				byName := map[string]config.TargetDefinition{}
				for _, t := range entry.Def.Targets {
					byName[t.Name] = t
				}
				for _, t := range prev.Def.Targets {
					if _, ok := byName[t.Name]; !ok {
						entry.Def.Targets = append(entry.Def.Targets, t)
					}
				}
			}
			if entry.Def.ActiveTarget == "" && prev.Def.ActiveTarget != "" {
				if _, ok := entry.Def.Target(prev.Def.ActiveTarget); ok {
					entry.Def.ActiveTarget = prev.Def.ActiveTarget
				}
			}
			if err := r.normalizeDefinition(&entry.Def); err != nil {
				return nil, err
			}
		}
	}
	apis := r.cloneAPIs()
	apis[entry.Def.Name] = entry
	tools, index, err := r.rebuild(apis)
	if err != nil {
		return nil, err
	}
	if err := r.persist(apis); err != nil {
		return nil, err
	}
	r.apis, r.tools, r.index = apis, tools, index
	r.ensureMonitorRunningLocked()
	summary := apiSummary(entry)
	r.notifyToolsListChanged()
	return &summary, nil
}

// UnregisterAPI removes an API (and all its targets).
func (r *Registry) UnregisterAPI(name string) (*APISummary, error) {
	name = strings.TrimSpace(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, exists := r.apis[name]
	if !exists {
		return nil, fmt.Errorf("API %q is not registered", name)
	}
	summary := apiSummary(entry)

	apis := r.cloneAPIs()
	delete(apis, name)
	tools, index, err := r.rebuild(apis)
	if err != nil {
		return nil, err
	}
	if err := r.persist(apis); err != nil {
		return nil, err
	}
	r.apis, r.tools, r.index = apis, tools, index
	r.notifyToolsListChanged()
	return &summary, nil
}

// APIs lists the registered APIs, sorted by name.
func (r *Registry) APIs() []APISummary {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.apis))
	for name := range r.apis {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]APISummary, 0, len(names))
	for _, name := range names {
		out = append(out, apiSummary(r.apis[name]))
	}
	return out
}

// GetAPI returns the summary of a single API.
func (r *Registry) GetAPI(name string) (APISummary, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.apis[name]
	if !ok {
		return APISummary{}, false
	}
	return apiSummary(entry), true
}

// apiEntryView is a read-only snapshot of an apiEntry used by the
// introspection management tools. It deliberately omits the entry's mutex and
// token cache so it can be handed out safely.
type apiEntryView struct {
	Def           config.APIDefinition
	SpecVersion   string
	RegisteredAt  time.Time
	SpecTimestamp time.Time
	ToolSet       *mcp.ToolSet
	Doc           *mcp.ApiDoc

	// loginTokens mirror target->token expiry (for introspection; never secrets).
	loginTokens map[string]time.Time
}

// GetApiEntryView returns a read-only snapshot of an API for introspection.
func (r *Registry) GetApiEntryView(name string) (*apiEntryView, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.apis[name]
	if !ok {
		return nil, fmt.Errorf("API %q is not registered", name)
	}
	view := &apiEntryView{
		Def:           entry.Def,
		SpecVersion:   entry.SpecVersion,
		RegisteredAt:  entry.RegisteredAt,
		SpecTimestamp: entry.SpecTimestamp,
		ToolSet:       entry.ToolSet,
		Doc:           entry.Doc,
	}
	// Return a shallow copy of the token cache snapshot (nil-safe copied under lock).
	view.loginTokens = make(map[string]time.Time, len(entry.loginTokens))
	for k, v := range entry.loginTokens {
		view.loginTokens[k] = v.expiresAt
	}
	return view, nil
}

// Relogin discards the cached session token for an API's target so the next
// call re-authenticates.
func (r *Registry) Relogin(apiName, targetName string) error {
	apiName = strings.TrimSpace(apiName)
	targetName = strings.TrimSpace(targetName)
	r.mu.RLock()
	entry, ok := r.apis[apiName]
	if !ok {
		r.mu.RUnlock()
		return fmt.Errorf("API %q is not registered", apiName)
	}
	if _, ok := entry.Def.Target(targetName); !ok {
		r.mu.RUnlock()
		return fmt.Errorf("API %q has no target named %q", apiName, targetName)
	}
	r.mu.RUnlock()

	entry.tokenMu.Lock()
	defer entry.tokenMu.Unlock()
	delete(entry.loginTokens, targetName)
	return nil
}

// ReloadFromConfig re-registers the APIs present in the config file, applying
// source/auth/target changes. APIs that were registered but are no longer in
// the file are left untouched. Server settings are refreshed too.
func (r *Registry) ReloadFromConfig(path string) ([]string, error) {
	fc, err := config.LoadFile(path)
	if err != nil {
		return nil, err
	}
	r.SetServerConfig(fc.Server)
	var messages []string
	for i := range fc.APIs {
		def := fc.APIs[i]
		if def.Name == "" {
			continue
		}
		if _, exists := r.GetAPI(def.Name); exists {
			if _, err := r.RegisterAPI(def, true); err != nil {
				messages = append(messages, fmt.Sprintf("updated %q: %v", def.Name, err))
			} else {
				messages = append(messages, fmt.Sprintf("updated %q", def.Name))
			}
		} else {
			if _, err := r.RegisterAPI(def, false); err != nil {
				messages = append(messages, fmt.Sprintf("registered %q failed: %v", def.Name, err))
			} else {
				messages = append(messages, fmt.Sprintf("registered %q", def.Name))
			}
		}
	}
	return messages, nil
}

// SpecStatus values returned by CheckSpecState and ReloadAPI. They describe how
// the in-memory toolset relates to its spec source.
const (
	// SpecStatusUpToDate: the spec source has not changed since it was loaded.
	SpecStatusUpToDate = "up-to-date"
	// SpecStatusOutdated: the spec source was modified after the API was loaded.
	SpecStatusOutdated = "outdated"
	// SpecStatusUnknown: no timestamp can be determined (inline spec, or a
	// source without a usable mtime/Last-Modified), so freshness is unknown.
	SpecStatusUnknown = "unknown"
)

// CheckSpecState reports the freshness of an API's loaded spec: the timestamp
// recorded when the spec was loaded, the current source last-modified time, and
// a status ("up-to-date", "outdated" or "unknown"). Errors when the API is not
// registered.
func (r *Registry) CheckSpecState(apiName string) (loadedAt, current time.Time, status string, err error) {
	apiName = strings.TrimSpace(apiName)
	r.mu.RLock()
	entry, ok := r.apis[apiName]
	r.mu.RUnlock()
	if !ok {
		return time.Time{}, time.Time{}, "", fmt.Errorf("API %q is not registered", apiName)
	}
	loadedAt = entry.SpecTimestamp
	current = parser.SpecSourceModified(entry.Def.Source)
	return loadedAt, current, specStatus(loadedAt, current), nil
}

// specStatus derives a freshness status from a pair of timestamps.
func specStatus(loadedAt, current time.Time) string {
	if loadedAt.IsZero() || current.IsZero() {
		return SpecStatusUnknown
	}
	if current.After(loadedAt) {
		return SpecStatusOutdated
	}
	return SpecStatusUpToDate
}

// APIReloadResult describes the outcome of ReloadAPI.
type APIReloadResult struct {
	// Name of the reloaded API.
	Name string
	// Source the spec was reloaded from.
	Source string
	// ToolCount after reload.
	ToolCount int
	// Status of the *previous* load relative to the spec source at reload time:
	// "up-to-date", "outdated" or "unknown".
	Status string
}

// ReloadAPI reloads an API from scratch: the spec is re-read from its source
// (file path, URL or inline) and the tools are regenerated, replacing the
// previous entry. The API's targets and active target are preserved. It reports
// whether the previous load was outdated relative to the spec source, so callers
// know when the reload actually picked up spec changes.
func (r *Registry) ReloadAPI(apiName string) (*APIReloadResult, error) {
	apiName = strings.TrimSpace(apiName)
	r.mu.RLock()
	entry, ok := r.apis[apiName]
	if !ok {
		r.mu.RUnlock()
		return nil, fmt.Errorf("API %q is not registered", apiName)
	}
	def := entry.Def
	status := specStatus(entry.SpecTimestamp, parser.SpecSourceModified(def.Source))
	r.mu.RUnlock()

	summary, err := r.RegisterAPI(def, true)
	if err != nil {
		return nil, fmt.Errorf("API %q reload failed: %w", apiName, err)
	}
	return &APIReloadResult{
		Name:      apiName,
		Source:    def.Source,
		ToolCount: summary.ToolCount,
		Status:    status,
	}, nil
}

// Tools returns the merged tool list served by tools/list (management tools
// first, then each API's tools sorted by API name).
func (r *Registry) Tools() []mcp.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]mcp.Tool, len(r.tools))
	copy(out, r.tools)
	return out
}

// ResolveTool maps a fully qualified tool name to its owning API entry and bare
// tool. It returns false for unknown names and management tools.
func (r *Registry) ResolveTool(fullName string) (*apiEntry, mcp.Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ref, ok := r.index[fullName]
	if !ok {
		return nil, mcp.Tool{}, false
	}
	return ref.api, ref.tool, true
}

// IsManagementTool reports whether name is one of the registry management tools.
func (r *Registry) IsManagementTool(name string) bool {
	_, ok := managementToolByName(name)
	return ok
}

// --- Target lifecycle (exported) ---

// AddTarget registers a target on an existing API. When replace is true an
// existing target with the same name is replaced.
func (r *Registry) AddTarget(apiName string, target config.TargetDefinition, replace bool) (*APISummary, error) {
	apiName = strings.TrimSpace(apiName)
	target.Name = strings.TrimSpace(target.Name)
	if target.Name == "" {
		target.Name = "default"
	}
	if err := validateAPIName(apiName); err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	entry, exists := r.apis[apiName]
	if !exists {
		return nil, fmt.Errorf("API %q is not registered", apiName)
	}
	def := entry.Def
	for i := range def.Targets {
		if def.Targets[i].Name == target.Name {
			if !replace {
				return nil, fmt.Errorf("API %q already has a target named %q (unregister it first, or add with update=true)", apiName, target.Name)
			}
			def.Targets[i] = target
			entry.Def = def
			summary := apiSummary(entry)
			if err := r.commitStateLocked(); err != nil {
				return nil, err
			}
			r.notifyToolsListChanged()
			return &summary, nil
		}
	}
	def.Targets = append(def.Targets, target)
	entry.Def = def
	// The first target of an API becomes its active (default) target.
	if len(def.Targets) == 1 {
		entry.Def.ActiveTarget = def.Targets[0].Name
	}
	summary := apiSummary(entry)
	if err := r.commitStateLocked(); err != nil {
		return nil, err
	}
	r.notifyToolsListChanged()
	return &summary, nil
}

// RemoveTarget removes a target from an API.
func (r *Registry) RemoveTarget(apiName, targetName string) (*APISummary, error) {
	apiName = strings.TrimSpace(apiName)
	targetName = strings.TrimSpace(targetName)

	r.mu.Lock()
	defer r.mu.Unlock()
	entry, exists := r.apis[apiName]
	if !exists {
		return nil, fmt.Errorf("API %q is not registered", apiName)
	}
	def := entry.Def
	idx := -1
	for i := range def.Targets {
		if def.Targets[i].Name == targetName {
			idx = i
			break
		}
	}
	if idx == -1 {
		return nil, fmt.Errorf("API %q has no target named %q", apiName, targetName)
	}
	def.Targets = append(def.Targets[:idx], def.Targets[idx+1:]...)
	if def.ActiveTarget == targetName {
		def.ActiveTarget = ""
	}
	entry.Def = def
	summary := apiSummary(entry)
	if err := r.commitStateLocked(); err != nil {
		return nil, err
	}
	r.notifyToolsListChanged()
	return &summary, nil
}

// SetActiveTarget selects the active (default) target for an API.
func (r *Registry) SetActiveTarget(apiName, targetName string) (*APISummary, error) {
	apiName = strings.TrimSpace(apiName)
	targetName = strings.TrimSpace(targetName)

	r.mu.Lock()
	defer r.mu.Unlock()
	entry, exists := r.apis[apiName]
	if !exists {
		return nil, fmt.Errorf("API %q is not registered", apiName)
	}
	if _, ok := entry.Def.Target(targetName); !ok {
		return nil, fmt.Errorf("API %q has no target named %q", apiName, targetName)
	}
	entry.Def.ActiveTarget = targetName
	summary := apiSummary(entry)
	if err := r.commitStateLocked(); err != nil {
		return nil, err
	}
	r.notifyToolsListChanged()
	return &summary, nil
}

// ClearActiveTarget unsets the active target for an API.
func (r *Registry) ClearActiveTarget(apiName string) (*APISummary, error) {
	apiName = strings.TrimSpace(apiName)

	r.mu.Lock()
	defer r.mu.Unlock()
	entry, exists := r.apis[apiName]
	if !exists {
		return nil, fmt.Errorf("API %q is not registered", apiName)
	}
	if entry.Def.ActiveTarget == "" {
		return nil, fmt.Errorf("API %q has no active target set", apiName)
	}
	entry.Def.ActiveTarget = ""
	summary := apiSummary(entry)
	if err := r.commitStateLocked(); err != nil {
		return nil, err
	}
	r.notifyToolsListChanged()
	return &summary, nil
}

// GetActiveTarget returns the name of the active target of an API ("" when
// none is set).
func (r *Registry) GetActiveTarget(apiName string) (string, error) {
	apiName = strings.TrimSpace(apiName)
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, exists := r.apis[apiName]
	if !exists {
		return "", fmt.Errorf("API %q is not registered", apiName)
	}
	return entry.Def.ActiveTarget, nil
}

// commitStateLocked recomputes the snapshot/index and persists after the current
// API definitions changed (target ops mutate entries in place). Callers must
// hold r.mu.
func (r *Registry) commitStateLocked() error {
	tools, index, err := r.rebuild(r.apis)
	if err != nil {
		return err
	}
	if err := r.persist(r.apis); err != nil {
		return err
	}
	r.tools, r.index = tools, index
	r.ensureMonitorRunningLocked()
	return nil
}

// --- Call routing ---

// prepareCallArgs resolves which target handles a tool call for an API, strips
// the synthetic "target" argument so it is never forwarded upstream, and
// returns the resolved target definition plus the target's request Config.
// SetSessionActiveTarget selects an active target for a single session
// (connection). It overrides the global active target for that session only and
// is removed when the session disconnects.
func (r *Registry) SetSessionActiveTarget(connID, apiName, targetName string) error {
	if connID == "" {
		return fmt.Errorf("session id is required")
	}
	apiName = strings.TrimSpace(apiName)
	targetName = strings.TrimSpace(targetName)
	r.mu.RLock()
	entry, ok := r.apis[apiName]
	if !ok {
		r.mu.RUnlock()
		return fmt.Errorf("API %q is not registered", apiName)
	}
	if _, ok := entry.Def.Target(targetName); !ok {
		r.mu.RUnlock()
		return fmt.Errorf("API %q has no target named %q", apiName, targetName)
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessionTargets[connID] == nil {
		r.sessionTargets[connID] = map[string]string{}
	}
	r.sessionTargets[connID][apiName] = targetName
	return nil
}

// ClearSessionActiveTarget removes the per-session active target for an API.
func (r *Registry) ClearSessionActiveTarget(connID, apiName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.sessionTargets[connID]; ok {
		delete(m, apiName)
	}
	return nil
}

// GetSessionActiveTarget returns the per-session active target of an API ("" if
// none set for this session).
func (r *Registry) GetSessionActiveTarget(connID, apiName string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if m, ok := r.sessionTargets[connID]; ok {
		return m[apiName]
	}
	return ""
}

// DropSession removes all per-session state for a connection (called when the
// session disconnects).
func (r *Registry) DropSession(connID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessionTargets, connID)
}

// prepareCallArgsFor resolves the target for a call within a session,
// consulting the per-session active target override first.
func (r *Registry) prepareCallArgsFor(connID string, api *apiEntry, args map[string]interface{}) (map[string]interface{}, config.TargetDefinition, *config.Config, error) {
	args = cloneArgs(args)
	if connID != "" {
		if sessTarget := r.GetSessionActiveTarget(connID, api.Def.Name); sessTarget != "" {
			if _, has := args[targetArgName]; !has {
				args[targetArgName] = sessTarget
			}
		}
	}
	return r.prepareCallArgs(api, args)
}

func cloneArgs(args map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(args)+1)
	for k, v := range args {
		out[k] = v
	}
	return out
}

func (r *Registry) prepareCallArgs(api *apiEntry, args map[string]interface{}) (map[string]interface{}, config.TargetDefinition, *config.Config, error) {
	if len(api.Def.Targets) == 0 {
		return args, config.TargetDefinition{}, &config.Config{}, fmt.Errorf("API %q has no registered targets: register one with register_api_target and set it active with set_active_api_target before calling its tools", api.Def.Name)
	}

	clean := make(map[string]interface{}, len(args))
	for k, v := range args {
		if k != targetArgName {
			clean[k] = v
		}
	}

	selected := ""
	if v, ok := args[targetArgName]; ok {
		if s, isStr := v.(string); isStr {
			selected = strings.TrimSpace(s)
		}
	}
	if selected == "" {
		selected = api.Def.ActiveTarget
	}

	target, ok := api.Def.Target(selected)
	if !ok {
		available := strings.Join(apiTargetNames(api), ", ")
		return nil, config.TargetDefinition{}, nil, fmt.Errorf("API %q: unknown or missing target. No active target is set; pass one of the registered targets as the 'target' argument (%s), or select a default with set_active_api_target", api.Def.Name, available)
	}
	return clean, target, target.ToConfig(), nil
}
