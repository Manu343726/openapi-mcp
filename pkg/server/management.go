package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/ckanthony/openapi-mcp/pkg/logx"
	"github.com/ckanthony/openapi-mcp/pkg/mcp"
)

// Management tool names. These are always exposed (unprefixed) alongside the
// per-API tools so MCP clients can configure the registry at runtime.
const (
	// API management.
	ToolRegisterAPI   = "register_openapi_api"
	ToolUnregisterAPI = "unregister_openapi_api"
	ToolListAPIs      = "list_openapi_apis"

	// API introspection / documentation.
	ToolDescribeAPI     = "describe_openapi_api"
	ToolGetOperation    = "get_api_operation"
	ToolListSchemas     = "list_api_schemas"
	ToolSearchOperation = "search_openapi_operations"
	ToolExportConfig    = "export_openapi_config"

	// Ops / maintenance.
	ToolRelogin      = "relogin"
	ToolTestTarget   = "test_api_target"
	ToolReloadConfig = "reload_config"
	ToolReloadAPI    = "reload_api"
	ToolCheckSpec    = "check_api_spec"
	ToolSetLogLevel  = "set_log_level"
	ToolPreviewCall  = "preview_api_call"

	// Target management.
	ToolAddTarget          = "register_api_target"
	ToolRemoveTarget       = "unregister_api_target"
	ToolListTargets        = "list_api_targets"
	ToolSetActiveTarget    = "set_active_api_target"
	ToolClearActiveTarget  = "clear_active_api_target"
	ToolGetActiveTarget    = "get_active_api_target"
	ToolSetSessionTarget   = "set_session_active_api_target"
	ToolClearSessionTarget = "clear_session_active_api_target"
	ToolGetSessionTarget   = "get_session_active_api_target"
)

// managementTools is the fixed set of management tools appended to every
// tools/list response.
var managementTools = buildManagementTools()

func managementToolByName(name string) (mcp.Tool, bool) {
	for _, t := range managementTools {
		if t.Name == name {
			return t, true
		}
	}
	return mcp.Tool{}, false
}

func buildManagementTools() []mcp.Tool {
	return []mcp.Tool{
		{
			Name:        ToolRegisterAPI,
			Description: "Register an OpenAPI/Swagger API (v2 or v3) so its operations become callable MCP tools. Provide either a 'source' (http(s) URL or file path) or an inline JSON 'spec'. The API name prefixes all generated tool names (<name>__<operationId>). Use register_api_target afterwards to add the servers that implement it.",
			InputSchema: registerAPISchema(),
		},
		{
			Name:        ToolUnregisterAPI,
			Description: "Remove a previously registered API (and all its targets). Existing tool calls against it will fail until re-registered.",
			InputSchema: nameOnlySchema("name", "Name of the API to remove"),
		},
		{
			Name:        ToolListAPIs,
			Description: "List every registered API, including its target names, active target and exposed tools.",
			InputSchema: emptySchema(),
		},
		{
			Name:        ToolAddTarget,
			Description: "Register a target (a concrete server implementing an already-registered API) for an API. Connection and authentication data live on the target; targets are never exposed to clients beyond their name.",
			InputSchema: registerTargetSchema(),
		},
		{
			Name:        ToolRemoveTarget,
			Description: "Remove a target from an API. The active target is cleared if it pointed to the removed target.",
			InputSchema: apiAndTargetSchema(),
		},
		{
			Name:        ToolListTargets,
			Description: "List the registered targets of an API.",
			InputSchema: nameOnlySchema("api", "Name of the registered API"),
		},
		{
			Name:        ToolSetActiveTarget,
			Description: "Select the active (default) target of an API. While an active target is set, calls to the API's tools are routed to it unless a 'target' argument is passed explicitly.",
			InputSchema: apiAndTargetSchema(),
		},
		{
			Name:        ToolClearActiveTarget,
			Description: "Clear the active target of an API. Afterwards, callers must pass an explicit 'target' argument on every tool call of that API.",
			InputSchema: nameOnlySchema("api", "Name of the registered API"),
		},
		{
			Name:        ToolGetActiveTarget,
			Description: "Return the active target of an API (or none).",
			InputSchema: nameOnlySchema("api", "Name of the registered API"),
		},
		{
			Name:        ToolSetSessionTarget,
			Description: "Select the active target of an API for THIS session/connection only. It overrides the global active target and is cleared automatically when the session disconnects. Other sessions are unaffected.",
			InputSchema: apiAndTargetSchema(),
		},
		{
			Name:        ToolClearSessionTarget,
			Description: "Clear this session's active-target override for an API, reverting to the global active target.",
			InputSchema: nameOnlySchema("api", "Name of the registered API"),
		},
		{
			Name:        ToolGetSessionTarget,
			Description: "Return this session's active-target override for an API (or none).",
			InputSchema: nameOnlySchema("api", "Name of the registered API"),
		},
		{
			Name:        ToolDescribeAPI,
			Description: "Return the documentation of a registered API: info (title, version, description), servers, tags, endpoints (method/path/summary + MCP tool name), DTO schemas, auth + target config. Use `include` and `search` to limit the payload size, and `schema_detail` to control how much schema info is returned.",
			InputSchema: mcp.Schema{
				Type: "object",
				Properties: map[string]mcp.Schema{
					"api":           {Type: "string", Description: "Name of the registered API"},
					"include":       stringListProp("Only include these sections: info, endpoints, schemas, auth, targets (default all)"),
					"search":        {Type: "string", Description: "Filter endpoints by substring match on operationId/path/summary"},
					"schema_detail": {Type: "string", Enum: []interface{}{"full", "compact"}, Description: "Schema detail level (default compact: names only; full: expanded)"},
				},
				Required: []string{"api"},
			},
		},
		{
			Name:        ToolGetOperation,
			Description: "Return detailed documentation for a single API operation/endpoint by its operationId (or full MCP tool name <api>__<op>): HTTP method/path, parameters (location, required, type), request body schema, response schemas, the exact MCP tool to call, and (optionally) example payloads from the spec.",
			InputSchema: mcp.Schema{
				Type: "object",
				Properties: map[string]mcp.Schema{
					"api":       {Type: "string", Description: "Name of the registered API"},
					"operation": {Type: "string", Description: "operationId or full MCP tool name (<api>__<op>) to inspect"},
					"examples":  {Type: "boolean", Description: "Include example payloads from the spec where present (default true)"},
				},
				Required: []string{"api", "operation"},
			},
		},
		{
			Name:        ToolListSchemas,
			Description: "List the data schemas/DTOs defined by a registered API's OpenAPI spec (components.schemas / definitions), optionally expanded, filtered by search, and paginated. Use this to understand the request/response shapes the API works with.",
			InputSchema: mcp.Schema{
				Type: "object",
				Properties: map[string]mcp.Schema{
					"api":    {Type: "string", Description: "Name of the registered API"},
					"expand": {Type: "boolean", Description: "Expand each schema with its full property tree (default false: names + types only)"},
					"name":   {Type: "string", Description: "Only return this specific schema by name (optional)"},
					"search": {Type: "string", Description: "Filter schema names by substring match"},
					"limit":  {Type: "integer", Description: "Max schemas to return (default 100)"},
					"offset": {Type: "integer", Description: "Skipped schemas for pagination (default 0)"},
				},
				Required: []string{"api"},
			},
		},
		{
			Name:        ToolSearchOperation,
			Description: "Search a registered API's endpoints (operations) by keyword matching operationId, path or summary. Returns the matching operation + its MCP tool name so you can then call get_api_operation for details.",
			InputSchema: mcp.Schema{
				Type: "object",
				Properties: map[string]mcp.Schema{
					"api":    {Type: "string", Description: "Name of the registered API"},
					"query":  {Type: "string", Description: "Keyword to match against operationId, path or summary"},
					"method": {Type: "string", Enum: []interface{}{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}, Description: "Optional HTTP method filter"},
				},
				Required: []string{"api", "query"},
			},
		},
		{
			Name:        ToolExportConfig,
			Description: "Return the effective, persisted configuration of a registered API: source/spec, auth (including inferred values), target list with active target, and filters. Read-only; no keys exposed.",
			InputSchema: nameOnlySchema("api", "Name of the registered API"),
		},
		{
			Name:        ToolRelogin,
			Description: "Force a re-authentication for an API's target: discard the cached session token/login so the next call re-logs-in. Useful after rotating credentials.",
			InputSchema: apiAndTargetSchema(),
		},
		{
			Name:        ToolTestTarget,
			Description: "Probe an API's target for reachability without executing a data call: performs a minimal connectivity check (e.g. issuing a login when the API uses one, or an OPTIONS/GET on the base). Reports reachability and credentials validity.",
			InputSchema: apiAndTargetSchema(),
		},
		{
			Name:        ToolReloadConfig,
			Description: "Reload the persisted config file and (re)register the APIs defined there, applying any source/auth/target changes. Existing registrations not present in the file are left untouched.",
			InputSchema: emptySchema(),
		},
		{
			Name:        ToolReloadAPI,
			Description: "Reload an API from scratch: re-read its spec from the source (path/URL/inline) and regenerate its tools, preserving targets and the active target. Reports whether the previous load was outdated relative to the spec source (e.g. the spec file was modified after the API was registered).",
			InputSchema: nameOnlySchema("api", "Name of the registered API to reload"),
		},
		{
			Name:        ToolCheckSpec,
			Description: "Check whether a registered API's loaded spec is stale: compares the spec-source timestamp recorded at load time against the current source mtime/Last-Modified, and reports up-to-date / outdated / unknown. Use reload_api to re-read a spec that changed.",
			InputSchema: nameOnlySchema("api", "Name of the registered API"),
		},
		{
			Name:        ToolSetLogLevel,
			Description: "Change the server's minimum log level on the fly (no restart): debug, info, warn or error. Takes effect immediately and is recorded in the config file (server.log_level) when persistence is enabled.",
			InputSchema: nameOnlySchema("level", "Minimum log level to emit (debug, info, warn or error)"),
		},
		{
			Name:        ToolPreviewCall,
			Description: "Dry-run: build the literal HTTP request an MCP tool call would send (method, URL, headers, body, resolved target) WITHOUT executing it. Use to inspect exactly what will be sent before calling.",
			InputSchema: mcp.Schema{
				Type: "object",
				Properties: map[string]mcp.Schema{
					"operation": {Type: "string", Description: "Full MCP tool name (<api>__<op>) to preview"},
					"arguments": mcp.Schema{
						Type:        "object",
						Description: "Input arguments exactly as they would be passed to the tool (map of param name to value)",
						Properties:  map[string]mcp.Schema{},
					},
				},
				Required: []string{"operation", "arguments"},
			},
		},
	}
}

func emptySchema() mcp.Schema {
	return mcp.Schema{Type: "object", Properties: map[string]mcp.Schema{}}
}

func nameOnlySchema(key, description string) mcp.Schema {
	return mcp.Schema{
		Type: "object",
		Properties: map[string]mcp.Schema{
			key: {Type: "string", Description: description},
		},
		Required: []string{key},
	}
}

func apiAndTargetSchema() mcp.Schema {
	return mcp.Schema{
		Type: "object",
		Properties: map[string]mcp.Schema{
			"api":    {Type: "string", Description: "Name of the registered API"},
			"target": {Type: "string", Description: "Name of the target within that API"},
		},
		Required: []string{"api", "target"},
	}
}

func stringProp(description string) mcp.Schema {
	return mcp.Schema{Type: "string", Description: description}
}

func stringListProp(description string) mcp.Schema {
	return mcp.Schema{
		Type:        "array",
		Description: description,
		Items:       &mcp.Schema{Type: "string"},
	}
}

func registerAPISchema() mcp.Schema {
	return mcp.Schema{
		Type: "object",
		Properties: map[string]mcp.Schema{
			"name":   {Type: "string", Description: "Unique API name; prefixes generated tools as <name>__<operationId>. Required."},
			"source": {Type: "string", Description: "Path or http(s) URL of the OpenAPI/Swagger spec. Provide either source or spec."},
			"spec":   {Type: "string", Description: "Inline OpenAPI/Swagger 2.0 or 3.x JSON document. Provide either source or spec."},
			// Authentication is inferred from the spec's security schemes when
			// possible and can be overridden with the auth_* fields below.
			"auth_type":              {Type: "string", Enum: []interface{}{"apiKey", "http", "oauth2", "openIdConnect", "custom"}, Description: "Authentication scheme type. Inferred from the spec's security schemes when omitted."},
			"auth_in":                {Type: "string", Enum: []interface{}{"header", "query", "cookie"}, Description: "Where the credential/token is attached (default header)"},
			"auth_name":              stringProp("Name of the header/query/cookie parameter carrying the credential or token (default 'Authorization')"),
			"auth_prefix":            stringProp("Prefix prepended to the credential/token value (e.g. 'Bearer ', 'Basic ')"),
			"auth_http_scheme":       {Type: "string", Enum: []interface{}{"basic", "bearer", "digest"}, Description: "HTTP auth scheme when auth_type=http"},
			"auth_flow":              {Type: "string", Enum: []interface{}{"password", "clientCredentials", "authorizationCode", "implicit"}, Description: "OAuth2 flow when auth_type=oauth2"},
			"auth_token_url":         stringProp("OAuth2 token endpoint (when auth_type=oauth2 and a flow is used)"),
			"auth_login_operation":   stringProp("operationId (or full <api>__<op> name) of the API's login operation for login/token based auth. Inferred automatically when omitted."),
			"monitoring_enabled":     {Type: "boolean", Description: "Watch the API's spec source for changes and notify clients when it changes (default false)"},
			"monitoring_auto_reload": {Type: "boolean", Description: "On spec change, also re-load the API and regenerate its tools (implies monitoring; default false)"},
			"include_tags":           stringListProp("Only expose operations with these tags"),
			"exclude_tags":           stringListProp("Exclude operations with these tags"),
			"include_ops":            stringListProp("Only expose these operation ids"),
			"exclude_ops":            stringListProp("Exclude these operation ids"),
			"update":                 {Type: "boolean", Description: "Replace an existing registration with the same name (default false)"},
		},
		Required: []string{"name"},
	}
}

func registerTargetSchema() mcp.Schema {
	return mcp.Schema{
		Type: "object",
		Properties: map[string]mcp.Schema{
			"api":      stringProp("Name of the registered API the target implements"),
			"name":     stringProp("Target name, unique within the API (defaults to 'default')"),
			"base_url": stringProp("Base URL the target API is served at (falls back to the spec's server declarations when empty)"),
			// Credential values. Placement (header/query/cookie + name) is
			// governed by the API's auth config, not by the target.
			"api_key_env":        stringProp("Name of an environment variable on the MCP server holding the API key"),
			"api_key":            stringProp("Literal API key (prefer api_key_env so secrets stay off the wire)"),
			"login_username":     stringProp("Username used to authenticate via the API's login/OAuth flow"),
			"login_password":     stringProp("Password used to authenticate via the API's login/OAuth flow (use login_password_env when possible)"),
			"login_username_env": stringProp("Environment variable on the MCP server holding the login username"),
			"login_password_env": stringProp("Environment variable on the MCP server holding the login password"),
			"insecure_skip_verify": {
				Type:        "boolean",
				Description: "Disable TLS certificate verification for this target (e.g. self-signed HTTPS like a local mofli device). Use with care.",
			},
			"custom_headers": mcp.Schema{
				Type:        "object",
				Description: "Additional headers sent on every request to this target (map of header name to value)",
				Properties:  map[string]mcp.Schema{},
			},
			"update": {Type: "boolean", Description: "Replace an existing target with the same name (default false)"},
		},
		Required: []string{"api"},
	}
}

// --- Argument extraction helpers ---

func strArg(args map[string]interface{}, key string) string {
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func boolArg(args map[string]interface{}, key string) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return false
}

func intArg(args map[string]interface{}, key string, def int) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	return def
}

func strSliceArg(args map[string]interface{}, key string) []string {
	raw, ok := args[key].([]interface{})
	if !ok {
		if s := strArg(args, key); s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

func strMapArg(args map[string]interface{}, key string) (map[string]string, error) {
	raw, ok := args[key].(map[string]interface{})
	if !ok {
		return nil, nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("argument %q expects string values, got %T for key %q", key, v, k)
		}
		out[k] = s
	}
	return out, nil
}

// apiDefinitionFromArgs parses register_openapi_api arguments into a definition.
func apiDefinitionFromArgs(args map[string]interface{}) (config.APIDefinition, bool, error) {
	name := strArg(args, "name")
	if name == "" {
		return config.APIDefinition{}, false, fmt.Errorf("'name' is required to register an API")
	}
	def := config.APIDefinition{
		Name:        name,
		Source:      strArg(args, "source"),
		Spec:        strArg(args, "spec"),
		IncludeTags: strSliceArg(args, "include_tags"),
		ExcludeTags: strSliceArg(args, "exclude_tags"),
		IncludeOps:  strSliceArg(args, "include_ops"),
		ExcludeOps:  strSliceArg(args, "exclude_ops"),
		Auth: config.AuthConfig{
			Type:           strings.ToLower(strArg(args, "auth_type")),
			In:             strArg(args, "auth_in"),
			Name:           strArg(args, "auth_name"),
			Prefix:         strArg(args, "auth_prefix"),
			HTTPScheme:     strArg(args, "auth_http_scheme"),
			Flow:           strArg(args, "auth_flow"),
			TokenURL:       strArg(args, "auth_token_url"),
			LoginOperation: strArg(args, "auth_login_operation"),
		},
		Monitoring: config.MonitorConfig{
			Enabled:    boolArg(args, "monitoring_enabled"),
			AutoReload: boolArg(args, "monitoring_auto_reload"),
		},
	}
	if def.Source == "" && def.Spec == "" {
		return config.APIDefinition{}, false, fmt.Errorf("provide either 'source' (path/URL) or an inline 'spec'")
	}
	return def, boolArg(args, "update"), nil
}

func targetDefinitionFromArgs(args map[string]interface{}) (config.TargetDefinition, bool, error) {
	t := config.TargetDefinition{
		Name:               strArg(args, "name"),
		BaseURL:            strArg(args, "base_url"),
		APIKey:             strArg(args, "api_key"),
		APIKeyEnv:          strArg(args, "api_key_env"),
		LoginUsername:      strArg(args, "login_username"),
		LoginPassword:      strArg(args, "login_password"),
		LoginUsernameEnv:   strArg(args, "login_username_env"),
		LoginPasswordEnv:   strArg(args, "login_password_env"),
		InsecureSkipVerify: boolArg(args, "insecure_skip_verify"),
	}
	headers, err := strMapArg(args, "custom_headers")
	if err != nil {
		return config.TargetDefinition{}, false, err
	}
	t.CustomHeaders = headers
	return t, boolArg(args, "update"), nil
}

// --- Management tool execution ---

// managementToolResult is the payload a management tool returns to the caller.
type managementToolResult struct {
	ok   bool
	text string
}

// runManagementTool executes a management tool and returns the text result for
// the MCP client. Registry mutations trigger a tools/list_changed broadcast.
func (r *Registry) runManagementTool(connID, name string, args map[string]interface{}) managementToolResult {
	var err error
	switch name {
	case ToolRegisterAPI:
		var def config.APIDefinition
		var update bool
		if def, update, err = apiDefinitionFromArgs(args); err == nil {
			var summary *APISummary
			if summary, err = r.RegisterAPI(def, update); err == nil {
				return okResult(fmt.Sprintf("Registered API %q: %s (OpenAPI %s) exposing %d tool(s). Select a target with register_api_target / set_active_api_target before calling them.", summary.Name, summary.Title, summary.SpecVersion, summary.ToolCount))
			}
		}
	case ToolUnregisterAPI:
		var summary *APISummary
		if summary, err = r.UnregisterAPI(strArg(args, "name")); err == nil {
			return okResult(fmt.Sprintf("Unregistered API %q (was exposing %d tool(s)).", summary.Name, summary.ToolCount))
		}
	case ToolListAPIs:
		summaries := r.APIs()
		body, _ := json.MarshalIndent(summaries, "", "  ")
		if len(summaries) == 0 {
			return okResult("No APIs are registered. Use register_openapi_api to add one.")
		}
		return okResult(string(body))
	case ToolAddTarget:
		var target config.TargetDefinition
		var update bool
		if target, update, err = targetDefinitionFromArgs(args); err == nil {
			var summary *APISummary
			if summary, err = r.AddTarget(strArg(args, "api"), target, update); err == nil {
				return okResult(fmt.Sprintf("Registered target %q on API %q. Targets: %s. Active: %s", target.Name, summary.Name, strings.Join(summary.Targets, ", "), summary.ActiveTarget))
			}
		}
	case ToolRemoveTarget:
		var summary *APISummary
		if summary, err = r.RemoveTarget(strArg(args, "api"), strArg(args, "target")); err == nil {
			return okResult(fmt.Sprintf("Removed target from API %q. Remaining targets: %s. Active: %s", summary.Name, strings.Join(summary.Targets, ", "), summary.ActiveTarget))
		}
	case ToolListTargets:
		api := strArg(args, "api")
		summary, ok := r.GetAPI(api)
		if !ok {
			return errResult(fmt.Errorf("API %q is not registered", api))
		}
		if len(summary.Targets) == 0 {
			return okResult(fmt.Sprintf("API %q has no targets. Use register_api_target to add one.", api))
		}
		targets := make([]map[string]interface{}, 0, len(summary.Targets))
		for _, name := range summary.Targets {
			targets = append(targets, map[string]interface{}{"name": name})
		}
		body, _ := json.MarshalIndent(map[string]interface{}{
			"api":           api,
			"active_target": summary.ActiveTarget,
			"targets":       targets,
		}, "", "  ")
		return okResult(string(body))
	case ToolSetActiveTarget:
		var summary *APISummary
		if summary, err = r.SetActiveTarget(strArg(args, "api"), strArg(args, "target")); err == nil {
			return okResult(fmt.Sprintf("Active target of API %q is now %q.", summary.Name, summary.ActiveTarget))
		}
	case ToolClearActiveTarget:
		var summary *APISummary
		if summary, err = r.ClearActiveTarget(strArg(args, "api")); err == nil {
			return okResult(fmt.Sprintf("Cleared active target of API %q. Callers must now pass an explicit 'target' argument.", summary.Name))
		}
	case ToolGetActiveTarget:
		api := strArg(args, "api")
		var active string
		if active, err = r.GetActiveTarget(api); err == nil {
			if active == "" {
				return okResult(fmt.Sprintf("API %q has no active target set.", api))
			}
			return okResult(fmt.Sprintf("Active target of API %q is %q.", api, active))
		}
	case ToolSetSessionTarget:
		api := strArg(args, "api")
		sessTarget := strArg(args, "target")
		if err := r.SetSessionActiveTarget(connID, api, sessTarget); err != nil {
			return errResult(err)
		}
		return okResult(fmt.Sprintf("Session-scoped active target of API %q is now %q (this session only).", api, sessTarget))
	case ToolClearSessionTarget:
		api := strArg(args, "api")
		if err := r.ClearSessionActiveTarget(connID, api); err != nil {
			return errResult(err)
		}
		return okResult(fmt.Sprintf("Cleared this session's active-target override for API %q.", api))
	case ToolGetSessionTarget:
		api := strArg(args, "api")
		if t := r.GetSessionActiveTarget(connID, api); t != "" {
			return okResult(fmt.Sprintf("This session targets API %q via %q (override).", api, t))
		}
		return okResult(fmt.Sprintf("This session has no active-target override for API %q (using the global active target).", api))
	case ToolDescribeAPI:
		api := strArg(args, "api")
		entry, err := r.GetApiEntryView(api)
		if err != nil {
			return errResult(err)
		}
		opts := describeOpts{
			include: map[string]bool{},
			search:  strArg(args, "search"),
			full:    strArg(args, "schema_detail") == "full",
		}
		for _, inc := range strSliceArg(args, "include") {
			opts.include[strings.ToLower(inc)] = true
		}
		body, err := describeAPIDoc(entry, opts)
		if err != nil {
			return errResult(err)
		}
		return okResult(body)
	case ToolGetOperation:
		api := strArg(args, "api")
		opRef := strArg(args, "operation")
		entry, err := r.GetApiEntryView(api)
		if err != nil {
			return errResult(err)
		}
		body, err := describeOperation(entry, opRef, boolArg(args, "examples"))
		if err != nil {
			return errResult(err)
		}
		return okResult(body)
	case ToolListSchemas:
		api := strArg(args, "api")
		entry, err := r.GetApiEntryView(api)
		if err != nil {
			return errResult(err)
		}
		body, err := listAPISchemas(entry, strArg(args, "name"), boolArg(args, "expand"), strArg(args, "search"), intArg(args, "limit", 100), intArg(args, "offset", 0))
		if err != nil {
			return errResult(err)
		}
		return okResult(body)
	case ToolSearchOperation:
		api := strArg(args, "api")
		entry, err := r.GetApiEntryView(api)
		if err != nil {
			return errResult(err)
		}
		body, err := searchOperations(entry, strArg(args, "query"), strings.ToUpper(strArg(args, "method")))
		if err != nil {
			return errResult(err)
		}
		return okResult(body)
	case ToolExportConfig:
		api := strArg(args, "api")
		entry, err := r.GetApiEntryView(api)
		if err != nil {
			return errResult(err)
		}
		body, err := exportConfig(entry)
		if err != nil {
			return errResult(err)
		}
		return okResult(body)
	case ToolRelogin:
		if err := r.Relogin(strArg(args, "api"), strArg(args, "target")); err != nil {
			return errResult(err)
		}
		return okResult(fmt.Sprintf("Cleared session token for API %q target %q; next call will re-authenticate.", strArg(args, "api"), strArg(args, "target")))
	case ToolTestTarget:
		api := strArg(args, "api")
		target := strArg(args, "target")
		body, err := testAPITarget(r, api, target)
		if err != nil {
			return errResult(err)
		}
		return okResult(body)
	case ToolReloadConfig:
		if r.PersistencePath() == "" {
			return errResult(fmt.Errorf("no config file is configured (server started without --config)"))
		}
		messages, err := r.ReloadFromConfig(r.PersistencePath())
		if err != nil {
			return errResult(err)
		}
		if len(messages) == 0 {
			return okResult("Config file has no APIs to register.")
		}
		// Apply server.log_level from the (possibly edited) config file.
		if lvl := r.ServerConfig().LogLevel; lvl != "" {
			if err := logx.SetLevelString(lvl); err != nil {
				logx.Module("management").Warn("ignoring invalid server.log_level during config reload", "level", lvl, "error", err)
			}
		}
		var b strings.Builder
		for _, m := range messages {
			b.WriteString("- " + m + "\n")
		}
		return okResult(strings.TrimSpace(b.String()))
	case ToolSetLogLevel:
		level := strArg(args, "level")
		if err := r.SetServerLogLevel(level); err != nil {
			return errResult(err)
		}
		return okResult(fmt.Sprintf("Log level set to %q (was recorded in the server config).", level))
	case ToolCheckSpec:
		api := strArg(args, "api")
		loadedAt, current, status, err := r.CheckSpecState(api)
		if err != nil {
			return errResult(err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "API %q spec freshness: %s\n", api, strings.ToUpper(status))
		if !loadedAt.IsZero() {
			fmt.Fprintf(&b, "  loaded spec from source at: %s\n", loadedAt.UTC().Format(time.RFC3339))
		} else {
			b.WriteString("  loaded spec from source at: (unknown)\n")
		}
		if !current.IsZero() {
			fmt.Fprintf(&b, "  current spec source mtime: %s\n", current.UTC().Format(time.RFC3339))
		} else {
			b.WriteString("  current spec source mtime: (unknown)\n")
		}
		switch status {
		case SpecStatusOutdated:
			b.WriteString("  WARNING: the spec was modified after the API was loaded; run reload_api to pick up the changes.")
		case SpecStatusUpToDate:
			b.WriteString("  The loaded spec matches the source.")
		default:
			b.WriteString("  Cannot compare: the spec source has no usable timestamp (inline spec or no Last-Modified header).")
		}
		return okResult(b.String())
	case ToolReloadAPI:
		api := strArg(args, "api")
		result, err := r.ReloadAPI(api)
		if err != nil {
			return errResult(err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Reloaded API %q from %q: %d tool(s).\n", result.Name, result.Source, result.ToolCount)
		switch result.Status {
		case SpecStatusOutdated:
			fmt.Fprintf(&b, "The previous load was OUTDATED (spec source modified after load); the reloaded toolset picks up the changes.")
		case SpecStatusUpToDate:
			b.WriteString("The previous load was up-to-date; reloading had no spec changes to pick up.")
		default:
			b.WriteString("No spec-source timestamp is available (inline spec or unknown source mtime), so freshness could not be compared.")
		}
		return okResult(b.String())
	case ToolPreviewCall:
		body, err := previewAPICall(r, strArg(args, "operation"), args["arguments"])
		if err != nil {
			return errResult(err)
		}
		return okResult(body)
	default:
		return errResult(fmt.Errorf("unknown management tool %q", name))
	}
	return errResult(err)
}

func okResult(text string) managementToolResult {
	return managementToolResult{ok: true, text: text}
}

func errResult(err error) managementToolResult {
	return managementToolResult{ok: false, text: err.Error()}
}

// --- Introspection helpers ---

// apiDocView is the JSON view returned by describe_openapi_api. It is fully
// self-referencing: endpoints carry both their REST form and the MCP tool that
// invokes them, and the tool schema/description _is_ the endpoint documentation.
type apiDocView struct {
	Info      apiInfoView               `json:"info"`
	Auth      config.AuthConfig         `json:"auth,omitempty"`
	Config    apiConfigView             `json:"config"`
	Targets   []targetInfoView          `json:"targets,omitempty"`
	Active    string                    `json:"active_target,omitempty"`
	Endpoints []endpointView            `json:"endpoints,omitempty"`
	Schemas   map[string]*mcp.SchemaDoc `json:"schemas,omitempty"`
}

type apiInfoView struct {
	Name        string   `json:"name"`
	Title       string   `json:"title,omitempty"`
	Version     string   `json:"version,omitempty"`
	Description string   `json:"description,omitempty"`
	Servers     []string `json:"servers,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

type apiConfigView struct {
	IncludeTags       []string              `json:"include_tags,omitempty"`
	ExcludeTags       []string              `json:"exclude_tags,omitempty"`
	IncludeOperations []string              `json:"include_ops,omitempty"`
	ExcludeOperations []string              `json:"exclude_ops,omitempty"`
	SpecVersion       string                `json:"spec_version,omitempty"`
	RegisteredAt      string                `json:"registered_at,omitempty"`
	SpecTimestamp     string                `json:"spec_timestamp,omitempty"` // spec source last-modified at load (RFC3339)
	Monitoring        *config.MonitorConfig `json:"monitoring,omitempty"`     // optional spec-source monitoring
	ToolCount         int                   `json:"tool_count"`
}

type targetInfoView struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url,omitempty"`
}

// endpointView glues a REST endpoint to its MCP tool.
type endpointView struct {
	OperationID string   `json:"operation_id,omitempty"`
	ToolName    string   `json:"tool_name,omitempty"`
	Method      string   `json:"method"`
	Path        string   `json:"path"`
	Summary     string   `json:"summary,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

type describeOpts struct {
	include map[string]bool // section, when non-empty only these sections render
	search  string          // endpoint filter
	full    bool            // schema_detail == full
}

func describeAPIDoc(entry *apiEntryView, opts describeOpts) (string, error) {
	doc := entry.Doc
	if doc == nil {
		return "", fmt.Errorf("API %q has no parsed documentation", entry.Def.Name)
	}
	has := func(section string) bool {
		if len(opts.include) == 0 {
			return true
		}
		return opts.include[section]
	}

	var monitoring *config.MonitorConfig
	if entry.Def.Monitoring.IsConfigured() {
		m := entry.Def.Monitoring
		monitoring = &m
	}

	view := apiDocView{
		Auth:    entry.Def.Auth.Effective(),
		Targets: []targetInfoView{},
		Active:  entry.Def.ActiveTarget,
		Config: apiConfigView{
			IncludeTags:       entry.Def.IncludeTags,
			ExcludeTags:       entry.Def.ExcludeTags,
			IncludeOperations: entry.Def.IncludeOps,
			ExcludeOperations: entry.Def.ExcludeOps,
			SpecVersion:       entry.SpecVersion,
			RegisteredAt:      entry.RegisteredAt.Format(time.RFC3339),
			SpecTimestamp:     formatSpecTimestamp(entry.SpecTimestamp),
			Monitoring:        monitoring,
			ToolCount:         len(entry.ToolSet.Tools),
		},
		Endpoints: []endpointView{},
		Schemas:   map[string]*mcp.SchemaDoc{},
	}
	if has("info") {
		view.Info = apiInfoView{
			Name:        entry.Def.Name,
			Title:       doc.Title,
			Version:     doc.Version,
			Description: doc.Description,
			Servers:     doc.Servers,
			Tags:        doc.Tags,
		}
	} else {
		view.Info.Name = entry.Def.Name
	}
	if has("targets") {
		for _, t := range entry.Def.Targets {
			view.Targets = append(view.Targets, targetInfoView{Name: t.Name, BaseURL: t.BaseURL})
		}
	}
	if !has("auth") {
		view.Auth = config.AuthConfig{}
	}

	// Endpoints (optionally filtered by search / limited by include).
	limit := -1
	for _, ep := range doc.Endpoints {
		if opts.search != "" {
			hay := strings.ToLower(ep.OperationID + " " + ep.Path + " " + ep.Summary + " " + strings.Join(ep.Tags, " "))
			if !strings.Contains(hay, strings.ToLower(opts.search)) {
				continue
			}
		}
		view.Endpoints = append(view.Endpoints, endpointView{
			OperationID: ep.OperationID,
			ToolName:    toolFullName(entry.Def.Name, ep.OperationID),
			Method:      ep.Method,
			Path:        ep.Path,
			Summary:     ep.Summary,
			Description: ep.Description,
			Tags:        ep.Tags,
		})
		if limit != -1 && len(view.Endpoints) >= limit {
			break
		}
	}
	if !has("endpoints") {
		view.Endpoints = []endpointView{}
	}

	// Schemas (compact or full; only when requested).
	if has("schemas") {
		if opts.full {
			view.Schemas = doc.Schemas
		} else {
			for n, s := range doc.Schemas {
				view.Schemas[n] = &mcp.SchemaDoc{Name: n, Type: s.Type, Ref: s.Ref, Description: s.Description}
			}
		}
	}

	body, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed rendering API documentation: %w", err)
	}
	return string(body), nil
}

func describeOperation(entry *apiEntryView, opRef string, examples bool) (string, error) {
	doc := entry.Doc
	if doc == nil {
		return "", fmt.Errorf("API %q has no parsed documentation", entry.Def.Name)
	}
	bare := strings.TrimPrefix(opRef, entry.Def.Name+toolNameSep)
	idx := -1
	for i, ep := range doc.Endpoints {
		if ep.OperationID == bare || ep.OperationID == opRef {
			idx = i
			break
		}
	}
	if idx == -1 {
		return "", fmt.Errorf("API %q has no operation %q", entry.Def.Name, opRef)
	}
	ep := doc.Endpoints[idx]

	view := struct {
		OperationID string             `json:"operation_id,omitempty"`
		ToolName    string             `json:"tool_name,omitempty"`
		Method      string             `json:"method"`
		Path        string             `json:"path"`
		Summary     string             `json:"summary,omitempty"`
		Description string             `json:"description,omitempty"`
		Parameters  []mcp.ParameterDoc `json:"parameters,omitempty"`
		RequestBody *mcp.SchemaDoc     `json:"request_body,omitempty"`
		Responses   []mcp.ResponseDoc  `json:"responses,omitempty"`
	}{
		OperationID: ep.OperationID,
		ToolName:    toolFullName(entry.Def.Name, ep.OperationID),
		Method:      ep.Method,
		Path:        ep.Path,
		Summary:     ep.Summary,
		Description: ep.Description,
		Parameters:  ep.Parameters,
		RequestBody: ep.RequestBody,
		Responses:   ep.Responses,
	}
	if !examples {
		body, err := json.MarshalIndent(view, "", "  ")
		if err != nil {
			return "", err
		}
		return string(body), nil
	}
	// Include example payloads mined from the raw spec where available.
	type withExamples struct {
		OperationID string                 `json:"operation_id,omitempty"`
		ToolName    string                 `json:"tool_name,omitempty"`
		Method      string                 `json:"method"`
		Path        string                 `json:"path"`
		Summary     string                 `json:"summary,omitempty"`
		Description string                 `json:"description,omitempty"`
		Parameters  []mcp.ParameterDoc     `json:"parameters,omitempty"`
		RequestBody interface{}            `json:"request_body,omitempty"`
		Responses   interface{}            `json:"responses,omitempty"`
		Examples    map[string]interface{} `json:"examples,omitempty"`
	}
	we := withExamples{
		OperationID: view.OperationID,
		ToolName:    view.ToolName,
		Method:      view.Method,
		Path:        view.Path,
		Summary:     view.Summary,
		Description: view.Description,
		Parameters:  ep.Parameters,
	}
	we.Examples = operationExamples(doc, ep)
	we.RequestBody = view.RequestBody
	we.Responses = view.Responses

	body, err := json.MarshalIndent(we, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// operationExamples returns example payloads for an endpoint if present on the
// raw spec. The ApiDoc model doesn't keep raw examples, so this is best-effort
// and empty unless a future load path stores them. Kept as a hook.
func operationExamples(doc *mcp.ApiDoc, ep mcp.EndpointDoc) map[string]interface{} {
	return nil
}

func listAPISchemas(entry *apiEntryView, name string, expand bool, search string, limit, offset int) (string, error) {
	doc := entry.Doc
	if doc == nil {
		return "", fmt.Errorf("API %q has no parsed documentation", entry.Def.Name)
	}
	if len(doc.Schemas) == 0 {
		return okMessage(fmt.Sprintf("API %q declares no DTO schemas (components.schemas/definitions).", entry.Def.Name)), nil
	}

	if name != "" {
		s, ok := doc.Schemas[name]
		if !ok {
			return "", fmt.Errorf("API %q has no schema named %q", entry.Def.Name, name)
		}
		body, err := json.MarshalIndent(s, "", "  ")
		if err != nil {
			return "", err
		}
		return string(body), nil
	}

	// Collect names, filter by search, sort.
	names := make([]string, 0, len(doc.Schemas))
	for n := range doc.Schemas {
		if search != "" && !strings.Contains(strings.ToLower(n), strings.ToLower(search)) {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	total := len(names)

	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 100
	}
	end := offset + limit
	if end > len(names) {
		end = len(names)
	}
	if offset > len(names) {
		offset = len(names)
		names = []string{}
	} else {
		names = names[offset:end]
	}

	view := map[string]interface{}{}
	for _, n := range names {
		s := doc.Schemas[n]
		if expand {
			view[n] = s
		} else {
			view[n] = map[string]interface{}{"type": s.Type, "ref": s.Ref}
		}
	}
	out := map[string]interface{}{
		"total":   total,
		"limit":   limit,
		"offset":  offset,
		"schemas": view,
	}
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func searchOperations(entry *apiEntryView, query, method string) (string, error) {
	doc := entry.Doc
	if doc == nil {
		return "", fmt.Errorf("API %q has no parsed documentation", entry.Def.Name)
	}
	q := strings.ToLower(query)
	results := []endpointView{}
	for _, ep := range doc.Endpoints {
		if method != "" && !strings.EqualFold(ep.Method, method) {
			continue
		}
		hay := strings.ToLower(ep.OperationID + " " + ep.Path + " " + ep.Summary + " " + strings.Join(ep.Tags, " "))
		if !strings.Contains(hay, q) {
			continue
		}
		results = append(results, endpointView{
			OperationID: ep.OperationID,
			ToolName:    toolFullName(entry.Def.Name, ep.OperationID),
			Method:      ep.Method,
			Path:        ep.Path,
			Summary:     ep.Summary,
			Tags:        ep.Tags,
		})
	}
	if len(results) == 0 {
		return okMessage(fmt.Sprintf("No operations in API %q match %q.", entry.Def.Name, query)), nil
	}
	body, err := json.MarshalIndent(map[string]interface{}{"api": entry.Def.Name, "query": query, "matches": results}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func exportConfig(entry *apiEntryView) (string, error) {
	view := map[string]interface{}{
		"name":           entry.Def.Name,
		"source":         entry.Def.Source,
		"spec_version":   entry.SpecVersion,
		"registered_at":  entry.RegisteredAt.Format(time.RFC3339),
		"spec_timestamp": formatSpecTimestamp(entry.SpecTimestamp),
		"auth":           entry.Def.Auth.Effective(),
		"active_target":  entry.Def.ActiveTarget,
		"include_tags":   entry.Def.IncludeTags,
		"exclude_tags":   entry.Def.ExcludeTags,
		"include_ops":    entry.Def.IncludeOps,
		"exclude_ops":    entry.Def.ExcludeOps,
	}
	if entry.Def.Monitoring.IsConfigured() {
		view["monitoring"] = entry.Def.Monitoring
	}
	targets := []map[string]interface{}{}
	for _, t := range entry.Def.Targets {
		targets = append(targets, map[string]interface{}{
			"name":                 t.Name,
			"base_url":             t.BaseURL,
			"insecure_skip_verify": t.InsecureSkipVerify,
		})
	}
	view["targets"] = targets
	body, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// testAPITarget performs a lightweight reachability check against a target:
// an OPTIONS (or GET fallback) request on the base URL. Proves the target is
// up and reachable; does not evaluate credentials.
func testAPITarget(r *Registry, apiName, targetName string) (string, error) {
	entry, err := r.GetApiEntryView(apiName)
	if err != nil {
		return "", err
	}
	target, ok := entry.Def.Target(targetName)
	if !ok {
		return "", fmt.Errorf("API %q has no target named %q", apiName, targetName)
	}
	base := target.BaseURL
	if base == "" {
		base = sniffSpecBase(entry.ToolSet)
	}
	if base == "" {
		return "", fmt.Errorf("API %q target %q has no base URL", apiName, targetName)
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "http://" + base
	}

	client := httpClientForConfig(target.ToConfig())
	for _, method := range []string{http.MethodOptions, http.MethodGet} {
		req, err := http.NewRequest(method, strings.TrimRight(base, "/")+"/", nil)
		if err != nil {
			return "", err
		}
		resp, rerr := client.Do(req)
		if rerr != nil {
			return "", fmt.Errorf("target %q at %s unreachable: %w", targetName, base, rerr)
		}
		resp.Body.Close()
		// Any HTTP response (even 404/401) proves reachability.
		return okMessage(fmt.Sprintf("Target %q reachable at %s (%s -> HTTP %d).", targetName, base, method, resp.StatusCode)), nil
	}
	return "", fmt.Errorf("unreachable")
}

func okMessage(text string) string { return text }

// formatSpecTimestamp renders a spec-source timestamp as RFC3339 UTC, or "" when
// it is unknown (inline spec / unprobeable source).
func formatSpecTimestamp(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339)
}

// previewAPICall builds the literal request a tool call would send, without
// executing it, and renders method/url/headers/body + resolved target.
func previewAPICall(r *Registry, operation string, arguments interface{}) (string, error) {
	args := map[string]interface{}{}
	if m, ok := arguments.(map[string]interface{}); ok {
		args = m
	} else if b, ok := arguments.(json.RawMessage); ok {
		_ = json.Unmarshal(b, &args)
	}
	req, target, err := buildRegisteredRequest(r, &ToolCallParams{ToolName: operation, Input: args})
	if err != nil {
		return "", err
	}
	headers := map[string]string{}
	for k, v := range req.Header {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}
	body := ""
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		req.Body.Close()
		if len(b) > 0 {
			body = string(b)
		}
	}
	cookies := []string{}
	for _, c := range req.Cookies() {
		cookies = append(cookies, c.Name+"="+c.Value)
	}
	view := map[string]interface{}{
		"operation": operation,
		"target":    target,
		"method":    req.Method,
		"url":       req.URL.String(),
		"headers":   headers,
		"cookies":   cookies,
		"body":      body,
		"note":      "Dry-run only; no request was sent.",
	}
	out, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}
