# Plan — Meta knowledge base, scripting (tengo), dynamic exposure, and web UI (CopilotKit)

> Status: **specification**, with **Phase 1 (meta knowledge base) implemented and
> tested**, **Phase 2 (dynamic exposure / runtime tool footprint) implemented and
> tested**, **Phase 3 (scripting core, tengo) implemented and tested**, **Phase 4
> (scripting hardening) implemented and tested**, and
> **Phase 7's `view`/`dashboard` model + `view` tool + `run_task`
> auto-display implemented and tested** (see §2, §3, §4.6, §7, and the
> "Implementation
> progress" section at the end); Phase 5+ planned below. This document is the
> detailed
> development guide for four coordinated features on top of the per-API semantic
> knowledge base (`docs/knowledge.md`, implemented):
>
> 1. **Meta knowledge base** — a global knowledge space not tied to any particular
>    API, where agents document general patterns, operation recipes, tool
>    write-ups and cross-API ideas, and can cross-reference the per-API knowledge
>    bases.
> 2. **Scripting via tengo** — algorithms and methods stored as executable
>    scripts inside the knowledge layer, exposed to agents as regular MCP tools.
>    Scripts may be scoped to an API or live in the meta knowledge base. Agents
>    are expected to *save recipes as script tools* instead of redoing manual
>    work.
> 3. **Dynamic exposure** — activating/deactivating whole APIs, sections (tags)
>    and individual operations at runtime, to shrink the MCP tool footprint on
>    the AI agent prompt to only what is being used. Everything remains
>    discoverable through always-exposed introspection tools, and the agent can
>    turn features on and off by calling management tools. OpenCode picks up the
>    changes on the fly via `notifications/tools/list_changed`.
> 4. **Interactive web UI via CopilotKit** — a web application served by the MCP
>    server itself, generated on the fly depending on context, that lets humans
>    interact with the registered APIs, MCP tools, scripting and the knowledge
>    base. Its purpose is to **mirror the agent session and render its results**
>    (tool calls, tasks, scripts) as natural views — not merely to provide a
>    chat — and to host **knowledge-defined dashboards** with interactive inputs.
>
> This document does not modify code; it is the reference for the implementation
> phases at the end.

## Goal

Let humans and agents grow a **global operational memory**, keep the **agent's
prompt footprint as small as the work at hand**, and talk to the machine through
a human interface:

- A **meta knowledge base** that is independent of any single API spec (unlike
  today's `knowledge.*` which is always attached to an `APIDefinition`), with the
  same editing/search/tasking capabilities per-API KBs have today, plus new doc
  kinds for patterns, tool guides and ideas, and cross-KB links.
- **Executable knowledge**: a capability or a known algorithm is stored as a
  script (tengo source) that surfaces as a first-class MCP tool
  (`<scope>__<script>`), callable by humans and agents alike, with the same
  dry-run/ask/auto discipline conceptually available. "First time do it by hand,
  second time write it once" — when an agent solves a problem manually (e.g.
  finding free disk space on the target machine), it is directed to save the
  recipe as a script tool for future reuse.
- **Dynamic exposure**: the set of tools served by `tools/list` is a *runtime
  projection* of what is registered. APIs, section (tag) groups and single
  operations can be activated/deactivated at any time — by an operator or by the
  agent itself — so only the tools actually in use cost prompt space. The
  projection never hides the *ability to discover*: introspection, exposure and
  knowledge tools are always exposed and always report the full available surface
  plus each item's activation state. OpenCode and other spec-compliant clients
  subscribe to `notifications/tools/list_changed` and refresh their tool list
  without a restart.
- A **web session UI** served by the same process that answers MCP. It is **not a
  chat webapp**: its purpose is to reflect — live — what is available on a given
  AI agent session (registered APIs, tools, tasks, knowledge) and what happens
  on it (tool calls, `run_task` executions, script runs), rendering every result
  in a natural, human-friendly way as the user talks with the AI. The UI is
  **knowledge-driven and dashboard-capable**: users can declare full, API-driven
  data dashboards in natural language ("I want to see the list of users and
  their actions on the platform for the last two hours") and the MCP *both*
  returns the data to the agent **and** renders it in the UI as a view.
  Views can be saved as dashboards in the knowledge base; a dashboard can be
  associated with a task/tool so it is shown automatically when that task
  completes; and dashboards support **inputs** (pagination, search, filters,
  sorting) that let users trigger task/tool calls directly from the UI.

## Audience

Agents and humans who operate this MCP server. The document assumes the reader
is familiar with `docs/knowledge.md` (`Backend`, `Library`, `Doc`, capability
`steps`, `run_task`) and `docs/config-file.md`.

## Closed decisions

- **The meta knowledge base is modeled as a reserved, virtual API named
  `_meta`.** No spec is loaded for it, it has no `ToolSet.Operations`, no
  targets, and its `KnowledgeConfig` comes from a **top-level `meta:` block** in
  the config file instead of an `apis:` entry. Everything that is per-API today
  (library root, backends, overlay, `knowledge_*` tools, learning) works for
  `_meta` unchanged. A reserved `_meta` is rejected as a user-registered API name
  by `validateAPIName` (keeps the collision rules in `rebuild` intact).
- **Cross-API knowledge links use the `kb:` scheme.** A relative link `/` path
  resolves inside the *same* KB (current behavior); `kb:<api>:<path>` resolves
  against another API's KB (or `kb:_meta:`); broken targets are warnings, same as
  today.
- **New doc kinds** are added beside the current six: `pattern`, `tool`,
  `idea`. `pattern` is a reusable, API-agnostic procedure; `tool` documents a
  tool/integration (e.g. a shell command, a CopilotKit capability); `idea` is an
  uncommitted thought/note (never executed).
- **Scripts are documents.** A script is a knowledge doc with
  `kind: script`, whose body is tengo source, stored under
  `scripts/<id>.md` (a code-fenced tengo block inside the body) or
  `scripts/<id>.tengo` (raw source; front-matter via a `.md` sibling). The
  library loader recognizes them and the registry registers one MCP tool per
  script.
- **tengo is the only script language.** Pure Go, no toolchain, sandboxed VM —
  fits the distroless container and the "no new runtime" constraint that chose
  `go-git` over a git binary. Version: `github.com/d5/tengo/v2`.
- **Scripts run in a permissioned sandbox.** By default a script may only use
  the tengo standard library plus an explicit `mcp` host module to call
  registered tools (`mcp.call("weather__GetCurrent...", {...})`). Privileged
  modules (`os`, `fs`, `exec`, `http`) are enabled per-script via front-matter
  `permissions`; the module set is decided at load time (not at runtime) so a
  script cannot escalate itself.
- **Script parameters are declared in front-matter** (`params:`) exactly like
  capability parameters, with types; the generated MCP tool input schema is
  derived from them. The tool result is the script's return value serialized as
  JSON/text.
- **Agent directive is enforced by content, not by code.** The KB templates and
  `docs/` guidance instruct agents to promote successful manual procedures into
  script tools (the free-disk-space example is used throughout). A
  `knowledge_resolve`/`knowledge_clarify` hint and a default README in the meta
  KB carry the same directive.
- **Exposure is a runtime projection over a fully-parsed spec, and static
  include/exclude filters are consolidated into it.** The parser stops dropping
  operations at parse time (today `pkg/parser/parser.go` filters
  `IncludeTags/ExcludeTags/IncludeOps/ExcludeOps` destructively); the full
  `ToolSet` and `ApiDoc` are always kept. The existing `include_*`/`exclude_*`
  config becomes the **authoritative allow-set** (the agent can *never* expose
  something the operator excluded) and the new `exposure:` overlay only narrows
  or widens **within** it. Default runtime mode is `all`, so existing configs
  behave exactly as today.
- **Exposure is two-layered: a global/persisted baseline plus a per-session
  override.** The global baseline (operator config + `update_api_exposure`) is
  what a fresh session sees; each connection can then override it **for its own
  connection only** via `update_session_api_exposure` (mirroring
  `SetSessionActiveTarget`, dropped on disconnect). `tools/list` is answered per
  `connID`, so every session — opencode, Web UI, scripts — has its own tool
  footprint while still sharing the same registrations, targets and knowledge.
- **Introspection, exposure and knowledge tools are permanently exposed** and
  report activation state, so an agent always knows what exists and how to turn
  it on/off even with every API tool hidden. There is no way to "deactivate" the
  ability to activate.
- **Every exposure mutation ends in `rebuild` + `notifyToolsListChanged()`**,
  which broadcasts `notifications/tools/list_changed` to all initialized
  connections (today `server.go:1388 broadcastToolsListChanged`, already used on
  register/unregister/target-change/spec-reload). OpenCode subscribes to it and
  re-fetches `tools/list`, updating its tool registry in place — no restart, no
  session reset.
- **The web UI mounts under `/ui` on the same server** (`ServeMCP`), alongside
  `/mcp`. The UI is a **static SPA (embedded via `go:embed`)** plus a
  **server-side chat bridge** (`POST /ui/chat`, SSE responses) that speaks to the
  same `Registry` through a real MCP connection (so sessions, overlay knowledge,
  session active targets, learning and exposure all work for web users). The
  React app is **generated at request time from a `/ui/manifest` JSON** that
  reflects the live registry context; CopilotKit's generative UI (`CopilotKit` +
  `useChat`) renders tool-call results dynamically.
- **The web UI is per-session.** Each browser session is an isolated, real MCP
  connection with its own `connID`: its own chat stream, per-connection knowledge
  overlay, session active targets and learning traces. Two users in different
  sessions never share or corrupt each other's UI state. Server-global
  notifications (`tools/list_changed`, spec-changed) reach every session but
  render only in a shared status area, never into a chat timeline; and
  `/ui/manifest` is a read-only global snapshot that sessions do not mutate.
- **The web UI is a session mirror and result renderer, not a chat product.**
  The value is in reflecting what a given agent session is doing and has
  available: as the user talks with the AI, every result the session produces
  (API tool call -> a rendered result table/list/cards, `run_task` -> plan + step
  results, knowledge search -> document links, script -> its output) is
  displayed in the UI in a natural manner. The conversation/text is a side
  effect, not the point. This colors every design choice below: events, model,
  and rendering are built around *results with structure*, not chat bubbles.
- **UI views and dashboards are knowledge documents.** A *view* is a knowledge
  doc describing how to render a result or a query (`kind: view`); a *dashboard*
  is a saved, named collection of views and their inputs (`kind: dashboard`),
  optionally associated with the capability/tool that produces its data. Both
  live in the KB (meta or per-API), are persisted via the normal
  `knowledge_upsert`/`knowledge_init` discipline, are searchable and can be
  shared across sessions. "Save as dashboard" = persist such a document.
- **Dashboards can be bound to tasks.** A dashboard may declare the task/API
  tool that feeds it; when that task finishes (its tool call completes, its
  `run_task` returns), the UI displays the dashboard automatically.
- **Dashboards are interactive call surfaces.** A dashboard's declared *inputs*
  (search, filters, pagination, sorting, refresh) map onto the parameters of the
  backing tool/task; interacting with the dashboard issues the corresponding
  tool/task call through the same session bridge, so the UI both reflects *and*
  drives the session.
- **No new long-lived daemons.** Script execution, KB sync and the UI bridge all
  run inside the existing process/goroutine model. `/ui` serves an embedded
  bundle; there is no Node development server in the runtime.

## 1. Meta knowledge base

### 1.1 Concept

Today `KnowledgeConfig`, `sessionKnowledge`, `mergedLibrary`, overlay and all
`knowledge_*` tools are addressed by an API name (`apiEntry`). The meta KB is a
single special entry that:

- represents **global** knowledge (no HTTP API behind it),
- is **always present** (enabled/disabled and storage configured via config),
- hosts the cross-API artifacts: patterns, tool write-ups, ideas, scripts, and
  the meta-level `_index.md`.

The same `KnowledgeConfig`/`Backend`/`Library` code paths are reused; only the
"API" identifier, the origin of the config, and the tool surface differ.

### 1.2 Config surface (`pkg/config`)

```yaml
meta:
  knowledge:
    enabled: true
    language: en
    root: ~/.config/openapi-mcp/knowledge/_meta   # default: <configDir>/knowledge/_meta
    type: git                 # local | git (default local)
    backend:                  # same fields as apis[].knowledge.backend
      repository_env: META_KB_REPO
      branch: main
      sync: auto
      conflict: rebase
      # author_*_env, auth_token_env, ssh_key_env ...
    learning:
      enabled: true           # traces + _suggestions/ for the meta scope
```

`FileConfig` gains `Meta *MetaConfig` (or a plain `MetaKnowledge KnowledgeConfig`
field). Normalization/validation reuse `normalizeKnowledgeConfig`
(refactor of the per-API knowledge defaults into `pkg/config`). Hot updates go
through a `meta_update_knowledge` tool mirroring `update_api_knowledge`.

### 1.3 Model (`pkg/knowledge/model.go`)

New kinds:

```go
KindPattern   Kind = "pattern"   // reusable, API-agnostic procedure
KindTool      Kind = "tool"      // documents a tool / integration / command
KindIdea      Kind = "idea"      // free-form note; never executed
KindScript    Kind = "script"    // executable tengo: surface as an MCP tool
KindView      Kind = "view"      // how to render a result / query in the web UI
KindDashboard Kind = "dashboard" // saved collection of views + inputs, task-bound
```

`Doc` gains a `Permissions` field (used by `kind: script`), an
`Input`/`Outputs` description for `pattern`/`script` that mirrors `params`, and
a `View`/`Dashboard` block used by `kind: view`/`kind: dashboard` docs:

```go
type View struct {
    Source   string     `yaml:"source,omitempty"`   // tool/capability/script feeding the view
    Inputs   []ViewInput `yaml:"inputs,omitempty"`  // interactive controls (search, page, sort, filter)
    Layout   string     `yaml:"layout,omitempty"`   // table | list | cards | chart
    AutoShow bool       `yaml:"auto_show,omitempty"`// show when Source completes (run_task memo)
}
type ViewInput struct {
    Name     string   `yaml:"name"`
    Label    string   `yaml:"label,omitempty"`
    Type     string   `yaml:"type,omitempty"`    // text | number | select | ...
    Options  []string `yaml:"options,omitempty"` // for select
    Required bool     `yaml:"required,omitempty"`
    Default  string   `yaml:"default,omitempty"`
    Binding  string   `yaml:"binding,omitempty"` // param.<name> | step.<n>.<jsonpath> on Source
}
```

Tree for `_meta`:

```
_meta/
  _index.md
  patterns/<slug>.md
  tools/<slug>.md
  ideas/<slug>.md
  scripts/<id>.md | scripts/<id>.tengo
  glossary/<term>.md
  capabilities/<task>.md
  views/<slug>.md
  dashboards/<slug>.md
  _suggestions/
  _templates/<lang>/
```

Per-API KBs unchanged, except scripts may also live per-API:
`apis.<api>.knowledge` tree gains `scripts/<id>.tengo|.md`, and views/dashboards
may also be defined per-API (`views/`, `dashboards/`).

### 1.4 Cross-API linking (`pkg/knowledge/links.go`, `store.go`)

- `Link` resolution today is relative within `Root`. Extend resolution to fully
  qualified targets: `kb:<api>:<relpath>` (and `kb:_meta:`), resolved against the
  target KB's library when both are indexed in the same `Registry.BatchLoad`
  context. Unknown `kb:` targets produce a broken-link warning just like unknown
  relative links today.
- `related:` in front-matter already takes a target path; extend to accept the
  `kb:` form so a `tool` doc can reference an endpoint doc of another API.

### 1.5 Tool surface (`pkg/server/knowledge_tools.go`)

Reuse the existing `knowledge_*` tools by allowing `api: _meta`:

- All `knowledge_*`, `capabilities`, `discover_task`, `run_task`,
  `update_api_knowledge` accept `_meta` as the scope. `api` parameter docs gain
  the note `"_meta" addresses the global knowledge base`. Scripts in `_meta`
  surface as `_meta__<script>`; per-API scripts as `<api>__<script>`.

New tools (all in `knowledgeToolNames`):

- `meta_init` — scaffold a meta KB (index + templates), like `knowledge_init`
  but spec-less.
- `meta_status` / `meta_sync` — KB status + `knowledge_sync` for `_meta`.
- `script_list {scope?}` — list scripts with their params/permissions.
- `script_describe {scope, script}` — show the source + schema of a script.
- `memorize {scope?, doc, content, persist?}` — convenience wrapper over
  `knowledge_upsert` used to teach agents the "write it down" discipline.
- `meta_update_knowledge {enabled?, language?, root?, type?, repository?, ...}`
  — hot config edit of the meta KB (mirror of `update_api_knowledge`).
- `view {scope?, api?, view_id, inputs?}` (in `knowledgeToolNames`) — render a
  `kind: view`/`kind: dashboard` document: resolves its `Source`, fills its
  declared inputs from `inputs`, executes the backing tool/task/preview call and
  returns a structured, screen-ready result (rows, columns, layout) that the web
  session bridge renders directly. This single tool powers both the session
  mirror (results of any call are wrapped into a view) and interactive
  dashboards (changing an input re-invokes the backing call through the same
  bridge). `run_task` emits `view` events with rendered views so the UI shows
  dashboards automatically when a bound task finishes.

### 1.6 Registry plumbing (`pkg/server/registry.go`)

- `NewRegistry` keeps everything; add a `metaEntry` (an `apiEntry`-like struct
  with no `ToolSet`, no targets, `Def.Name == "_meta"`, `Def.Knowledge` from
  `FileConfig.Meta`). `rebuild` includes it in the tools snapshot **only for its
  script tools** (see §2.4) and knowledge tools (already always exposed).
- `validateAPIName` rejects `_meta`.
- `DropSession` also clears the `_meta` overlay.

## 2. Scripting via tengo

### 2.1 Storage and document format

An executable script is a knowledge doc. Two accepted encodings on disk:

```
scripts/free-disk-space.tengo          # raw source with .tengo extension
scripts/free-disk-space.md             # front-matter + ```tengo fenced body
```

Both normalize at load time into a `Doc{Kind: KindScript, Body: <tengo source>,
Params: [...], Permissions: {...}}`. The `.md` form keeps the front-matter
convention; `.tengo` is preferred for hand-written scripts (front-matter pulled
from a sibling `.md` if present, or from `script:<id>` metadata inline via a
comment block).

Example (`_meta/scripts/free-disk-space.tengo`):

```tengo
// ---
// id: free_disk_space
// kind: script
// summary: Free disk space on the MCP host (uses exec)
// permissions: {exec: true}
// params:
//   - {name: mount, required: false}
// ---
// Tengo source below. This recipe was first solved interactively with shell
// commands, then promoted into a reusable script tool (_meta__free_disk_space).
func := import("os/exec")
out := func("df", "-h", mount == undefined ? "/" : mount)
return out
```

Scripts are documents first: they `related:` to the pattern they implement and
can be reviewed (`knowledge_review`) and learned into capabilities.

### 2.2 Execution engine (`pkg/script` — new package)

`go.mod`: add `github.com/d5/tengo/v2`.

```go
type Module = func(vm *tengo.Script, args map[string]interface{}) (interface{}, error)

// Modules: the privileged set a script may declare permission for.
const (
    ModMCP  = "mcp"   // mcp.call(fullTool, args) / mcp.resolve(fullTool)
    ModOS   = "os"    // os.getenv, os.hostname, ...
    ModExec = "exec"  // exec.run(cmd, args...) with allowlist + timeout
    ModFS   = "fs"    // fs.read, fs.stat, fs.list (path-constrained)
    ModHTTP = "http"  // http.get/post (constrained by allowlist / timeouts)
)

type Executor struct { ... }   // holds permissions + host bridges
func (e *Executor) Run(ctx, src string, params map[string]interface{}) (string, error)
```

- Compile once per script (`tengo.Compile`), cache compiled bytecode per
  `(id, sourceHash)`.
- `Params` are injected as VM globals (converted from JSON input; numbers/strict
  types per `pkg/mcp` schema conventions).
- Return value normalized: nil → empty; map/array → JSON; string → verbatim.
- A hard **time budget** (default 10s, configurable per script via front-matter
  `timeout`) and a **step limit** guard the VM against runaway loops.
- The `mcp` host module calls `executeRegisteredTool` with the real registry and
  connection, so scripts can chain API tools, other scripts, and knowledge tools,
  and their calls show up in session learning traces.
- Security: `permissions` is parsed only from on-disk front-matter; the runtime
  executor is constructed from the parsed permission set — a script cannot grant
  itself a privilege. Privilged modules default to **deny**. `exec`/`fs`/`http`
  additionally take opt-in allowlists in the meta/per-API config:

```yaml
meta:
  scripting:
    exec_allowlist: ["df", "du"]       # commands permitted for scripts with exec:true
    fs_read_roots: [""]                # filesystem roots readable by fs:* ("" = whole /, default none)
    http_allowlist: ["https://api.weatherbit.io/*"]
    default_timeout_s: 10
```

### 2.3 MCP tool registration

On `knowledge_load`/`knowledge_status`/`LoadKnowledge` the registry scans the
merged library for `kind: script` docs and builds `mcp.Tool`s:

- Name: `toolFullName(scope, id)` → `_meta__free_disk_space` /
  `weather__<script>`.
- Input schema: derived from `Params` (name, required, type, description from
  `summary`), with a synthetic `target` property only for per-API scripts (same
  as `exposeTool`).
- Description: `summary` + `[script]` marker + note that it executes tengo under
  the declared permissions.
- Collision handling: reuse the existing `used` map in `rebuild` — a script name
  colliding with an operation tool is a registration **error** (same rule as
  today). Registered in `ResolveTool`/`executeRegisteredTool` routing so
  `run_task` steps (capabilities) and other scripts can call script tools
  transparently.

Scripts are excluded from `capabilities`'s *task* lists (they are tools, not
tasks), but scopable via `script_list`.

### 2.4 Dispatcher integration

`executeRegisteredTool` currently takes `(reg, connID, call)` and resolves via
`ResolveTool`. Add a script branch:

- `ResolveTool(fullName)` returns a synthetic `toolRef` whose `tool.Handler`
  (new optional field on `mcp.Tool`, or a side map `scriptTools[fullName]`) runs
  `script.Executor.Run`.
- `run_task` (auto) and the `mcp` host module both funnel through the same path,
  so scripts are invisible to callers beyond being ordinary tools. Dry-run
  listing shows script steps as `(script)`.
- Script tools respect the exposure mask (§3): per-API script tools are toggled
  like any operation of that API (bucket tag `script`); `_meta__*` scripts are
  always exposed unless the operator hides them — they are the mirror of the meta
  KB and by design few and global.

### 2.5 Agent directive ("write it once")

- `knowledge_init`/`meta_init` templates and the meta KB `_index.md` embed the
  rule: *solve manually once → `knowledge_remember_sequence`/`memorize` the
  recipe → promote to a `scripts/` doc → call it as a tool next time.*
- The free-disk-space case is the canonical example in docs and templates.
- `knowledge_clarify` and `knowledge_search` can surface a
  "related script exists" hint when an intent matches a script's `intents`.
- When a script tool is executed successfully, session learning records a trace
  that can fold the run into a capability draft (running the script as one step).
- Scripts that call API tools whose operations are currently hidden (§3) get the
  same "tool not exposed" error as direct calls, teaching the agent to activate
  before use.

## 3. Dynamic exposure (tool footprint)

### 3.1 Concept

Reduce the number of tools served by `tools/list` to the actor's current needs:

- **Whole API** off → none of its operation tools are served (the API stays
  registered; targets, auth, knowledge intact).
- **Section off** (an OpenAPI tag, or a path-prefix group) → the operations in
  that section disappear.
- **Operation off** (single operationId) → that one tool disappears.
- **On** works the same way in reverse.

Deactivated tools are *not forgotten*: the full spec is always indexed, so every
introspection tool can still describe what exists, whether it is currently
exposed, and how to expose it. Agents discover first, activate only what they
need, and the prompt shrinks automatically.

### 3.2 Static allow-set vs runtime overlay

Today `pkg/parser/parser.go` filters operations destructively
(`IncludeTags/ExcludeTags/IncludeOps/ExcludeOps`). Change:

- The parser always returns the **full** `ToolSet` (all operations) and **full**
  `ApiDoc`. The filtering block (parser.go:1158-1181) is removed from the parse
  path.
- The static config is re-interpreted as the API's **allow-set** (`allowed`).
  Semantics preserved for existing users — the runtime overlay defaults to
  "expose all allowed operations".
- The overlay lives in **two layers**: the global baseline on `apiEntry.Exposure`
  and a per-session override in `sessionExposure[connID]` (§3.3). Both only
  narrow/widen **within** `allowed`. An operation excluded by
  `exclude_ops`/`exclude_tags` (or not included by `include_ops`/`include_tags`)
  can never be activated by an agent: that requires an operator config edit. This
  is the security boundary for exposure.
- `tools/list` is computed per connection: a session with no override sees the
  global baseline; otherwise its own override is applied on top.

Effective exposed set, computed in `rebuild`:

```
exposed(op) = apiActive
          ∧ allowed(op)
          ∧ (mode == "all"
               ? op ∉ disabledOps ∧ tags(op) ∩ disabledTags == ∅
               : op ∈ activeOps ∨ tags(op) ∩ activeTags ≠ ∅)
```

### 3.3 State and persistence

Two layers, reusing the existing per-session plumbing:

- **Global baseline (persisted)**: `apiEntry.Exposure` with a mirror persisted
  into `APIDefinition.Exposure` in the config file (survives restarts and
  `reload_config`). This is the default footprint every new session starts from.
- **Per-session override (ephemeral)**: `Registry.sessionExposure` —
  `map[connID]map[apiName]Exposure`, exactly like `sessionTargets`
  (`registry.go:118`) and `sessionKnowledge`. Added to `Registry` alongside them,
  seeded in `NewRegistry`, and deleted in `DropSession(connID)`
  (`registry.go:1297`) so an override dies with its connection. No persistence.
- `apiEntry` gains `OpTags map[string][]string` (operationId → tags, built once
  at load from `ApiDoc.Endpoints`) so tag toggles resolve to operations without
  re-parsing.

```go
type Exposure struct {
    Active        bool     // whole-API on/off (default true)
    Mode          string   // "all" | "none"
    ActiveTags    []string // mode=none: sections force-on
    ActiveOps     []string // mode=none: operations force-on
    DisabledTags  []string // mode=all: sections force-off
    DisabledOps   []string // mode=all: operations force-off
}
```

Composition per session (evaluated in `rebuild` / the tools/list filter):

```
effective(conn, op) = allowed(op)
          ∧ (session(conn, op)  if a session override exists for that API
             else baseline(op))
session(conn, op) = connActive ∧ (connMode == "all" ? op ∉ connDisabled :
                                  op ∈ connActiveOps ∨ tags(op) ∩ connActiveTags ≠ ∅)
```

So a session can narrow below the baseline or widen back up to it — the allow-set
(`allowed`) remains the only hard boundary. A session never leaks its view into
another session.

Config surface (per API — the baseline):

```yaml
apis:
  - name: weather
    source: ...
    exposure:
      active: true          # whole-API on/off
      mode: all             # all | none (bootstrap a minimal footprint with none)
      active_tags: []
      active_ops: []
      disabled_tags: []
      disabled_ops: []
```

### 3.4 Tool surface (`pkg/server/management.go`)

New management tools (always exposed, see §3.6):

- `update_api_exposure {api, active?, mode?, activate_tags?, activate_ops?,
  deactivate_tags?, deactivate_ops?}` — mutate the **global baseline** for the
  API, then `rebuild`, `persist`, broadcast. `mode = none` +
  `activate_tags: [forecast]` is the canonical "slim down to one section" move;
  `mode = all` resets to the full allow-set.
- `update_session_api_exposure {api, active?, mode?, activate_tags?,
  activate_ops?, deactivate_tags?, deactivate_ops?}` — same shape, but for the
  **calling connection only** (it is routed by `connID`; never persisted).
  This is how one agent slims down its own prompt without affecting colleagues,
  and how it is the per-session counterpart of `set_session_active_api_target`.
- `clear_session_api_exposure {api}` — drop this connection's override for the
  API, reverting to the global baseline (counterpart of `clear_session_active_api_target`).
- `api_exposure {api?}` — report per API: totals vs exposed counts, **global
  baseline and this session's effective** exposure (mode, active tag/op lists,
  whether a session override is active) and, with `api` set, per-operation/tag
  status (`exposed|hidden|excluded-by-config|session-hidden`) so the agent knows
  exactly what *it* can toggle. This is the always-available "what exists and what
  can I turn on" surface.
- `register_openapi_api` (update), `register_api_target` accept `exposure`; the
  tool schemas mention the footprint workflow.
- Sections are addressed by **tag name** (the OpenAPI `tags` array); an optional
  `path_prefix` convenience (`/forecast`, `/bulk`) expands to the operations
  under that path — both resolve through `OpTags`/path→op mapping and produce the
  same tag/op entries, so `api_exposure` shows the resolved result.

Introspection tools report activation state **as seen by the calling session**
(the session-effective view, with the global baseline alongside where useful):

- `list_openapi_apis` → per API adds `exposedTools: N/M` (session view),
  `active`, `mode`, `sessionOverride: true|false`.
- `describe_openapi_api` (endpoints) → each endpoint marked
  `active: true|false` for this session.
- `search_openapi_operations` → each hit marked with its current (session) state.
- `get_api_operation` → notes the tool's current exposure for this session and
  how to toggle it.
- `export_openapi_config` → includes the effective global `exposure` baseline
  (persisted config is always global).

### 3.5 Rebuild / dispatch integration

- Global `rebuild` computes the baseline per-API tool list via the §3.2 rule
  before the existing naming/collision loop (`registry.go:284-295`). Script tools
  (§2.4) are filtered through the same rule.
- `tools/list` is answered **per connection**: `handleToolsListJSONRPC(connID,
  ...)` (`server.go:658`) already receives `connID` but today returns global
  `reg.Tools()`. Add `Registry.ToolsForSession(connID)` which starts from the
  baseline and applies the session override for that connID (management tools
  always included). The snapshot `r.tools` stays the global baseline; the
  per-session filter is applied on the way out. Neither transport bypasses this
  — both SSE and streamable HTTP dispatch through the same handler.
- Call-time gate: `dispatchJSONRPC`/`executeRegisteredTool(reg, connID, ...)`
  reject calls to tools that exist in the index but are not exposed **for this
  session** with:
  `tool "weather__x" is registered but not exposed in this session; enable it
  with update_session_api_exposure {api:"weather", activate_ops:["x"]} (or
  update_api_exposure to change it for everyone)`.
  This is reachable only via races/stale plans, since clients re-list after
  `tools/list_changed`. `run_task` (auto/ask) and scripts carry the same connID,
  so the gate applies uniformly; `ResolveTool` stays global (introspection needs
  the full index).
- `run_task` static validation flags steps whose tool is not exposed for this
  session as `INACTIVE` in dry-run; auto mode fails fast with the same activation
  hint.
- Both `update_api_exposure` and `update_session_api_exposure` end in
  `notifyToolsListChanged()` → broadcast `tools/list_changed` (all sessions
  re-list and each gets its own filtered view; harmless, and keeps the shared
  status area in sync). Only the global variant additionally does `rebuild` +
  `persist`. A session-only mutation could otherwise be notified just to the
  affected connID — optional optimization, not required.

### 3.6 Permanently exposed surface

The following stay exposed no matter what is deactivated (they are the reason an
agent can always recover):

- registry/management tools (`list_openapi_apis`, `describe_openapi_api`,
  `search_openapi_operations`, `get_api_operation`, `list_api_schemas`,
  `export_openapi_config`, `test_api_target`, ...),
- exposure tools (`update_api_exposure`, `update_session_api_exposure`,
  `clear_session_api_exposure`, `api_exposure`),
- knowledge tools and `_meta` tools (`capabilities`, `discover_task`,
  `run_task`, `knowledge_*`, `meta_*`, `script_list`/`script_describe`,
  `memorize`).

If the agent renders "I cannot see any tools", `list_openapi_apis` /
`api_exposure` still answer and `update_api_exposure` still works.

### 3.7 OpenCode on-the-fly tool updates

- OpenCode subscribes to `notifications/tools/list_changed` from MCP servers and
  re-fetches `tools/list`, updating its in-memory tool registry without restart
  (anomalyco/opencode#5913 — "Subscribe to notifications/tools/list_changed …
  Enables consumers to react when servers dynamically modify their tool list").
- The server already emits this notification for register/unregister/
  target/spec-reload (`server.go:1388 broadcastToolsListChanged`; fire
  `notifyToolsListChanged` on exposure mutations too). This is the *single*
  mechanism needed: no opencode-specific code, no config file edits, no session
  reset.
- Verification (manual, in Phase 2): start the server, connect **two** opencode
  instances, and in session A call `update_session_api_exposure` (e.g.
  `active: false`, then `active: true`; then `mode: none, activate_tags:[...]`).
  Confirm **only session A's** tool list grows/shrinks and a just-activated tool
  is immediately callable there, while session B's footprint is unchanged; then
  change the global baseline with `update_api_exposure` and confirm both sessions
  re-list. Add an integration test asserting the notification is broadcast after
  each exposure mutation to every initialized connection (pattern already proven
  in `TestManagementToolsJSON` / monitoring tests) and that two concurrent
  sessions receive different, non-leaking `tools/list` results.

### 3.8 Edge cases

- **Deactivated API**: targets/auth/knowledge/overlay remain; only its operation
  tools leave `tools/list` **for the sessions that deactivated it**. A
  session-hidden tool called by that session (direct, `run_task`, scripts,
  `mcp.call`) fails with the activation hint until re-activated. Re-activation is
  instant and restores the exact previous exposure state for that session.
- **Monitoring auto-reload**: on spec change the allow-set is recomputed and the
  global baseline is reapplied on top; session overrides keep their op/tag lists
  (re-resolved against the new spec — an op that disappears from the spec simply
  stops matching). Tools added by a spec update appear as `hidden` until
  activated (default mode `all` exposes them, consistent with how auto-reload
  behaves today).
- **Session lifecycle and defaults**: a new session has no override and inherits
  the global baseline (backward compatible — single-operator deployments see no
  change). An override lasts as long as the connection: `DropSession(connID)`
  (`registry.go:1297`) deletes `sessionExposure` alongside `sessionTargets` /
  `sessionKnowledge` / `sessionTraces` on disconnect or `/ui` tab close.
- **Global vs session authoritative for one session's view**: the allow-set
  (`include_*`/`exclude_*`) is the only hard boundary — no session or the global
  baseline can exceed it. Global baseline deactivations are **soft**: a session
  may re-activate within the allow-set for itself. If an operator wants global
  deactivations to be a hard floor, that flag is an open decision (see Open
  questions).

## 4. Interactive web UI (CopilotKit)

The web UI is a **live mirror of the agent session**: it shows the tools, tasks,
capabilities and knowledge a session has available, and it renders — in a natural
manner — every result the session produces as the user talks with the AI and the
AI runs API tools, tasks, scripts and knowledge searches. The chat field is a
control surface, not the product; the product is the session's results being
readable and interactable. This is achieved by (a) the session bridge streaming
every tool/task/script outcome as structured `view` events, and (b) the
knowledge base holding persistent, queryable UI **views** and **dashboards**.

### 4.1 Architecture

CopilotKit is a TypeScript/React framework (generative UI, `useChat`, chat
runtime). The Go server cannot embed it directly; instead:

```
Browser  ── GET /ui (embedded SPA) ───────────────►  Go ServeMCP (same :8080)
Browser  ── GET /ui/manifest (context JSON) ──────►  /ui/manifest handler
Browser  ── POST /ui/chat (JSON) ────────────────►  /ui/chat handler ──► Registry
Browser  ◄── SSE stream (chat messages + copilot  ◄──  notifications, tool calls)
```

The `/ui` SPA is a **pre-built static bundle** embedded with `go:embed`
(`webui/dist`), produced by a `make webui` (Node) step in CI — it is committed or
built before `go build`. The runtime server never runs Node.

The chat bridge owns an MCP-style connection: it creates a `connID` per browser
session (`X-Ui-Session`), registers it in `activeConnections`-like plumbing or
directly drives `dispatchJSONRPC`, so:

- tools/initialize + tools/list come straight from `Registry.Tools()`,
- per-connection overlay (`knowledge_upsert`), session active targets, learning
  and **exposure** work identically for the web user,
- SSE notifications (`notifications/message`, spec-changed, knowledge sync,
  **tools/list_changed**) stream into the UI, so the web session reacts to the
  same footprint changes as opencode.

### 4.2 Per-session isolation

Multiple browsers/users can use `/ui` concurrently; no two sessions share view
state, chat history or session-scoped data. Each browser session is a first-class
MCP conversation:

- **One `connID` per browser session.** `/ui/chat` mints a fresh `connID` from an
  opaque, per-tab/per-login `X-Ui-Session` token and registers it in the same
  `activeConnections` map as MCP clients. Everything the registry already keys by
  `connID` is isolated for free: the per-connection knowledge overlay
  (`sessionKnowledge[connID]`), session active targets
  (`SetSessionActiveTarget(connID, ...)`) and learning traces. `DropSession`
  (already invoked on SSE cleanup) releases them when the tab closes.
- **Chat history and event streams are private.** Each session's SSE stream
  carries only that session's assistant responses, its own tool-call events and
  its own CopilotKit action stream; the bridge writes responses only into the
  originating connID's buffered channel. User A's conversation never renders in
  user B's window, and tool calls from session A are never shown as session B's.
- **Broadcast notifications are global; rendering is scoped.** Server-global
  events (`tools/list_changed`, `api/spec_changed`, log-level changes, exposure
  mutations) are delivered to every session's stream so all views stay in sync,
  but the front-end renders them only in the shared status area (tool-count
  indicator, "API reloaded" toast) — they are never injected into a session's
  chat timeline. Session-specific content never travels over the broadcast path.
- **View/layout state lives client-side.** Open panels, pinned tools, filters,
  collapsed sections, drafts and the chat DOM are per-tab React state keyed by the
  session token; the server stores no UI layout. Each tab requests its own
  `X-Ui-Session`, so there are no shared cookies/sessionStorage keys to collide
  across tabs or users.
- **`/ui/manifest` is a read-only global context snapshot**, identical for every
  session in its *baseline* fields (APIs, targets, allow-set, global exposure),
  but its `exposed`/`mode`/`activeTags` numbers are the **requesting session's
  effective values** (the manifest handler resolves the `X-Ui-Session` token to a
  connID). Sessions never write to the manifest; session-scoped changes go
  through `update_session_api_exposure`/knowledge tools exactly as a headless
  client would, and each live view re-renders from its own filtered tool list.
- **Capacity and cleanup.** `max_sessions` caps concurrent web connections
  (reject or evict the oldest — decide in Phase 5). An idle lease timeout drops
  the session via `DropSession`, freeing its overlay and traces. The count is a
  mutex-guarded counter alongside `activeConnections`.

### 4.3 Dynamic/manifest-driven UI

`/ui/manifest` returns JSON computed at request time from the live registry:

```json
{
  "apis": [{"name": "weather", "targets": ["default"], "active": "default",
            "tools": 46, "exposed": 12, "mode": "none",
            "exposure": {"activeTags": ["forecast"]},
            "knowledge": {"enabled": true, "language": "en"}}],
  "meta": {"knowledge": {"enabled": true}},
  "scripts": [{"name": "_meta__free_disk_space", "summary": "..."}],
  "capabilities": {"weather": ["..."]},
  "server": {"logLevel": "info"}
}
```

The SPA renders its initial layout from the manifest (which APIs exist, what
tools/capabilities/scripts are exposed or hidden, whether a meta KB is
configured) — i.e. the UI is **generated on the fly depending on context**, not
hand-coded per API. CopilotKit generative UI (`CopilotKit`'s `useChat` +
`CopilotPortal` + custom `RenderFunctionComponent`) turns the streamed events
(tool call, args, result, structured JSON payloads) into interactive components in
the conversation.

Projection rules (front-end only, data comes from the registry/manifest):

- A tool call event with a known `api` → a "call card" showing method/path
  (from `search_openapi_operations` knowledge at render time, or the tool
  description already embedded in `Tools()` — which is enriched today by
  `buildToolDescription`).
- A script tool event → a collapsible "script ran (permissions …,
  duration …)" card.
- A `knowledge_search` result → a link list into KB docs.
- A `run_task` plan → a step checklist that turns into result cards per step.
- A rendered `view` event (`kind: view`/`kind: dashboard` resolved by the
  `view` tool, or the auto-displayed dashboard of a completed task) → a table,
  list, card or chart of the structured rows, with the view's declared inputs
  (search, filters, pagination, sorting) rendered as live controls that re-issue
  the backing call.
- An exposure change event → a live "tool count" indicator in the header.
- Unknown/malformed payloads → fallback markdown block; the UI must never crash
  on unknown stream shapes.

### 4.4 Config surface

```yaml
server:
  ui:
    enabled: true          # serve /ui, /ui/manifest, /ui/chat (default true)
    session_header: X-Ui-Session   # optional; else cookie/query
    max_sessions: 100      # browser session limit
    # Optional bearer token to gate the UI endpoints:
    # token_env: UI_ACCESS_TOKEN   ('' = open on the local network)
```

`ServeMCP` gains routes when `ui.enabled` (`mux.HandleFunc("/ui", ...)`,
`"/ui/manifest"`, `"/ui/chat"`). CORS handling stays centralized.

### 4.5 CopilotKit specifics

- Dependency: `@copilotkit/react-core`, `@copilotkit/react-ui`,
  `@copilotkit/react-textarea` (front-end only, in `webui/`).
- The `/ui/chat` SSE stream is the "runtime" CopilotKit needs: it emits
  MCP-style message objects (`{type:"copilot.action", ...}`) which the SDK turns
  into GenUI components; tool/function-call style messages get rendered by the
  front-end component map. If a heavier agent is desired later, a single
  `/ui/agent` CopilotKit *cloud/middleware* endpoint can be added — this spec
  keeps the Go server as the source of truth and uses the local bridge only.
- `useChat` in `CopilotKit` connects to `/ui/chat`; the stream returns both the
  assistant text and structured events. The chat model itself is pluggable; the
  bridge may expose a `model:` setting or default to calling tools/knowledge
  directly with a thin local planner (see phase plan).

### 4.6 Views, dashboards and the session mirror

The UI's core promise: **"you talk to the AI, and everything it runs shows up
here as something you can actually read and interact with."** The following are
explicit requirements:

- **The UI automatically reflects the agent session.** When the user types
  "I want to see the list of users and their actions on the platform for the
  last two hours", the agent resolves the request (via `discover_task`/tool
  selection), runs the underlying API calls, and the MCP **both returns the data
  to the agent** (tool text result, as today) **and renders it in the UI** as a
  structured view. The text answer is not the deliverable; the rendered result
  is.
- **Every session outcome renders naturally.** API tool calls, `run_task`
  executions, script runs and knowledge searches all emit a `view` event whose
  payload is a screen-ready structure (rows/columns/list/cards, plus metadata
  such as total count). The front-end's projection rules (§4.3) turn them into
  components; "unknown shape" always falls back to markdown.
- **Views and dashboards are knowledge documents.** A user can say "save this as
  a dashboard"; the bridge persists a `kind: dashboard` doc (or the agent writes
  a `view` doc) via the normal `knowledge_upsert` path, so dashboards are
  durable, searchable and reusable across sessions. A `kind: view` doc declares:
  - `source` — the tool/capability/script it renders (optionally by natural
    language intent that is resolved at render time),
  - `inputs` — interactive controls bound to the source's parameters,
  - `layout` — table/list/cards/chart,
  - `auto_show` — display automatically when `source` completes.
- **Dashboards bind to tasks.** A dashboard may be associated with a capability
  (task) — either declared in the doc (`source`/`related` to the capability) or
  set at runtime. When that task finishes (`run_task` returns), the server emits
  the corresponding rendered view and the UI displays the dashboard
  automatically, without the user asking again.
- **Dashboards have inputs that drive calls.** A dashboard whose source is e.g.
  `/users` supports pagination, search, filters and sorting: each control is a
  declared `ViewInput` bound to a parameter of the backing tool/task
  (`binding: param.<name>` or a step output). Changing an input (typing a
  search, paging, clicking a column header) re-invokes the backing call through
  the same session bridge, so the dashboard is a fully interactive API surface,
  not a static snapshot.
- **Declaring a dashboard in natural language.** "Show me a dashboard of the
  last two hours of user activity, searchable by user id and sortable by time"
  is a first-class request: the agent creates (or updates) a `view`/`dashboard`
  doc from the conversation, the `view` tool renders it immediately, and the
  saved doc means the same dashboard is available in future sessions by name.

## 5. Changes per package

- `go.mod`: `github.com/d5/tengo/v2` (scripting). Front-end deps live in
  `webui/package.json` (not Go).
- `pkg/config`: `MetaConfig`, `ScriptingConfig`, `ExposureConfig` (on
  `APIDefinition`), `UIServerConfig`; validate `_meta`, `server.ui`, exposure
  lists; expose the knowledge-defaults normalization for reuse.
- `pkg/knowledge`: `model.go` (new kinds incl. `view`/`dashboard` + `View`/
  `ViewInput` block, `Permissions`), `store.go` (script doc discovery + `.tengo`
  parsing), `links.go` (`kb:` scheme), `templates.go` (meta/script/view
  templates).
- `pkg/parser`: stop dropping ops destructively; always return the full
  `ToolSet`/`ApiDoc` (regression-covered by existing include/exclude tests restated
  as allow-set + exposure tests).
- `pkg/script` (new): executor + host modules + sandbox/permissions.
- `pkg/server`: `metaEntry` + `_meta` tools (`meta_*`, `script_*`, `memorize`,
  `view`); a **result→view wrapper** that turns tool/task/script outcomes into
  structured render payloads and emits `view` events on the session stream;
  `Exposure` state on `apiEntry` + `sessionExposure[connID]` (registry maps,
  seeded in `NewRegistry`, cleaned in `DropSession`), `OpTags`,
  expose-filter in `rebuild`, `ToolsForSession(connID)`, per-session call-time
  gate, `update_api_exposure`/`update_session_api_exposure`/
  `clear_session_api_exposure`/`api_exposure`, introspection annotations,
  exposure-aware dispatch/`run_task`; `ResolveTool`/`executeRegisteredTool`
  script branch; `/ui`, `/ui/manifest`, `/ui/chat` handlers + browser session
  bridge (`web_session.go`) with view-event streaming + dashboard auto-display
  on task completion.
- `webui/` (new): React + CopilotKit SPA, `webui/dist` embedded; view serializer
  (table/list/cards/chart) + input controls that re-issue backing calls.
- `cmd/openapi-mcp/main.go`: flags `--ui`, `--ui-token-env` (wired to
  `server.ui`).
- Docs: this document (kept as the development guide after implementation),
  README, `newsession`/anthropic note in development.md.

## 6. Implementation phases

1. **Phase 1 — Meta KB (spec→running)**: config + model kinds + `_meta` virtual
   entry + `meta_init`/`meta_status`/`meta_sync` + `kb:` links + `_meta` accepted
   by existing knowledge tools. Tests: config validation, link resolution, tool
   naming with `_meta`.
2. **Phase 2 — Dynamic exposure**: parser keeps the full spec (filtering becomes
   the allow-set), `ExposureConfig` + `apiEntry.Exposure` + `sessionExposure`,
   `OpTags`, expose-filter in `rebuild`, `ToolsForSession(connID)` +
   per-session call gate, `update_api_exposure` / `update_session_api_exposure` /
   `clear_session_api_exposure` / `api_exposure` + introspection annotations,
   exposure-aware dispatch + `run_task` hints, and `notifyToolsListChanged` after
   every mutation. Tests: overlap of allow-set×baseline×session-overlay, mode
   `none`, tag/path-prefix resolution, tool-not-exposed error, `tools/list_changed`
   broadcast, **two concurrent sessions with non-leaking per-session tool lists +
   `DropSession` cleanup of overrides**, and a manual opencode verify (§3.7).
3. **Phase 3 — Scripting core**: `pkg/script` executor + `mcp` module +
   `script_list`/`script_describe` + script→tool registration (exposure-aware) +
   `run_task` dispatch + initial agent-directive templates. Tests: executor
   sandbox defaults, param typing, tool schema, collision rejection, tracing
   hook.
4. **Phase 4 — Scripting hardening**: privileged modules (`os`/`exec`/`fs`/`http`)
   with allowlists, time budget, compiled-cache invalidation on edit, review +
   promotion of scripts, `memorize`.
5. **Phase 5 — Web UI shell**: `webui/` SPA (chat shell only) + embedded
   bundle + `/ui`/`/ui/manifest`/`/ui/chat` bridge + a browser session that
   authenticates against the registry and streams notifications (incl.
   `tools/list_changed`). Tests: bridge dispatches tools, manifest reflects
   registry + exposure, and **per-session isolation** (§4.2): two concurrent
   sessions get distinct chat streams and disjoint overlays/session targets,
   a broadcast notification renders once per session without leaking into either
   chat timeline, and `DropSession` on one tab frees only that session's state.
6. **Phase 6 — Generative UI**: CopilotKit GenUI components for tool calls,
   scripts, knowledge results, `run_task` plans and exposure changes;
   context-dependent landing view from the manifest; polish + docs.
7. **Phase 7 — Views and dashboards (session mirror)**: `view`/`dashboard`
   model kinds + templates, the `view` tool (source resolution + input
   binding + render payload), result→view wrapper emitting `view` events for
   every tool/task/script outcome, dashboard auto-display on task completion,
   natural-language "save as dashboard" persisting through `knowledge_upsert`,
   and the front-end serializer + interactive input controls. Specifically
   exercised end-to-end: the "users and their actions for the last two hours"
   flow (declare → render → save as dashboard → reopen) and a dashboard whose
   pagination/search/sort inputs re-issue the backing call. Tests: `ViewInput`
   binding resolution, render payload shape for tool/task/script results,
   dashboard doc→view resolution, `run_task` emitting a `view` event on its
   auto_show source, and session-mirror cases (two sessions see only their own
   results/inputs).

Each phase ships unit tests following the existing patterns (`pkg/config`,
`pkg/knowledge`, `pkg/server`, plus `pkg/script`), and a manual smoke test: for
Phase 1–4, register the weather API, teach `_meta` a disk-space script, slim the
footprint to one tag and call `_meta__free_disk_space` + a reactivated op; for
Phase 5–6, open `/ui` and drive a chat tool call end-to-end. Phase 7 adds the
"users and actions" dashboard to the smoke flow.

## 7. Open questions (resolve before Phase 2/4)

1. Exact tengo version pin (`github.com/d5/tengo/v2` latest) and whether
   `mcp.call` needs async/tool-call streaming support for long-running API ops.
2. Whether per-API scripts should be allowed for any `kind: script` in an API's
   KB or only in `_meta` (spec allows both; default config may restrict to
   `_meta` for safety).
3. `/ui/chat` bridge: drive `dispatchJSONRPC` directly (preferred) vs. speak the
   MCP protocol over an internal socket — decide based on session-overlay wiring
   reuse.
4. CopilotKit version to pin and whether to use its cloud runtime or the local
   bridge only (spec: local bridge only, revisit if streaming complexity grows).
5. Exposure defaults: keep `mode: all` per API (backward compatible) or offer a
   global default (`server.exposure.default_mode`) that new registrations inherit,
   so operators can ship minimal-footprint registries out of the box.
6. Whether the global baseline's deactivations are a hard floor or soft
   (sessions may re-activate within the allow-set; spec defaults to **soft**,
   allow-set is the only hard boundary). Consider an optional per-API
   `exposure.locked: true` for hard global deactivations.
7. How much of a view's layout/chart spec lives in the `view` doc vs. the
   front-end (spec: the doc declares `layout` + `inputs`, the serializer decides
   details). Whether rich dashboard widgets need a declarative mini-language in
   the KB (e.g. chart series, grouping) or if enumerated layouts + inputs suffice
   for Phase 7.
## 8. Implementation progress

Tracked against §6. Each item links the working changes that shipped it.

### Completed

- **Phase 1 — Meta KB** (commits `985ed9c`, part of `417653a`): `_meta`
  virtual entry, `meta_init` / `meta_status` / `meta_sync`, `kind: view`/
  `kind: dashboard` model kinds in `pkg/knowledge` (`model.go`, `links.go`,
  `store.go`, `templates.go`, `backend.go`), knowledge tools accepting `_meta`,
  `kb:` links.
- **Phase 7 (model + view tool + run_task auto-display)** (commits `417653a`,
  `ea3aff1`, and the run_task auto-show work):
  - `pkg/knowledge/model.go`: `KindView`/`KindDashboard`, `View`/`ViewInput`
    blocks (source, inputs, layout, auto_show), `Doc.View`, `Serialize`
    round-trip, `Library.Views()`, overlay/per-path placement for
    `views/…` and `dashboards/…` (`overlayPathFor`).
  - `pkg/server/knowledge_tools.go` + `knowledge.go`: the `view` tool
    (`api`, `scope`, `view_id`, `inputs`) — resolves the doc (session overlay
    first, then library by id or scoped path), fills declared inputs
    (provided → default → ""), rejects missing required inputs, resolves
    `view.source` to the registered tool and executes the backing call, then
    returns a structured render payload (`view_id`, `kind`, `title`, `summary`,
    `source`, `layout`, `auto_show`, `inputs`, `columns`/`rows`/`count`, or raw
    `text` for unknown shapes, plus `resolved_tool`/`status_code`/
    `call_error`/`source_unresolved`). Tool name in the payload uses the full
    MCP name (`<api>__<operationId>`, never string-surgery).
  - `pkg/server/tasks.go`: `run_task` auto mode appends an
    `--- auto-displayed views ---` block for every dashboard/view in the
    merged library whose `view.source` was executed by the task and whose
    `auto_show: true` (via `autoShowViews` + `Library.Views()`), rendering each
    through the same `RenderView` path so the backing call is re-issued with
    the bound inputs.
  - Tests: `TestSerializeViewRoundTrip` (knowledge), `TestViewToolRendersDashboard`
    (render payload shape, input binding, required-input rejection, dashboard
    doc→view resolution) and `TestRunTaskAutoDisplaysBoundDashboard`
    (run_task emits the bound dashboard on completion, non-matching dashboards
    are excluded, backing call re-issued) in `pkg/server`.
- **Phase 2 — Dynamic exposure**: the parser is non-destructive (always emits
  the full `ToolSet`/`ApiDoc`; the parser filtering tests are restated as "the
  filters do not drop operations"), and the include/exclude config is
  re-interpreted as the API's hard allow-set (`allowSetAllows`). Implemented:
  - `pkg/config`: `ExposureConfig` (active *bool, mode all|none, active/disabled
    tags+ops), `ActiveEnabled()`, `NormalizeDefaults()`, `Exposure` on
    `APIDefinition` (persisted in the config file).
  - `pkg/server/registry.go`: `apiEntry.OpTags` (opID→tags built from the
    ApiDoc at load), `apiEntry.Exposure` (normalized global baseline),
    `Registry.sessionExposure[connID][api]` (seeded in `NewRegistry`, cleaned in
    `DropSession`), `allowSetAllows`/`exposureExposesOp`/`opExposedLocked`/
    `opExposedForSession`/`opStatusForSession`, rebuild filters the *snapshot*
    by the baseline while the `index` keeps every tool (ResolveTool stays
    global), `ToolsForSession(connID)`, `IsToolExposedForSession`,
    `UpdateAPIExposure` (rebuild+persist+broadcast), `UpdateSessionAPIExposure` /
    `ClearSessionAPIExposure` (ephemeral), `APIExposure`/`exposureReportForSession`,
    `exposurePatch` (+appendUnique/removeString), session-aware
    `APIsForSession` annotations, and `liveEntry`.
  - `pkg/server/server.go`: `tools/list` answered per connection via
    `ToolsForSession(connID)` (`handleToolsListJSONRPC`), and a per-session
    call-time gate in `executeRegisteredTool` that rejects registered-but-hidden
    tools with the activation hint (no string surgery; resolved via the index).
  - `pkg/server/management.go`: tools `update_api_exposure`,
    `update_session_api_exposure`, `clear_session_api_exposure` and
    `api_exposure` (+ schemas and `exposurePatchFromArgs`/`exposureConfigFromArgs`);
    `register_openapi_api` accepts an `exposure` object; introspection annotates
    the session view (`list_openapi_apis` adds `exposed_tools`/`active`/`mode`/
    `session_override`, `describe_openapi_api`/`search_openapi_operations` mark
    each endpoint `active`, `get_api_operation` reports exposure, and
    `export_openapi_config` includes the global `exposure` baseline).
  - `pkg/server/tasks.go`: `run_task` dry-run flags steps whose tool is not
    exposed to the session (`not exposed in this session — enable with
    update_session_api_exposure`); auto mode fails fast through the call gate.
  - Tests (`pkg/server/exposure_test.go`): defaults expose-all, global
    deactivate/reactivate, mode `none` + tag footprint + `all` reset, session
    overrides that neither leak (`sessA` vs `sessB`) nor survive `DropSession`,
    widening within the allow-set, allow-set as a hard boundary
    (`excluded-by-config` cannot be activated), call-time gate, broadcast of
    `tools/list_changed` after global and session mutations, report shape,
    session-aware introspection, API registration from the `exposure` config,
    and per-session `tools/list` through the JSON-RPC handler.
- **Phase 3 — Scripting core**: scripts are `kind: script` knowledge docs
  (`scripts/*.tengo` with a `// ---` comment front-matter, or `*.md` with a
  fenced tengo body) surfaced as MCP tools and executed in a sandboxed tengo VM.
  Implemented:
  - `go.mod`: `github.com/d5/tengo/v2 v2.17.0`.
  - `pkg/script` (new): `Executor`/`Options`/`Host`, `Run(ctx, src, args, host)`
    with a per-run time budget (default 10s) + tengo `SetMaxAllocs` guard, and
    return normalization (nil/undefined → "", string verbatim, else JSON). The
    source is wrapped in an IIFE so a top-level `return` is legal. Safe stdlib
    modules (math/text/times/rand/base64/hex/json/fmt/enum) are always imported;
    the host modules `mcp` (call/resolve), `os` (getenv/hostname/getwd/environ),
    `exec` (allowlisted `run`), `fs` (root-constrained read/stat/list) and
    `http` (allowlisted get/post) are default-deny and gated by the declared
    `permissions` plus `meta.scripting` allowlists. The compile cache is
    partitioned by `(sourceHash, Host.Key)` so per-session `mcp` bindings never
    leak across sessions.
  - `pkg/knowledge`: `ParseScriptDoc` (both encodings), `Doc.Source`, the
    list-or-map `Permissions` type with `Has`, extended `Param`
    (type/description/default) and `Doc.TimeoutS`; `LoadLocal`/`LocalBackend`
    index `.tengo`; `Library.Scripts()`; `.tengo` sources skip Markdown link
    validation; `ScriptSkeleton` + a scripts section in the index manual.
  - `pkg/config`: `MetaConfig.Scripting` (`exec_allowlist`, `fs_read_roots`,
    `http_allowlist`, `default_timeout_s`) with `DefaultTimeout()`.
  - `pkg/server`: `scriptRef` + `Registry.scriptTools`; `refreshScriptsLocked`/
    `RefreshScripts` re-scan libraries on knowledge load and rebuild the
    snapshot; `rebuild` merges script tools (collision with operations /
    management tools is a hard error); `ToolsForSession`/`IsToolExposedForSession`
    gate per-API scripts by the synthetic `script` tag (meta scripts always
    exposed); `RunScript`, `ToolInfo`, `CallTool` (the unified management/
    script/operation bridge used by the `mcp` module); `ListScripts`/
    `DescribeScript` + the `script_list`/`script_describe` management tools; the
    JSON-RPC handler routes script tools to `toolResultFromText`; `run_task`
    marks script steps `(static script)` and executes them through `RunScript`.
  - Tests: `pkg/script/executor_test.go` (returns, params, safe stdlib, denied
    module, mcp bridge, timeout, exec/fs/http allowlists) and
    `pkg/server/scripts_test.go` (registration/dispatch, mcp bridge to a
    management tool, denied `os`, global + per-session exposure isolation,
    operation-name collision, unregister cleanup, `run_task` dry-run/auto,
    exposure-report script totals).
  - Introspection: `api_exposure`/`update_*_exposure` and `list_openapi_apis`
    now include scripts (the synthetic `script` tag and `"kind":"script"`
    entries) in their totals and per-operation lists, so the footprint report
    matches what `tools/list` actually serves.
  - Verified end-to-end over JSON-RPC (`knowledge_load` → `tools/call` a script
    → `mcp.call` chaining) and under Delve per `docs/ai_debugging.md`
    (breakpoints at the handler branch, `RunScript`, the `mcpCall` bridge and
    result normalization; the 10s budget correctly fires when debug pauses
    consume it).
- **Phase 4 — Scripting hardening**: implemented:
  - `pkg/script`: the compile cache is now an LRU bounded by
    `Options.CacheSize` (default `DefaultCacheEntries = 64`), so a long-lived
    process cannot grow it without bound; `TestExecutorCacheBounded` pins the
    eviction + recompile behavior.
  - `script_list`/`script_describe` now report `timeout_s` and `intents` (plus
    permissions/params/tags/exposed as before) via `scriptView`.
  - Discovery hints: `Registry.MatchingScripts` scores registered scripts
    (per-API + `_meta`) against a free-text query by id/intents/summary/tags, and
    `knowledge_search`, `discover_task` and `knowledge_clarify` surface a
    `related_scripts` / `scripts` list so an agent reuses a script instead of
    re-deriving the steps.
  - `knowledge_promote_script`: turns a draft capability (created by
    `knowledge_remember_sequence` from a recorded session sequence) into a
    `kind: script` doc under `scripts/<id>.tengo` that replays its steps via
    `mcp.call`; preview with `confirm=false`, write with `confirm=true`, then it
    re-indexes and the script becomes a tool. `generateScriptFromCapability`
    maps `param.*` bindings to the injected `params` map and `step.N.*` bindings
    to the previous call's result.
  - Tests: `TestScriptViewSurfacesTimeoutAndIntents`,
    `TestScriptDiscoveryHints`, `TestScriptRunFoldsIntoCapabilityDraft`,
    `TestPromoteSequenceToScript` (+ the cache test above).

### In progress / next

- Phase 7 remainder: result→view wrapper firing `view` events on the session
  stream (needs the Phase 5/6 web-session bridge for delivery), dashboard
  auto-display surfaced through `/ui`, and the front-end serializer + input
  controls re-issuing backing calls.
- Phase 5 — Web UI shell (CopilotKit) and Phase 6 — generative UI, as planned in
  §6/§4: the per-session isolation, manifest-driven exposure surface, session
  mirror and view/dashboard rendering are already in place, so the remaining work
  is the front-end shell that consumes them.

### Resume here (next working session)

**Status:** Phases 1 (meta KB), 2 (dynamic exposure), 3 (scripting core) and 4
(scripting hardening) are implemented, tested and committed to `main`; the Phase 7
view/dashboard model is done pending the Phase 5/6 UI bridge (§8 above).
`pkg/script` (`executor.go`/`modules.go`), `pkg/knowledge/script.go`,
`pkg/server/scripts.go` and their `_test.go` files are the main additions.

**Next up: Phase 5 — Web UI shell (§4).** Suggested order:

1. Stand up the CopilotKit shell that talks to the streamable-HTTP MCP and the
   session mirror; reuse `pkg/server`'s `view`/`dashboard` renders.
2. Wire per-session isolation (`X-Connection-ID` / `sessionId`) end-to-end so
   `update_session_api_exposure` and session-active targets work from the UI — note
   that stateless streamable HTTP currently yields `connID=""`, so session-scoped
   tools need the legacy SSE connection id (or a new session header) plumbed.
3. Surface `api_exposure` (which now includes scripts) as the UI's tool-footprint
   control, and the script tools/`script_list` as first-class actions.

**Working notes (Phase 3/4 were developed here):**

- `pkg/script` is intentionally independent of `pkg/server`/`pkg/knowledge`:
  callers inject the host bridge through `script.Host` at run time, and the
  compile cache is an LRU partitioned by `(sourceHash, Host.Key)` (the session
  id) so a script that uses `mcp` never shares compiled bytecode across sessions.
- Scripts are resolved through `Registry.scriptToolFor` (not `ResolveTool`,
  which stays operation-only) and dispatched by `Registry.RunScript`; the
  per-session exposure gate uses the synthetic `script` tag (`scriptTag`).
- A script colliding with an operation or management tool is a hard
  registration error from `rebuild`.
- Always resolve tool names through `toolFullName`/the registry maps
  (`ResolveTool`/`scriptToolFor`/`IsToolExposedForSession`) — capped/truncated
  names must never be re-derived by string surgery.
