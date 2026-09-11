# Development notes

How to build, test, and exercise the MCP server, plus the known quirks and
deliberate constraints that the tests and behavior rely on. None of these quirks
are bugs to "fix" blindly — they are deliberate.

## Prerequisites

- **Go 1.25+.** The module requires `go 1.25.0` (OpenAPI 3.1 support via
  `kin-openapi` v0.149.0). Use one of the versioned toolchains if the base image
  is older, e.g.:
  ```sh
  go install golang.org/dl/go1.25.0@latest
  go1.25.0 download
  # then "go1.25.0 test ./..." replaces "go test ./..."
  ```
  `Dockerfile` builds with `GO_VERSION=1.25`.
- The `Makefile` targets assume a Go toolchain that supports the module version.

## Build, run, test

```sh
make deps        # go mod download
make build       # CGO_ENABLED=0 go build -o bin/openapi-mcp ./cmd/openapi-mcp
make run         # build + run on --port 8080 (no config file)
make run-server  # build + run against .config/config.yaml (persists runtime registrations)
make test        # go test ./...  (see the timeout caveat below)
make clean
```

Directly:

```sh
go build ./...                       # compile check without emitting a binary
bin/openapi-mcp --port 8080
bin/openapi-mcp --config .config/config.yaml --port 8086
bin/openapi-mcp --log-level debug    # structured logs on stdout
```

### Test timeout caveat

`go test ./...` may hang without a network / longer timeout: one package's tests
need internet/SSE. Run the suite with a generous timeout if you hit it:

```sh
go test -timeout 300s ./...
```

If a run hangs, identify the offending package (`go test ./...` with `-v`, or
bisect `go test ./pkg/...` one package at a time) and give it `-timeout
600s` or network access. The parser, config, knowledge, and logx packages are
safe to run offline (`go test ./pkg/parser/... ./pkg/config/... ./pkg/knowledge/... ./pkg/logx/...`).

## Testing the MCP end-to-end

The server speaks JSON-RPC 2.0 over two transports — legacy SSE and streamable
HTTP, both under `/mcp`. Start it with a spec to test against, then issue raw
JSON-RPC requests. No MCP client is required.

### 1. Start the server with a test spec

Pick a spec from the reference list below, then run:

```sh
# remote spec via config file
cat > /tmp/test-config.yaml <<'EOF'
server:
  port: 18080
apis:
  - name: petstore
    source: https://petstore3.swagger.io/api/v3/openapi.json
    targets:
      - name: default
        base_url: https://petstore3.swagger.io/api/v3
EOF
bin/openapi-mcp --config /tmp/test-config.yaml
```

Or start with no config and register the API at runtime through MCP tools
(`register_openapi_api`), which persists everything back to the config file.

### 2. Hand-roll JSON-RPC over streamable HTTP

```sh
# initialize
curl -s -X POST http://localhost:18080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"0.0.1"}}}'

# list the generated tools
curl -s -X POST http://localhost:18080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'

# call a tool (petstore)
curl -s -X POST http://localhost:18080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"petstore__findPetsByStatus","arguments":{"status":"available"}}}'

# management / introspection tools are plain MCP tools too
curl -s -X POST http://localhost:18080/mcp -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"list_openapi_apis","arguments":{}}}'
curl -s -X POST http://localhost:18080/mcp -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"describe_openapi_api","arguments":{"api":"petstore"}}}'
```

`preview_api_call` is a dry run — it returns the exact HTTP request a tool call
would send (method, URL, headers, body) **without** executing it. Use it to
verify parameter serialization, target resolution, and auth header injection
before anything hits the network.

To debug the request the server would actually emit, use `preview_api_call` for
a fully offline check; to hit a real API, call the tool.

### 3. Reference OpenAPI specs for manual / integration testing

The [awesome-openapi-specs](https://github.com/bhavyshekhaliya/awesome-openapi-specs)
catalog lists real-world provider specs (it has no petstore-style stubs — those
are real APIs). Useful ones for testing this MCP, roughly ordered by usefulness:

| Provider | Spec | Notes |
|----------|------|-------|
| Petstore v3 | `https://petstore3.swagger.io/api/v3/openapi.json` | The classic test stub (not in the catalog); ideal for a first smoke test |
| U.S. National Weather Service | `https://api.weather.gov/openapi.json` | **No auth**, clean OpenAPI 3.1 JSON; great for validating 3.1 parsing and live calls |
| Discourse | `https://docs.discourse.org/openapi.json` | Free, community-run, 3.1 JSON |
| People Data Labs | `https://raw.githubusercontent.com/peopledatalabs/openAPI-specifications/master/pdl-specs.json` | 3.0 JSON, API-key auth |
| Geoapify (geocoding) | `https://raw.githubusercontent.com/geoapify/geoapify-openapi-specs/refs/heads/main/api-specs/geocoding/forward_geocoding.yaml` | 3.0 YAML, API-key auth |
| Open Education API | `https://raw.githubusercontent.com/open-education-api/specification/main/oeapi.yaml` | Very small, focused, YAML |
| ApostropheCMS | `https://raw.githubusercontent.com/apostrophecms/apostrophecms-openapi/main/apostrophecms-openapi.yaml` | CMS, no auth |
| Hostinger | `https://raw.githubusercontent.com/hostinger/api/main/openapi.json` | Small, focused, JSON |
| Stytch | `https://raw.githubusercontent.com/stytchauth/stytch-openapi/main/openapi.yml` | 3.0.3 auth-heavy spec — exercises `securitySchemes` inference |
| Paystack | `https://raw.githubusercontent.com/PaystackOSS/openapi/main/dist/paystack.yaml` | Payments, auth-heavy |
| Resend | `https://raw.githubusercontent.com/resend/resend-openapi/main/resend.yaml` | 3.1, modern YAML |
| Trello | `https://developer.atlassian.com/cloud/trello/swagger.v3.json` | Large 3.0 spec — good for tool-name truncation and scale checks |

Suggestions for what to test against each:

- **Parsing / validation** (smoke test): Petstore v3, NWS, OE API.
- **OpenAPI 3.1 path**: NWS (`/openapi.json`), Discourse, Resend.
- **Auth inference** (`securitySchemes` → API-level `auth`): People Data Labs,
  Geoapify, Stytch, Paystack. Register the spec, then `describe_openapi_api` and
  confirm the inferred auth in `export_openapi_config`.
- **Tool-name truncation / scale**: Trello (many operations), then verify the
  capped `<api>__<operationId>` names appear consistently in `tools/list`, tool
  descriptions, and `search_openapi_operations`.
- **Live reads** (no key needed): Petstore v3, NWS, Discourse, OE API.

Use `make run-server` and `register_openapi_api` at runtime to iterate without
editing the config file by hand.

## Known quirks

These are deliberate constraints — the tests and behavior rely on them.

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
  epoch-style parameters like `StartDateTime`/`EndDateTime`.
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
- **Git backend** (`knowledge.backend.type: git`): implemented via `GitBackend`
  (pull-rebase/ff_only, pending-push retention) and `knowledge_sync`. The checkout
  lives under the configured `root`; sync state is surfaced by `knowledge_status`.

## Scripting layer (`pkg/script`, `pkg/server/scripts.go`)

- A script is a `kind: script` knowledge doc: `scripts/<id>.tengo` (raw source,
  optional `// ---` comment front-matter) or `<id>.md` (YAML front-matter + a
  fenced ```tengo body). It is surfaced as `toolFullName(scope, id)`
  (`acme__<id>` / `_meta__<id>`) and executed by a tengo VM.
- **Sandbox**: safe stdlib modules are always available; the host modules
  `mcp`/`os`/`exec`/`fs`/`http` are default-**deny** and require a `permissions`
  declaration. `exec`/`fs`/`http` additionally need operator allowlists
  (`meta.scripting.{exec_allowlist,fs_read_roots,http_allowlist}`); an empty
  allowlist denies every call. Do not widen this without a test.
- **Guards**: each run has a wall-clock budget (default 10s via
  `meta.scripting.default_timeout_s`, overridable per doc with `timeout:`) and a
  tengo `SetMaxAllocs` allocation cap. The source is wrapped in an IIFE so a
  top-level `return` is valid.
- **Dispatch**: scripts are deliberately *not* in the operation index
  (`ResolveTool` stays operation-only). They live in `Registry.scriptTools` and
  are resolved with `scriptToolFor`; `RunScript` runs them, `CallTool` is the
  unified bridge (`mcp.call` reaches management tools, scripts and operations).
  Never re-derive script names by string surgery.
- **Exposure**: per-API scripts follow the owning API's runtime exposure, bucketed
  under the synthetic `script` tag; `_meta__*` scripts are always exposed. The
  `script_list`/`script_describe` tools inspect them.

## MCP protocol (`pkg/server/server.go` `dispatchJSONRPC`)

- Standard notifications (including `notifications/cancelled`) are accepted
  silently — they carry no response.
- `resources/*`, `prompts/*` and `completion/complete` are wired but **not
  implemented**: they reply with JSON-RPC `-32001 Method not implemented`.
  Never fabricate empty payloads for them; implement the real features in those
  cases when they land.
- Unknown methods that start with `notifications/` are ignored; other unknown
  requests still log a warning and return `-32601`.