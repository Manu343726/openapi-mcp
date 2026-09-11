package server

import (
	"encoding/json"
	"strings"

	"github.com/ckanthony/openapi-mcp/pkg/logx"
)

// This file implements the result→view wrapper: every session tool outcome is
// projected into a structured "view" payload and pushed as a
// notifications/view JSON-RPC notification on the calling session's stream
// (the /ui/events bridge and any MCP SSE client). The projection is
// best-effort: an unknown shape degrades to a markdown block, never an error.

var viewLog = logx.Module("view")

// maxViewText bounds the inline text carried in a markdown/fallback view event.
const maxViewText = 8000

// viewParams builds the structured view payload for a completed tool call.
// Callers must not mutate the returned map.
func (r *Registry) viewParams(toolName string, payload ToolResultPayload) map[string]interface{} {
	text := ""
	if len(payload.Content) > 0 {
		text = payload.Content[0].Text
	}
	params := map[string]interface{}{
		"tool":  toolName,
		"kind":  r.toolKind(toolName),
		"error": payload.IsError,
		"text":  truncate(text, maxViewText),
	}
	if payload.StatusCode != 0 {
		params["status_code"] = payload.StatusCode
	}

	// A `view` tool result is already a render payload: pass it through.
	if toolName == ToolView {
		var render map[string]interface{}
		if err := json.Unmarshal([]byte(text), &render); err == nil && render["view_id"] != nil {
			params["view"] = render
			if layout, ok := render["layout"]; ok {
				params["layout"] = layout
			}
			if cols, ok := render["columns"]; ok {
				params["columns"] = cols
			}
			if rows, ok := render["rows"]; ok {
				params["rows"] = rows
			}
			if count, ok := render["count"]; ok {
				params["count"] = count
			}
			return params
		}
	}

	// Otherwise project JSON payloads into a table (arrays of objects) or a
	// key/value list (a single object); anything else falls back to markdown.
	trimmed := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(trimmed, "["):
		if cols, rows := rowsFromJSON([]byte(text)); cols != nil {
			params["layout"] = "table"
			params["columns"] = cols
			params["rows"] = rows
			params["count"] = len(rows)
			return params
		}
		params["layout"] = "markdown"
	case strings.HasPrefix(trimmed, "{"):
		if isJSONObject(text) {
			params["layout"] = "list"
			return params
		}
		params["layout"] = "markdown"
	default:
		params["layout"] = "markdown"
	}
	return params
}

// toolKind classifies a fully qualified tool name for rendering.
func (r *Registry) toolKind(toolName string) string {
	if toolName == ToolView {
		return "view"
	}
	if _, ok := r.scriptToolFor(toolName); ok {
		return "script"
	}
	if _, _, ok := r.ResolveTool(toolName); ok {
		return "operation"
	}
	if r.IsManagementTool(toolName) {
		return "management"
	}
	return "unknown"
}

// emitToolResultView pushes the structured view for a completed call onto the
// session stream. connID == "" (stateless client) is a no-op.
func (r *Registry) emitToolResultView(connID, toolName string, payload ToolResultPayload) {
	if connID == "" {
		return
	}
	enqueueView(connID, r.viewParams(toolName, payload))
}

// enqueueView sends a notifications/view notification to one session's stream.
// Delivery is best-effort (dropped when the channel is full), like other
// broadcasts.
func enqueueView(connID string, params map[string]interface{}) {
	connMutex.RLock()
	ch, ok := activeConnections[connID]
	connMutex.RUnlock()
	if !ok {
		viewLog.Debug("no session stream for view event", "conn_id", connID)
		return
	}
	notification := jsonRPCResponse{
		Jsonrpc: "2.0",
		Method:  "notifications/view",
		Params:  params,
	}
	select {
	case ch <- notification:
	default:
		viewLog.Warn("dropped view event (channel full)", "conn_id", connID)
	}
}

// isJSONObject reports whether text parses to a JSON object.
func isJSONObject(text string) bool {
	var v interface{}
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return false
	}
	_, ok := v.(map[string]interface{})
	return ok
}
