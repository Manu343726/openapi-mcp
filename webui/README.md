# webui — generative UI

This directory holds the **pre-built static bundle** served by the Go server at
`/ui`. It is committed so `go build` never needs Node (`webui/embed.go` embeds
`dist/` with `go:embed`). Rebuild it with `make webui` (requires Node 20+).

## What it is

A React 18 + Vite 5 + TypeScript front-end built on **CopilotKit v2 (AG-UI)**:

- `<CopilotKit agentId="default" runtimeUrl="/ui/copilotkit"
  headers={{ "X-Ui-Session": <token> }}>` + `CopilotChat` — the chat drives the
  server's deterministic AG-UI runtime (`GET /info`, `POST /agent/default/run`,
  `/connect`, `/stop/{thread}`), which streams `TEXT_MESSAGE_*` and
  `TOOL_CALL_*` events plus a `CUSTOM "view"` event per tool call.
- GenUI component map (`src/components/GenUI.tsx`): `useComponent` renderers for
  the `view` and `run_task` tool calls (zod parameter schemas) rendered inline in
  the timeline.
- Landing header (`src/components/Landing.tsx`): API/tool/script/knowledge
  counts from `GET /ui/manifest`.
- Results pane (`src/components/ResultsPane.tsx`): `EventSource /ui/events`
  streams the session's `view` notifications; `ViewRender` renders
  table/list/markdown/error cards.

Vite `base` is `/ui/` so embedded assets resolve under `/ui`. Dev mode
(`npm run dev`) proxies `/ui` to a local server on :8080.

## How the session token flows

Each browser tab mints an opaque session token (kept in memory +
`sessionStorage`) and sends it as `X-Ui-Session` on every request. The server
maps it to a dedicated MCP `connID`, giving every tab isolated per-session
overlays, active targets and exposure — exactly like a headless MCP client.

## Layout

- `src/main.tsx`, `src/App.tsx` — bootstrap, CopilotKit + CopilotChat wiring.
- `src/components/` — GenUI cards, results pane, landing, view renderer.
- `src/lib/session.ts`, `src/state/results.tsx` — token/manifest helpers and the
  `/ui/events` results store.
- `dist/` — committed build output (rebuild via `make webui`).

## Scripts

- `npm run dev` — Vite dev server (proxies `/ui` → `localhost:8080`).
- `npm run build` — typecheck + production build into `dist/`.
- `npx tsc --noEmit` — typecheck only.