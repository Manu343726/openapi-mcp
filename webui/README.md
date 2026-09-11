# webui — interactive web UI shell

This directory holds the **pre-built static bundle** served by the Go server at
`/ui`. It is committed so `go build` never needs Node (`webui/embed.go` embeds
`dist/` with `go:embed`).

The Phase 5 shell is dependency-free (plain HTML/CSS/JS):

- `dist/index.html` / `dist/style.css` / `dist/app.js`
- It reads `GET /ui/manifest` (session-aware registry context), calls tools with
  `POST /ui/chat`, and listens for broadcast notifications (e.g.
  `notifications/tools/list_changed`) on `GET /ui/events` via SSE.

Each browser tab gets its own opaque session token in `sessionStorage`, sent as
`X-Ui-Session` (or `?session=`), which the server maps to a dedicated MCP
`connID`. That gives every tab isolated per-session overlays, active targets and
exposure, exactly like a headless MCP client.

## Phase 6 (CopilotKit)

Phase 6 replaces/augments this bundle with a CopilotKit generative-UI front-end
(`@copilotkit/react-core`, `react-ui`, `react-textarea`). When that lands, add a
Node build (`make webui`) that emits into `dist/`; the Go bridge endpoints stay
the same. Keep `dist/` committed so the default `go build` path stays Node-free.
