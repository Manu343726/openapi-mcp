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