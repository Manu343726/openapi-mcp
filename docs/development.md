# Development notes & known quirks

Things to keep in mind when touching this codebase. None of these are bugs to
"fix" blindly; they are deliberate constraints that the tests and behavior rely
on.

## OpenAPI 3.1 support

- OpenAPI 3.1 specs (e.g. FastAPI `/openapi.json`) require `kin-openapi`
  v0.149.0 and Go 1.25 — see `go.mod` and `Dockerfile` (`GO_VERSION=1.25`).
- `pkg/parser/parser.go` `validationOpts()` passes `AllowExtraSiblingFields(...)`.
  kin-openapi v0.149 validates OpenAPI 3.0 strictly and rejects `$ref` with
  sibling fields (`description`, `readOnly`, `type`, …). The allow-list keeps
  real-world 3.0 specs (mofli, mortimer) loadable while 3.1 documents relax
  automatically via `IsOpenAPI31OrLater`. Do not remove the allow-list without
  re-testing those specs.

## Tool names

- `pkg/server/registry.go` `maxToolNameLen = 100` and `capToolName`:
  fully-qualified tool names (`<api>__<operationId>`) are truncated to 100 chars
  with a deterministic sha1 suffix. Model providers reject function names longer
  than 128 chars and clients (opencode) prefix every MCP tool with `<server>_`.
  Tool lookups (`ResolveTool`, the `tools/call` index) use the truncated names
  consistently — always route via `toolFullName`, never re-derive names by
  string surgery.

## Parameter serialization (`pkg/server/server.go`)

- `formatScalar`: `encoding/json` decodes every JSON number as `float64`, and
  Go's default `%v` renders large integers in scientific notation (e.g.
  `1789037393` → `1.789037393e+09`), which REST APIs reject. Integral floats are
  emitted as plain integers; other floats use `'f'` (no exponent). Required for
  epoch-style parameters like acme's `StartDateTime`/`EndDateTime`.
- Array query/header/cookie parameters are sent as **repeated** values
  (`sites=1&sites=2`), never as a single `[a b]` literal
  (`paramValueStrings` / `appendParamValues` / `appendHeaderValues`).

## Semantic knowledge layer (`pkg/knowledge`, `pkg/server/{knowledge,tasks}.go`)

- **Lazy-load**: `apiEntryFor` indexes the Markdown library on first knowledge
  read after a restart. The root is `KnowledgeConfig.Root` or
  `<configDir>/knowledge/<api>`. Knowledge reads fail with a clear error when
  `knowledge.enabled=false`; they do not silently no-op.
- **Overlay is per connection**: `sessionKnowledge[connID]` is cleared on
  `DropSession`. `knowledge_upsert` defaults to `persist=false` (overlay).
- **`run_task` binding semantics**: `{from: param.x}` is required unless the
  parameter is declared `required: false` in the capability front-matter, in
  which case a missing value is omitted (optional HTTP params). Undeclared or
  required-but-missing params fail the step.
- **`run_task` JSONPath is a subset**: `$.a.b[0].c`, `$..deep.field`, `[*]`.
  No filters, functions or expressions.
- **`knowledge_review`**: amends overlay docs in place; for library (persisted)
  docs it writes the amendment back to the manual (local backend) and re-indexes.
- **Git backend** (`knowledge.backend.type: git`) is explicitly unimplemented and
  returns a clear error. `knowledge_load`/`knowledge_init`/`update_api_knowledge`
  all guard against it.

## MCP protocol (`pkg/server/server.go` `dispatchJSONRPC`)

- Standard notifications (including `notifications/cancelled`) are accepted
  silently — they carry no response.
- `resources/*`, `prompts/*` and `completion/complete` are wired but **not
  implemented**: they reply with JSON-RPC `-32001 Method not implemented`.
  Never fabricate empty payloads for them; implement the real features in those
  cases when they land.
- Unknown methods that start with `notifications/` are ignored; other unknown
  requests still log a warning and return `-32601`.