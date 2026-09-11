package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ckanthony/openapi-mcp/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newScriptRegistry registers a knowledge-enabled API backed by specPath with a
// temp knowledge root, returning the registry and the root.
func newScriptRegistry(t *testing.T, specPath string) (*Registry, string) {
	t.Helper()
	root := t.TempDir()
	reg := NewRegistry("")
	_, err := reg.RegisterAPI(config.APIDefinition{
		Name:      "acme",
		Source:    specPath,
		Knowledge: config.KnowledgeConfig{Enabled: true, Language: "en", Root: root},
	}, false)
	require.NoError(t, err)
	return reg, root
}

func writeScript(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, "scripts")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
}

func TestScriptRegistrationAndDispatch(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, root := newScriptRegistry(t, specPath)
	writeScript(t, root, "hello.tengo", "// ---\n// kind: script\n// id: hello\n// summary: Say hi\n// ---\nreturn \"hi \" + params.name\n")

	_, err := reg.LoadKnowledge("acme")
	require.NoError(t, err)

	names := toolNames(reg.Tools())
	assert.Contains(t, names, "acme__hello", "script tool should be exposed")
	assert.Contains(t, names, ToolScriptList)
	assert.Contains(t, names, ToolScriptDescribe)

	out, err := reg.RunScript("sess-1", "acme__hello", map[string]interface{}{"name": "bob"})
	require.NoError(t, err)
	assert.Equal(t, "hi bob", out)

	// script_list reports it.
	list := reg.runManagementTool("sess-1", ToolScriptList, map[string]interface{}{"api": "acme"})
	require.True(t, list.ok, list.text)
	assert.Contains(t, list.text, "acme__hello")

	// script_describe returns the source.
	desc := reg.runManagementTool("sess-1", ToolScriptDescribe, map[string]interface{}{"script": "acme__hello"})
	require.True(t, desc.ok, desc.text)
	assert.Contains(t, desc.text, "params.name")
}

func TestScriptMCPBridge(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, root := newScriptRegistry(t, specPath)
	writeScript(t, root, "chain.tengo", "// ---\n// kind: script\n// id: chain\n// permissions: [mcp]\n// ---\nm := import(\"mcp\")\nreturn m.call(\"script_list\", {})\n")

	_, err := reg.LoadKnowledge("acme")
	require.NoError(t, err)

	out, err := reg.RunScript("sess-1", "acme__chain", nil)
	require.NoError(t, err)
	assert.Contains(t, out, "acme__chain")
}

func TestScriptDeniedModule(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, root := newScriptRegistry(t, specPath)
	writeScript(t, root, "evil.tengo", "// ---\n// kind: script\n// id: evil\n// ---\no := import(\"os\")\nreturn o.hostname()\n")

	_, err := reg.LoadKnowledge("acme")
	require.NoError(t, err)

	_, err = reg.RunScript("sess-1", "acme__evil", nil)
	require.Error(t, err, "os module must be denied without permission")
}

func TestScriptExposure(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, root := newScriptRegistry(t, specPath)
	writeScript(t, root, "hello.tengo", "// ---\n// kind: script\n// id: hello\n// ---\nreturn \"hi\"\n")
	_, err := reg.LoadKnowledge("acme")
	require.NoError(t, err)
	require.Contains(t, toolNames(reg.Tools()), "acme__hello")

	// Global: hide the script bucket.
	_, err = reg.UpdateAPIExposure("acme", exposurePatch{deactivateTags: []string{scriptTag}})
	require.NoError(t, err)
	assert.NotContains(t, toolNames(reg.Tools()), "acme__hello")
	_, err = reg.RunScript("sess-1", "acme__hello", nil)
	require.Error(t, err, "hidden script must be gated")

	// Session: re-activate for one session only.
	_, err = reg.UpdateSessionAPIExposure("sess-1", "acme", exposurePatch{mode: config.ExposureModeNone, activateTags: []string{scriptTag}})
	require.NoError(t, err)
	assert.Contains(t, toolNames(reg.ToolsForSession("sess-1")), "acme__hello")
	assert.NotContains(t, toolNames(reg.ToolsForSession("sess-2")), "acme__hello")
	out, err := reg.RunScript("sess-1", "acme__hello", nil)
	require.NoError(t, err)
	assert.Equal(t, "hi", out)
	_, err = reg.RunScript("sess-2", "acme__hello", nil)
	require.Error(t, err)
}

func TestScriptNameCollision(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, root := newScriptRegistry(t, specPath)
	// getCurrent is an operation of registryTestV3Spec.
	writeScript(t, root, "getCurrent.tengo", "// ---\n// kind: script\n// id: getCurrent\n// ---\nreturn \"x\"\n")
	_, err := reg.LoadKnowledge("acme")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collision")
}

func TestScriptDroppedOnUnregister(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, root := newScriptRegistry(t, specPath)
	writeScript(t, root, "hello.tengo", "// ---\n// kind: script\n// id: hello\n// ---\nreturn \"hi\"\n")
	_, err := reg.LoadKnowledge("acme")
	require.NoError(t, err)
	require.Contains(t, toolNames(reg.Tools()), "acme__hello")

	_, err = reg.UnregisterAPI("acme")
	require.NoError(t, err)
	assert.NotContains(t, toolNames(reg.Tools()), "acme__hello")
	_, err = reg.RunScript("sess", "acme__hello", nil)
	require.Error(t, err)
}

func TestScriptRunTask(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, root := newScriptRegistry(t, specPath)
	writeScript(t, root, "hello.tengo", "// ---\n// kind: script\n// id: hello\n// ---\nreturn \"hi \" + params.name\n")
	// A capability that runs the script as one step.
	require.NoError(t, os.WriteFile(filepath.Join(root, "hello-task.md"), []byte(`---
id: hello_task
kind: capability
api: acme
intents: [greet]
params:
  - {name: name, required: true}
steps:
  - tool: acme__hello
    inputs:
      name: {from: param.name}
---
# Greet
`), 0o644))

	_, err := reg.LoadKnowledge("acme")
	require.NoError(t, err)

	dry, err := reg.RunTask("sess-1", "acme", "hello_task", map[string]interface{}{"name": "ada"}, "", TaskModeDryRun)
	require.NoError(t, err)
	assert.Contains(t, dry, "static script")

	auto, err := reg.RunTask("sess-1", "acme", "hello_task", map[string]interface{}{"name": "ada"}, "", TaskModeAuto)
	require.NoError(t, err)
	assert.True(t, strings.Contains(auto, "hi ada"), auto)
}

func TestScriptExposureReportCountsScripts(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, root := newScriptRegistry(t, specPath)
	writeScript(t, root, "hello.tengo", "// ---\n// kind: script\n// id: hello\n// ---\nreturn \"hi\"\n")
	_, err := reg.LoadKnowledge("acme")
	require.NoError(t, err)

	rep, err := reg.exposureReportForSession("", "acme")
	require.NoError(t, err)
	totals := rep["totals"].(map[string]interface{})
	// Two spec operations + one script.
	assert.EqualValues(t, 3, totals["total"])
	assert.EqualValues(t, 3, totals["allowed"])
	assert.EqualValues(t, 3, totals["exposed"])

	ops := rep["operations"].([]map[string]interface{})
	var scriptSeen bool
	for _, op := range ops {
		if op["operation_id"] == "hello" {
			scriptSeen = true
			assert.Equal(t, "script", op["kind"])
			assert.Equal(t, "exposed", op["status"])
		}
	}
	assert.True(t, scriptSeen, "script should appear in api_exposure operations")

	// Hiding the script bucket flips its status and the totals.
	_, err = reg.UpdateAPIExposure("acme", exposurePatch{deactivateTags: []string{scriptTag}})
	require.NoError(t, err)
	rep, err = reg.exposureReportForSession("", "acme")
	require.NoError(t, err)
	assert.EqualValues(t, 2, rep["totals"].(map[string]interface{})["exposed"])

	summaries := reg.APIsForSession("")
	require.Len(t, summaries, 1)
	assert.Contains(t, summaries[0].Tools, "acme__hello")
	assert.Equal(t, 3, summaries[0].ToolCount)
}

func TestScriptViewSurfacesTimeoutAndIntents(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, root := newScriptRegistry(t, specPath)
	writeScript(t, root, "greet.tengo", "// ---\n// kind: script\n// id: greet\n// summary: Greets a name\n// intents: [greet, saludo]\n// timeout: 3\n// ---\nreturn \"hi\"\n")
	_, err := reg.LoadKnowledge("acme")
	require.NoError(t, err)

	out, err := reg.ListScripts("acme")
	require.NoError(t, err)
	assert.Contains(t, out, `"timeout_s": 3`)
	assert.Contains(t, out, `"intents"`)
	assert.Contains(t, out, "greet")

	desc, err := reg.DescribeScript("acme", "acme__greet")
	require.NoError(t, err)
	assert.Contains(t, desc, `"timeout_s": 3`)
	assert.Contains(t, desc, "saludo")
}

func TestScriptDiscoveryHints(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	reg, root := newScriptRegistry(t, specPath)
	writeScript(t, root, "greet.tengo", "// ---\n// kind: script\n// id: greet\n// summary: Greets a person\n// intents: [greet someone, saludo]\n// ---\nreturn \"hi\"\n")
	writeScript(t, root, "unrelated.tengo", "// ---\n// kind: script\n// id: unrelated\n// summary: Does something else entirely\n// ---\nreturn \"x\"\n")
	_, err := reg.LoadKnowledge("acme")
	require.NoError(t, err)

	search, err := reg.KnowledgeSearch("s1", "acme", "greet someone", 10)
	require.NoError(t, err)
	assert.Contains(t, search, "related_scripts")
	assert.Contains(t, search, "acme__greet")
	assert.NotContains(t, search, "acme__unrelated")

	discover, err := reg.DiscoverTask("s1", "acme", "greet someone")
	require.NoError(t, err)
	assert.Contains(t, discover, `"scripts"`)
	assert.Contains(t, discover, "acme__greet")

	clarify, err := reg.ClarifyKnowledge("s1", "acme", "saludo")
	require.NoError(t, err)
	assert.Contains(t, clarify, "acme__greet")
}

func TestScriptRunFoldsIntoCapabilityDraft(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	root := t.TempDir()
	reg := NewRegistry("")
	_, err := reg.RegisterAPI(config.APIDefinition{
		Name:   "acme",
		Source: specPath,
		Knowledge: config.KnowledgeConfig{
			Enabled:  true,
			Language: "en",
			Root:     root,
			Learning: config.KnowledgeLearningConfig{Enabled: true},
		},
	}, false)
	require.NoError(t, err)
	writeScript(t, root, "hello.tengo", "// ---\n// kind: script\n// id: hello\n// ---\nreturn \"hi\"\n")
	_, err = reg.LoadKnowledge("acme")
	require.NoError(t, err)

	// Invoke the tool through the JSON-RPC dispatch so the handler records the
	// trace exactly as a real client call would.
	req := &jsonRPCRequest{Jsonrpc: "2.0", Method: "tools/call", ID: 1, Params: map[string]interface{}{
		"name":      "acme__hello",
		"arguments": map[string]interface{}{},
	}}
	dispatchJSONRPC("s1", req, 1, reg)

	msg, err := reg.RememberSequence("s1", "acme", "script-draft")
	require.NoError(t, err)
	assert.Contains(t, msg, "script-draft")

	reg.mu.RLock()
	doc := reg.sessionKnowledge["s1"]["acme"]["script-draft"]
	reg.mu.RUnlock()
	require.NotNil(t, doc)
	require.Len(t, doc.Steps, 1)
	assert.Equal(t, "acme__hello", doc.Steps[0].Tool)
}

func TestPromoteSequenceToScript(t *testing.T) {
	specPath := writeSpecFile(t, "spec.json", registryTestV3Spec)
	root := t.TempDir()
	reg := NewRegistry("")
	_, err := reg.RegisterAPI(config.APIDefinition{
		Name:   "acme",
		Source: specPath,
		Knowledge: config.KnowledgeConfig{
			Enabled:  true,
			Language: "en",
			Root:     root,
			Learning: config.KnowledgeLearningConfig{Enabled: true},
		},
	}, false)
	require.NoError(t, err)
	writeScript(t, root, "hello.tengo", "// ---\n// kind: script\n// id: hello\n// ---\nreturn \"hi\"\n")
	_, err = reg.LoadKnowledge("acme")
	require.NoError(t, err)

	// Record a call to the script, then remember the sequence.
	req := &jsonRPCRequest{Jsonrpc: "2.0", Method: "tools/call", ID: 1, Params: map[string]interface{}{
		"name":      "acme__hello",
		"arguments": map[string]interface{}{},
	}}
	dispatchJSONRPC("s1", req, 1, reg)
	_, err = reg.RememberSequence("s1", "acme", "hello_recipe")
	require.NoError(t, err)

	preview, err := reg.PromoteScript("s1", "acme", "hello_recipe", "", false)
	require.NoError(t, err)
	assert.Contains(t, preview, "confirm=true")

	out, err := reg.PromoteScript("s1", "acme", "hello_recipe", "", true)
	require.NoError(t, err)
	assert.Contains(t, out, "acme__hello_recipe")

	// The generated script is registered and replays the recorded call.
	assert.Contains(t, toolNames(reg.Tools()), "acme__hello_recipe")
	res, err := reg.RunScript("s1", "acme__hello_recipe", nil)
	require.NoError(t, err)
	assert.Equal(t, "hi", res)
}
