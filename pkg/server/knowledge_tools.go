package server

import (
	"fmt"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/ckanthony/openapi-mcp/pkg/mcp"
)

// Knowledge management tool names.
const (
	ToolKnowledgeInit     = "knowledge_init"
	ToolKnowledgeLoad     = "knowledge_load"
	ToolKnowledgeStatus   = "knowledge_status"
	ToolKnowledgeUpsert   = "knowledge_upsert"
	ToolKnowledgeDelete   = "knowledge_delete"
	ToolKnowledgeGet      = "knowledge_get"
	ToolKnowledgeSearch   = "knowledge_search"
	ToolKnowledgeClarify  = "knowledge_clarify"
	ToolKnowledgeRemember = "knowledge_remember_sequence"
	ToolCapabilities      = "capabilities"
	ToolDiscoverTask      = "discover_task"
	ToolUpdateKnowledge   = "update_api_knowledge"
	ToolRunTask           = "run_task"
	ToolKnowledgeReview   = "knowledge_review"
)

// knowledgeToolNames is the set of knowledge management tools.
var knowledgeToolNames = map[string]bool{
	ToolKnowledgeInit: true, ToolKnowledgeLoad: true, ToolKnowledgeStatus: true,
	ToolKnowledgeUpsert: true, ToolKnowledgeDelete: true, ToolKnowledgeGet: true,
	ToolKnowledgeSearch: true, ToolKnowledgeClarify: true, ToolKnowledgeRemember: true,
	ToolCapabilities: true, ToolDiscoverTask: true, ToolUpdateKnowledge: true,
	ToolRunTask: true, ToolKnowledgeReview: true,
}

func isKnowledgeTool(name string) bool { return knowledgeToolNames[name] }

// buildKnowledgeTools returns the tool definitions exposed for the knowledge
// layer. They are appended to the always-present management tools.
func buildKnowledgeTools() []mcp.Tool {
	apiSchema := func() mcp.Schema { return nameOnlySchema("api", "Name of the registered API") }
	return []mcp.Tool{
		{
			Name:        ToolKnowledgeInit,
			Description: "Scaffold the knowledge library (Markdown manual) of an API from its spec: index, glossary/capability placeholders and skeleton documents for endpoints (optionally filtered by tag) and schemas, in the API's configured knowledge language.",
			InputSchema: mcp.Schema{
				Type: "object",
				Properties: map[string]mcp.Schema{
					"api":          {Type: "string", Description: "Name of the registered API"},
					"include_tags": stringListProp("Only scaffold endpoints with these tags (operation tags); empty scaffolds all"),
				},
				Required: []string{"api"},
			},
		},
		{
			Name:        ToolKnowledgeLoad,
			Description: "Index the API's knowledge library from disk (local backend). Reports the number of documents and validation warnings (broken links, unknown anchors, invalid capability steps).",
			InputSchema: apiSchema(),
		},
		{
			Name:        ToolKnowledgeStatus,
			Description: "Report the API's knowledge state: enabled, language, root, backend type, documents count, library warnings and (later) git sync state.",
			InputSchema: apiSchema(),
		},
		{
			Name:        ToolKnowledgeUpsert,
			Description: "Add or replace a knowledge document for an API. Pass full Markdown content (YAML front-matter + body). persist=false (default) stores it only in the current session overlay; persist=true writes it into the manual and re-indexes.",
			InputSchema: mcp.Schema{
				Type: "object",
				Properties: map[string]mcp.Schema{
					"api":     {Type: "string", Description: "Name of the registered API"},
					"content": {Type: "string", Description: "Full Markdown document (front-matter + body)"},
					"doc":     {Type: "string", Description: "Optional document id (defaults to the front-matter id or the title)"},
					"path":    {Type: "string", Description: "Optional library-relative path when persisting (e.g. capabilities/my-task.md)"},
					"persist": {Type: "boolean", Description: "Write to the manual instead of the session overlay (default false)"},
				},
				Required: []string{"api", "content"},
			},
		},
		{
			Name:        ToolKnowledgeDelete,
			Description: "Remove a knowledge document from the session overlay (persist=false, default). Persisted removal must be done by editing the manual files.",
			InputSchema: mcp.Schema{
				Type: "object",
				Properties: map[string]mcp.Schema{
					"api":     {Type: "string", Description: "Name of the registered API"},
					"doc":     {Type: "string", Description: "Document id to remove"},
					"persist": {Type: "boolean", Description: "Whether to remove from the manual (unsupported) rather than the overlay"},
				},
				Required: []string{"api", "doc"},
			},
		},
		{
			Name:        ToolKnowledgeGet,
			Description: "Return a knowledge document (id or library path) as Markdown. Checks the session overlay first, then the library.",
			InputSchema: mcp.Schema{
				Type: "object",
				Properties: map[string]mcp.Schema{
					"api": {Type: "string", Description: "Name of the registered API"},
					"doc": {Type: "string", Description: "Document id or library-relative path"},
				},
				Required: []string{"api", "doc"},
			},
		},
		{
			Name:        ToolKnowledgeSearch,
			Description: "Search the API's knowledge (library + session overlay) for a natural-language query and return the most relevant documents with their scores, kinds and parameters.",
			InputSchema: mcp.Schema{
				Type: "object",
				Properties: map[string]mcp.Schema{
					"api":   {Type: "string", Description: "Name of the registered API"},
					"query": {Type: "string", Description: "Natural-language query"},
					"limit": {Type: "integer", Description: "Max documents to return (default 10)"},
				},
				Required: []string{"api", "query"},
			},
		},
		{
			Name:        ToolKnowledgeClarify,
			Description: "Tell the agent what knowledge is available for an intent: the closest documents, or guidance about what to document when nothing matches.",
			InputSchema: mcp.Schema{
				Type: "object",
				Properties: map[string]mcp.Schema{
					"api":    {Type: "string", Description: "Name of the registered API"},
					"intent": {Type: "string", Description: "Natural-language intent/term"},
				},
				Required: []string{"api", "intent"},
			},
		},
		{
			Name:        ToolKnowledgeRemember,
			Description: "Create a capability draft in the session overlay from the tool calls recorded in this session (requires knowledge.learning enabled on the API). Persist it later with knowledge_upsert.",
			InputSchema: mcp.Schema{
				Type: "object",
				Properties: map[string]mcp.Schema{
					"api":  {Type: "string", Description: "Name of the registered API"},
					"name": {Type: "string", Description: "Id for the draft capability (optional)"},
				},
				Required: []string{"api"},
			},
		},
		{
			Name:        ToolCapabilities,
			Description: "List the high-level capability documents (tasks) of an API from its knowledge: each capability's intents it answers, its parameters and step count. Use with discover_task for natural-language task resolution.",
			InputSchema: apiSchema(),
		},
		{
			Name:        ToolDiscoverTask,
			Description: "Resolve a natural-language intent against the API's capabilities and produce a static plan (list of steps with resolved inputs); it NEVER executes. Use run_task (dry-run/ask) to list the plan to execute, or run_task (auto) to execute it.",
			InputSchema: mcp.Schema{
				Type: "object",
				Properties: map[string]mcp.Schema{
					"api":    {Type: "string", Description: "Name of the registered API"},
					"intent": {Type: "string", Description: "High-level task in natural language"},
				},
				Required: []string{"api", "intent"},
			},
		},
		{
			Name:        ToolRunTask,
			Description: "Execute a capability task of an API (by capability id or natural-language reference). mode=dry-run lists every step it would run and validates it statically WITHOUT executing; mode=ask returns the same plan for confirmation; mode=auto executes the steps in order, chaining outputs into later inputs.",
			InputSchema: mcp.Schema{
				Type: "object",
				Properties: map[string]mcp.Schema{
					"api":    {Type: "string", Description: "Name of the registered API"},
					"task":   {Type: "string", Description: "Capability id or natural-language task"},
					"params": {Type: "object", Properties: map[string]mcp.Schema{}, Description: "Task parameters (values for the capability's param.* bindings) as an object"},
					"target": {Type: "string", Description: "Target (server) to route the steps to; defaults to the API's active target"},
					"mode":   {Type: "string", Enum: []interface{}{"dry-run", "ask", "auto"}, Description: "dry-run (default): list only; ask: list + require confirmation; auto: execute"},
				},
				Required: []string{"api", "task"},
			},
		},
		{
			Name:        ToolKnowledgeReview,
			Description: "Record the outcome of a run_task execution on a session-overlay capability document, amending its Markdown body (e.g. corrections learned from experience). Only overlay docs can be amended in place.",
			InputSchema: mcp.Schema{
				Type: "object",
				Properties: map[string]mcp.Schema{
					"api":     {Type: "string", Description: "Name of the registered API"},
					"task":    {Type: "string", Description: "Capability id or natural-language task"},
					"outcome": {Type: "string", Description: "Result (e.g. success, partial, failed)"},
					"note":    {Type: "string", Description: "Human note about the execution"},
				},
				Required: []string{"api", "task", "outcome", "note"},
			},
		},
		{
			Name:        ToolUpdateKnowledge,
			Description: "Update the knowledge configuration of an API in place (hot): enabled, language, root, backend type/repository/branch/sync and learning. Persists and re-loads the library. Values may reference host env vars via *_env fields.",
			InputSchema: mcp.Schema{
				Type: "object",
				Properties: map[string]mcp.Schema{
					"api":              {Type: "string", Description: "Name of the registered API"},
					"enabled":          {Type: "boolean", Description: "Enable/disable the knowledge layer"},
					"language":         {Type: "string", Description: "Knowledge base language (e.g. es, en)"},
					"root":             {Type: "string", Description: "Root directory of the library/checkout"},
					"type":             {Type: "string", Enum: []interface{}{"local", "git"}, Description: "Backend type"},
					"repository":       {Type: "string", Description: "Git URL (or repository_env host env var name)"},
					"repository_env":   {Type: "string", Description: "Host env var holding the git URL"},
					"branch":           {Type: "string", Description: "Git branch (default main)"},
					"branch_env":       {Type: "string", Description: "Host env var holding the branch"},
					"sync":             {Type: "string", Enum: []interface{}{"auto", "manual"}, Description: "Git sync policy"},
					"conflict":         {Type: "string", Enum: []interface{}{"rebase", "ff_only"}, Description: "Git divergence policy"},
					"auth_token_env":   {Type: "string", Description: "Host env var with git token"},
					"ssh_key_env":      {Type: "string", Description: "Host env var with ssh key path"},
					"author_name_env":  {Type: "string", Description: "Host env var with git author name"},
					"author_email_env": {Type: "string", Description: "Host env var with git author email"},
					"learning":         {Type: "boolean", Description: "Enable session learning (trace-based capability drafts)"},
				},
				Required: []string{"api"},
			},
		},
	}
}

// knowledgeConfigFromArgs merges arg-provided fields over an existing config.
func knowledgeConfigFromArgs(args map[string]interface{}, existing config.KnowledgeConfig) config.KnowledgeConfig {
	kc := existing
	if v, ok := args["enabled"].(bool); ok {
		kc.Enabled = v
	}
	if s := strArg(args, "language"); s != "" {
		kc.Language = s
	}
	if s := strArg(args, "root"); s != "" {
		kc.Root = s
	}
	if s := strArg(args, "type"); s != "" {
		kc.Backend.Type = s
	}
	if s := strArg(args, "repository"); s != "" {
		kc.Backend.Repository = s
	}
	if s := strArg(args, "repository_env"); s != "" {
		kc.Backend.RepositoryEnv = s
	}
	if s := strArg(args, "branch"); s != "" {
		kc.Backend.Branch = s
	}
	if s := strArg(args, "branch_env"); s != "" {
		kc.Backend.BranchEnv = s
	}
	if s := strArg(args, "sync"); s != "" {
		kc.Backend.Sync = s
	}
	if s := strArg(args, "conflict"); s != "" {
		kc.Backend.Conflict = s
	}
	if s := strArg(args, "auth_token_env"); s != "" {
		kc.Backend.AuthTokenEnv = s
	}
	if s := strArg(args, "ssh_key_env"); s != "" {
		kc.Backend.SSHKeyEnv = s
	}
	if s := strArg(args, "author_name_env"); s != "" {
		kc.Backend.AuthorNameEnv = s
	}
	if s := strArg(args, "author_email_env"); s != "" {
		kc.Backend.AuthorEmailEnv = s
	}
	if v, ok := args["learning"].(bool); ok {
		kc.Learning.Enabled = v
	}
	return kc
}

// interfaceMapArg returns an object-typed tool argument as a map, or empty.
func interfaceMapArg(args map[string]interface{}, key string) map[string]interface{} {
	if m, ok := args[key].(map[string]interface{}); ok {
		return m
	}
	return map[string]interface{}{}
}

// runKnowledgeTool dispatches a knowledge management tool call.
func (r *Registry) runKnowledgeTool(connID, name string, args map[string]interface{}) managementToolResult {
	switch name {
	case ToolKnowledgeInit:
		api := strArg(args, "api")
		out, err := r.KnowledgeInit(api, strSliceArg(args, "include_tags"))
		if err != nil {
			return errResult(err)
		}
		return okResult(out)
	case ToolKnowledgeLoad:
		out, err := r.LoadKnowledge(strArg(args, "api"))
		if err != nil {
			return errResult(err)
		}
		return okResult(out)
	case ToolKnowledgeStatus:
		out, err := r.KnowledgeStatus(strArg(args, "api"))
		if err != nil {
			return errResult(err)
		}
		return okResult(out)
	case ToolKnowledgeUpsert:
		out, err := r.KnowledgeUpsert(connID, strArg(args, "api"), strArg(args, "content"), strArg(args, "path"), boolArg(args, "persist"))
		if err != nil {
			return errResult(err)
		}
		return okResult(out)
	case ToolKnowledgeDelete:
		out, err := r.KnowledgeDelete(connID, strArg(args, "api"), strArg(args, "doc"), boolArg(args, "persist"))
		if err != nil {
			return errResult(err)
		}
		return okResult(out)
	case ToolKnowledgeGet:
		out, err := r.KnowledgeGet(connID, strArg(args, "api"), strArg(args, "doc"))
		if err != nil {
			return errResult(err)
		}
		return okResult(out)
	case ToolKnowledgeSearch:
		out, err := r.KnowledgeSearch(connID, strArg(args, "api"), strArg(args, "query"), intArg(args, "limit", 10))
		if err != nil {
			return errResult(err)
		}
		return okResult(out)
	case ToolKnowledgeClarify:
		out, err := r.ClarifyKnowledge(connID, strArg(args, "api"), strArg(args, "intent"))
		if err != nil {
			return errResult(err)
		}
		return okResult(out)
	case ToolKnowledgeRemember:
		out, err := r.RememberSequence(connID, strArg(args, "api"), strArg(args, "name"))
		if err != nil {
			return errResult(err)
		}
		return okResult(out)
	case ToolCapabilities:
		out, err := r.KnowledgeCapabilities(connID, strArg(args, "api"))
		if err != nil {
			return errResult(err)
		}
		return okResult(out)
	case ToolDiscoverTask:
		out, err := r.DiscoverTask(connID, strArg(args, "api"), strArg(args, "intent"))
		if err != nil {
			return errResult(err)
		}
		return okResult(out)
	case ToolRunTask:
		mode := TaskMode(strArg(args, "mode"))
		if mode == "" {
			mode = TaskModeDryRun
		}
		switch mode {
		case TaskModeDryRun, TaskModeAsk, TaskModeAuto:
		default:
			return errResult(fmt.Errorf("invalid mode %q (want dry-run|ask|auto)", mode))
		}
		out, err := r.RunTask(connID, strArg(args, "api"), strArg(args, "task"), interfaceMapArg(args, "params"), strArg(args, "target"), mode)
		if err != nil {
			return errResult(err)
		}
		return okResult(out)
	case ToolKnowledgeReview:
		out, err := r.KnowledgeReview(connID, strArg(args, "api"), strArg(args, "task"), strArg(args, "outcome"), strArg(args, "note"))
		if err != nil {
			return errResult(err)
		}
		return okResult(out)
	case ToolUpdateKnowledge:
		api := strArg(args, "api")
		entry, err := r.apiEntryFor(api)
		if err != nil {
			return errResult(err)
		}
		kc := knowledgeConfigFromArgs(args, entry.Def.Knowledge)
		if kc.Backend.Type != "" && kc.Backend.Type != "local" && kc.Backend.Type != "git" {
			return errResult(fmt.Errorf("invalid backend type %q (want local|git)", kc.Backend.Type))
		}
		if s := kc.ResolveKnowledgeBackend().Sync; s != "auto" && s != "manual" {
			return errResult(fmt.Errorf("invalid sync policy %q (want auto|manual)", s))
		}
		if c := kc.ResolveKnowledgeBackend().Conflict; c != "rebase" && c != "ff_only" {
			return errResult(fmt.Errorf("invalid conflict policy %q (want rebase|ff_only)", c))
		}
		out, err := r.UpdateKnowledgeConfig(api, kc)
		if err != nil {
			return errResult(err)
		}
		return okResult(out)
	default:
		return errResult(fmt.Errorf("unknown knowledge tool %q", name))
	}
}
