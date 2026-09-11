package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func payloadOf(text string, isErr bool) ToolResultPayload {
	return ToolResultPayload{
		Content: []ToolResultContent{{Type: "text", Text: text}},
		IsError: isErr,
	}
}

func TestViewParamsTable(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, _ := newScriptRegistry(t, specPath)

	params := reg.viewParams("acme__getCurrent", payloadOf(`[{"a":1,"b":"x"},{"a":2,"b":"y"}]`, false))
	assert.Equal(t, "table", params["layout"])
	assert.Equal(t, "operation", params["kind"])
	assert.Equal(t, 2, params["count"])
	assert.ElementsMatch(t, []string{"a", "b"}, params["columns"])
	rows := params["rows"].([]map[string]interface{})
	require.Len(t, rows, 2)
}

func TestViewParamsMarkdownFallback(t *testing.T) {
	reg := NewRegistry("")
	params := reg.viewParams("some_tool", payloadOf("not json", false))
	assert.Equal(t, "markdown", params["layout"])
	assert.Equal(t, "unknown", params["kind"])
	assert.Equal(t, "not json", params["text"])

	obj := reg.viewParams("some_tool", payloadOf(`{"k":"v"}`, false))
	assert.Equal(t, "list", obj["layout"])
}

func TestViewParamsPassthroughForViewTool(t *testing.T) {
	reg := NewRegistry("")
	render := `{"view_id":"leads","layout":"table","columns":["id"],"rows":[{"id":1}],"count":1}`
	params := reg.viewParams(ToolView, payloadOf(render, false))
	assert.Equal(t, "view", params["kind"])
	assert.Equal(t, "table", params["layout"])
	assert.EqualValues(t, 1, params["count"])
	assert.NotNil(t, params["view"])
}

func TestEmitToolResultViewDeliversToSession(t *testing.T) {
	reg := NewRegistry("")
	connID := "ui-session-1"
	ch := make(chan jsonRPCResponse, 4)
	connMutex.Lock()
	activeConnections[connID] = ch
	connMutex.Unlock()
	t.Cleanup(func() {
		connMutex.Lock()
		delete(activeConnections, connID)
		connMutex.Unlock()
	})

	reg.emitToolResultView(connID, "acme__getCurrent", payloadOf(`[{"a":1}]`, false))
	select {
	case msg := <-ch:
		assert.Equal(t, "notifications/view", msg.Method)
		p := msg.Params.(map[string]interface{})
		assert.Equal(t, "acme__getCurrent", p["tool"])
		assert.Equal(t, "table", p["layout"])
	case <-time.After(time.Second):
		t.Fatal("view event not delivered")
	}

	// Stateless (connID "") is a no-op.
	reg.emitToolResultView("", "acme__getCurrent", payloadOf(`[{"a":1}]`, false))
}

func TestUIViewEventForScriptResult(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, root := newScriptRegistry(t, specPath)
	writeScript(t, root, "rows.tengo", "// ---\n// kind: script\n// id: rows\n// ---\nreturn [{a: 1}, {a: 2}]\n")
	_, err := reg.LoadKnowledge("acme")
	require.NoError(t, err)
	srv, bridge := newUIServer(t, reg)

	// Prime the session so its stream exists.
	_ = getJSON(t, uiRequest(t, "GET", srv.URL+"/ui/manifest", "s", ""))
	connID := bridge.sessions["s"].connID

	resp := getJSON(t, uiRequest(t, "POST", srv.URL+"/ui/chat", "s",
		`{"tool":"acme__rows","arguments":{}}`))
	require.Equal(t, true, resp["ok"])
	view, ok := resp["view"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "script", view["kind"])
	assert.Equal(t, "table", view["layout"])
	assert.EqualValues(t, 2, view["count"])

	// The same outcome is mirrored on the session's event stream.
	select {
	case msg := <-bridge.sessions["s"].ch:
		require.Equal(t, "notifications/view", msg.Method)
		p := msg.Params.(map[string]interface{})
		assert.Equal(t, "acme__rows", p["tool"])
	case <-time.After(time.Second):
		t.Fatal("no view event on the session stream")
	}
	_ = connID
}
