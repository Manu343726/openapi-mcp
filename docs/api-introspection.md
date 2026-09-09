# API introspection & documentation

Registered APIs can be inspected through dedicated MCP management tools, and
every tool generated from an API is **self-describing**: its description embeds
the underlying endpoint, the operation, and the input requirements. Combined,
these let an AI agent understand an API and use it without reading the raw
OpenAPI document.

## The self-referencing model

Two views of an API are kept in sync:

- **REST/OpenAPI level** — the parsed spec: endpoints (method `GET /orders`),
  parameters, DTO schemas (`components.schemas` / `definitions`), auth schemes.
- **MCP tool level** — each operation becomes a tool named `<api>__<operationId>`
  whose description includes that same endpoint, its parameter locations/types
  and required flags.

So:

- an endpoint documentation entry always shows the exact `tool_name` to call;
- a tool description always shows the exact `Endpoint: <METHOD> <path>` it
  invokes, the operation, and its parameters.

## Management tools

| Tool | Purpose |
|------|---------|
| `describe_openapi_api` | Full API documentation: info (title/version/description), servers, tags, every endpoint (method, path, summary, description) mapped to its MCP tool, the DTO schemas, and the API's auth + target configuration. |
| `get_api_operation` | Detailed docs for one endpoint by operationId (or full tool name `<api>__<op>`): method/path, parameters (location, required, type), request body schema, response schemas, and the exact MCP tool to call. |
| `list_api_schemas` | List an API's DTO schemas by name (compact), expand a single schema's full property tree (`name=Order`), or expand all (`expand=true`). |
| `check_api_spec` | Compare the timestamps the API's spec was loaded at against the current source (`Last-Modified` / file mtime): reports `up-to-date`, `outdated`, or `unknown`. |
| `reload_api` | Re-read an API's spec from its source and regenerate its tools from scratch (targets + active target preserved). Reports whether the previous load was stale. |

## Spec timestamp tracking

When an API is registered the server records the *last-modified time of the spec
source* (file modification time, or the `Last-Modified` header for HTTP sources):

- it is exposed as `spec_timestamp` on `list_openapi_apis`,
  `describe_openapi_api` and `export_config`;
- `check_api_spec {api: x}` compares it against the source's *current* timestamp
  and warns whether the in-memory toolset is stale (`outdated`) — i.e. the spec
  was modified after the MCP server loaded it;
- `reload_api {api: x}` re-reads the spec from its source and rebuilds the tools,
  so changes to a spec file/URL are picked up without restarting the server.

`spec_timestamp` is reported as `unknown` when no source timestamp is available
(e.g. an inline spec, or an HTTP source that omits `Last-Modified`).

## Spec monitoring

An API can opt into watching its spec source via its `monitoring` config
(`monitoring.enabled` / `monitoring.auto_reload`, see `config-file.md`). When
enabled, a background watcher polls the source and, on change:

- sends a `notifications/message` log event (level `notice`, logger
  `openapi-mcp.monitoring`, data `{api, source, changed_at, auto_reload}`) over
  the standard MCP **logging** channel — the protocol's intended way for a server
  to push event info to clients, which the client may surface to the user/agent;
- the event is only delivered to clients that opted in via `logging/setLevel` and
  whose configured minimum level includes `notice`;
- if `auto_reload` is enabled, it then re-loads the spec (regenerating the
  tools), which also broadcasts the standard `notifications/tools/list_changed`
  so clients immediately re-fetch the updated tool list.

Clients can always check freshness explicitly with `check_api_spec {api}` and
force a refresh with `reload_api {api}` even when monitoring is off.

## How it maps

Example spec:

```yaml
paths:
  /orders:
    get:
      operationId: listOrders
      parameters:
        - name: page
          in: query
          schema: { type: integer }
components:
  schemas:
    Order: { type: object, properties: { id: { type: string } } }
```

Registered as API `orders`:

- `tools/list` returns a tool named `orders__listOrders` whose description
  includes `Endpoint: GET "https://orders.example.com/v2/orders"` and
  `page (query, optional): integer`.
- `describe_openapi_api {api: orders}` returns an `endpoints` entry with
  `operation_id: listOrders`, `tool_name: orders__listOrders`, `method: GET`,
  `path: /orders`, plus the `Order` schema under `schemas`.
- `get_api_operation {api: orders, operation: listOrders}` returns the full
  operation detail and `tool_name: orders__listOrders`.
- `list_api_schemas {api: orders, name: Order}` returns the `Order` DTO with
  its properties.

References (request/response bodies pointing to components) are resolved so the
agent sees the actual DTO shape, not a `$ref` string.

## Output

All three tools return JSON (via the text content channel) so agents can parse
the structure reliably. Credentials and API keys are **never** included in any
introspection output.