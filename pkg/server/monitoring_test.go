package server

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// specWithExtraOp returns a spec that mirrors registryTestV3Spec plus a PUT
// operation (operationId "putCurrent"), used to simulate a spec source change.
func specWithExtraOp() string {
	return `{
	  "openapi": "3.0.0",
	  "info": {"title": "Weather API", "version": "1.1.0"},
	  "servers": [{"url": "https://weather.example.com/v1"}],
	  "paths": {
	    "/current": {
	      "get": {"operationId": "getCurrent", "responses": {"200": {"description": "OK"}}},
	      "put": {"operationId": "putCurrent", "responses": {"200": {"description": "OK"}}}
	    }
	  }
	}`
}

// bumpSpecFile rewrites a spec file with fresh content and pushes its mtime into
// the future so the monitoring watcher detects a change.
func bumpSpecFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	future := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	require.NoError(t, os.Chtimes(path, future, future))
}

// setupMonitorTestConnection registers a fake initialized SSE connection and
// returns its id and a buffered channel capturing server->client notifications.
func setupMonitorTestConnection(t *testing.T) (string, chan jsonRPCResponse) {
	t.Helper()
	connID := fmt.Sprintf("mon-%d", time.Now().UnixNano())
	ch := make(chan jsonRPCResponse, 16)
	connMutex.Lock()
	activeConnections[connID] = ch
	initializedConnections[connID] = true
	connMutex.Unlock()
	t.Cleanup(func() { cleanupTestConnection(connID) })
	return connID, ch
}

func drainNotifications(ch chan jsonRPCResponse) []jsonRPCResponse {
	var out []jsonRPCResponse
	for {
		select {
		case n := <-ch:
			out = append(out, n)
		default:
			return out
		}
	}
}

func TestMonitoringNotifyOnChange(t *testing.T) {
	path := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg := NewRegistry("")
	_, err := reg.RegisterAPI(config.APIDefinition{
		Name: "weather", Source: path,
		Monitoring: config.MonitorConfig{Enabled: true},
		Targets:    []config.TargetDefinition{{Name: "prod", BaseURL: "https://api"}},
	}, false)
	require.NoError(t, err)
	t.Cleanup(reg.StopMonitoring)

	_, ch := setupMonitorTestConnection(t)
	drainNotifications(ch) // drop the registration-time tools/list_changed

	bumpSpecFile(t, path, specWithExtraOp())
	reg.checkMonitoredAPIs(context.Background())

	notifs := drainNotifications(ch)
	methods := map[string]bool{}
	for _, n := range notifs {
		methods[n.Method] = true
	}
	assert.True(t, methods[notificationSpecChanged], "monitoring-only must notify spec_changed, got %v", notifs)
	assert.False(t, methods["notifications/tools/list_changed"], "monitoring-only must NOT reload, got %v", notifs)

	// No reload happened: the generated tools are unchanged.
	assert.NotContains(t, toolNames(reg.Tools()), "weather__putCurrent")

	// A subsequent check for the same source mtime must not re-notify.
	reg.checkMonitoredAPIs(context.Background())
	for _, n := range drainNotifications(ch) {
		assert.NotEqual(t, notificationSpecChanged, n.Method, "must not re-notify for an unchanged source")
	}
}

func TestMonitoringAutoReloadOnChange(t *testing.T) {
	path := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg := NewRegistry("")
	_, err := reg.RegisterAPI(config.APIDefinition{
		Name: "weather", Source: path,
		Monitoring: config.MonitorConfig{Enabled: true, AutoReload: true},
		Targets:    []config.TargetDefinition{{Name: "prod", BaseURL: "https://api"}},
	}, false)
	require.NoError(t, err)
	t.Cleanup(reg.StopMonitoring)

	_, ch := setupMonitorTestConnection(t)
	drainNotifications(ch)

	bumpSpecFile(t, path, specWithExtraOp())
	reg.checkMonitoredAPIs(context.Background())

	// Auto-reload regenerated the tools and preserved the targets.
	assert.Contains(t, toolNames(reg.Tools()), "weather__putCurrent")
	sum, _ := reg.GetAPI("weather")
	assert.ElementsMatch(t, []string{"prod"}, sum.Targets)
	assert.Equal(t, "prod", sum.ActiveTarget)

	// Clients were first told the spec changed, then told to re-fetch tools.
	notifs := drainNotifications(ch)
	methods := []string{}
	for _, n := range notifs {
		methods = append(methods, n.Method)
	}
	assert.Contains(t, methods, notificationSpecChanged, "expected spec_changed, got %v", methods)
	assert.Contains(t, methods, "notifications/tools/list_changed", "expected list_changed after reload, got %v", methods)
	specIdx, listIdx := -1, -1
	for i, m := range methods {
		if m == notificationSpecChanged && specIdx == -1 {
			specIdx = i
		}
		if m == "notifications/tools/list_changed" && listIdx == -1 {
			listIdx = i
		}
	}
	assert.True(t, specIdx >= 0 && listIdx >= 0 && specIdx < listIdx, "notify must precede reload, order: %v", methods)

	// After the reload the spec is fresh; a follow-up check must not re-fire.
	reg.checkMonitoredAPIs(context.Background())
	for _, n := range drainNotifications(ch) {
		assert.NotEqual(t, notificationSpecChanged, n.Method)
		assert.NotEqual(t, "notifications/tools/list_changed", n.Method)
	}
}

func TestMonitoringDisabledNoNotify(t *testing.T) {
	path := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg := NewRegistry("")
	_, err := reg.RegisterAPI(config.APIDefinition{Name: "weather", Source: path}, false)
	require.NoError(t, err)
	assert.Nil(t, reg.monitorCtx, "watcher must not start when monitoring is disabled")

	_, ch := setupMonitorTestConnection(t)
	drainNotifications(ch)
	bumpSpecFile(t, path, specWithExtraOp())
	reg.checkMonitoredAPIs(context.Background())

	for _, n := range drainNotifications(ch) {
		assert.NotEqual(t, notificationSpecChanged, n.Method)
	}
	assert.NotContains(t, toolNames(reg.Tools()), "weather__putCurrent")
}

func TestMonitoringLifecycleAndNormalization(t *testing.T) {
	// auto_reload alone implies enabled (monitoring).
	path := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg := NewRegistry("")
	_, err := reg.RegisterAPI(config.APIDefinition{Name: "weather", Source: path, Monitoring: config.MonitorConfig{AutoReload: true}}, false)
	require.NoError(t, err)

	view, err := reg.GetApiEntryView("weather")
	require.NoError(t, err)
	assert.True(t, view.Def.Monitoring.Enabled, "auto_reload must imply monitoring enabled")
	assert.True(t, view.Def.Monitoring.AutoReload)

	// The watcher started when the monitored API was registered...
	assert.NotNil(t, reg.monitorCtx)

	// ...and can be stopped (idempotently).
	reg.StopMonitoring()
	assert.Nil(t, reg.monitorCtx)
	reg.StopMonitoring()
}
