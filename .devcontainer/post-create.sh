#!/usr/bin/env bash
# Runs after the devcontainer is created: builds the MCP server binary via the
# Makefile and starts it in the background so opencode (in-container or on the
# host) and VS Code can connect to it over SSE at
# http://localhost:${OPENAPI_MCP_PORT}.
set -euo pipefail

cd /workspace

echo "==> Building openapi-mcp via Makefile"
make deps
make build

echo "==> Starting MCP server on :${OPENAPI_MCP_PORT}"
mkdir -p /workspace/.config
# --config tells the server to persist runtime registrations (APIs/targets
# added via MCP tools) to a JSON file so they survive restarts.
nohup /workspace/bin/openapi-mcp \
  --config /workspace/.config/config.yaml \
  --port "${OPENAPI_MCP_PORT}" \
  > /workspace/.config/openapi-mcp.log 2>&1 &

echo "==> MCP server pid $!"
echo "==> Logs: /workspace/.config/openapi-mcp.log"
