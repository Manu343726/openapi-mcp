package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseDocFrontMatter(t *testing.T) {
	src := `---
id: alta_usuario
kind: capability
api: acme
language: es
intents: ["crear empleado", "dar de alta"]
params:
  - {name: nombre, required: true}
steps:
  - tool: acme__create_user_users_post
    inputs: {user_name: {from: param.nombre}}
---
# Alta de usuario

Crea un usuario y lo asigna.
`
	doc, err := ParseDoc([]byte(src))
	require.NoError(t, err)
	require.Equal(t, KindCapability, doc.Kind)
	require.Equal(t, "alta_usuario", doc.ID)
	require.Equal(t, "acme", doc.API)
	require.Equal(t, "es", doc.Language)
	require.Len(t, doc.Intents, 2)
	require.Len(t, doc.Steps, 1)
	require.Equal(t, "param.nombre", doc.Steps[0].Inputs["user_name"].From)
	require.Equal(t, "Alta de usuario", doc.Title)
	require.Contains(t, doc.Body, "Crea un usuario")
}

func TestParseDocWithoutFrontMatter(t *testing.T) {
	doc, err := ParseDoc([]byte("# Solo cuerpo"))
	require.NoError(t, err)
	require.Equal(t, "Solo cuerpo", doc.Title)
	require.Empty(t, doc.Kind)
}

func TestSerializeRoundTrip(t *testing.T) {
	doc := &Doc{
		ID:       "glos",
		Kind:     KindGlossary,
		API:      "acme",
		Language: "es",
		Title:    "Incidencia",
		Body:     "Una incidencia es un aviso del sistema.\n",
	}
	data, err := Serialize(doc)
	require.NoError(t, err)
	re, err := ParseDoc(data)
	require.NoError(t, err)
	require.Equal(t, KindGlossary, re.Kind)
	require.Equal(t, "glos", re.ID)
	require.Contains(t, re.Body, "Una incidencia")
}

func writeDoc(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

func TestLoadLocalValidatesLinksAndAnchors(t *testing.T) {
	root := t.TempDir()
	writeDoc(t, root, "_index.md", "---\nkind: index\n---\n# Manual\n")
	writeDoc(t, root, "glossary/incidencia.md", "---\nid: incidencia\nkind: glossary\n---\n# Incidencia\n\nVer [alta usuario](../capabilities/alta.md)\n")
	writeDoc(t, root, "elements/endpoints/create-user.md", "---\nid: create-user\nkind: endpoint\nanchor: createUser\n---\n# Crear usuario\n")
	writeDoc(t, root, "capabilities/alta.md", "---\nid: alta\nkind: capability\nintents: [crear empleado]\nsteps:\n  - tool: myapi__createUser\n---\n# Alta\n")

	lib, err := LoadLocal(root, LoadOptions{
		API:        "myapi",
		Language:   "es",
		Operations: map[string]bool{"createUser": true, "myapi__createUser": true},
		Schemas:    map[string]bool{},
	})
	require.NoError(t, err)
	require.Len(t, lib.Docs, 4)
	require.Empty(t, lib.Warnings, "expected no warnings, got: %v", lib.Warnings)

	// A broken link should be reported.
	writeDoc(t, root, "glossary/roto.md", "---\nid: roto\nkind: glossary\n---\n# Roto\n\n[no existe](../missing/doc.md)\n")
	lib2, err := LoadLocal(root, LoadOptions{
		API: "myapi", Language: "es",
		Operations: map[string]bool{"createUser": true, "myapi__createUser": true},
	})
	require.NoError(t, err)
	require.NotEmpty(t, lib2.Warnings)
	require.True(t, strings.Contains(strings.Join(lib2.Warnings, "\n"), "broken link"), "warnings: %v", lib2.Warnings)

	// Unknown endpoint anchor → warning.
	writeDoc(t, root, "elements/endpoints/nope.md", "---\nkind: endpoint\nanchor: doesNotExist\n---\n# Nope\n")
	lib3, err := LoadLocal(root, LoadOptions{
		API: "myapi", Language: "es",
		Operations: map[string]bool{"createUser": true, "myapi__createUser": true},
	})
	require.NoError(t, err)
	require.True(t, strings.Contains(strings.Join(lib3.Warnings, "\n"), "doesNotExist"), "warnings: %v", lib3.Warnings)
}

func TestSearchRanksCapabilities(t *testing.T) {
	root := t.TempDir()
	writeDoc(t, root, "glossary/incidencia.md", "---\nid: incidencia\nkind: glossary\n---\n# Incidencia\n\nEn el dominio significa aviso.\n")
	writeDoc(t, root, "capabilities/alta.md", "---\nid: alta\nkind: capability\nintents: [crear empleado, dar de alta]\nparams: [{name: nombre, required: true}]\nsteps: []\n---\n# Alta de empleado\n\nProceso de alta de empleados en el sistema.\n")
	writeDoc(t, root, "capabilities/puerta.md", "---\nid: puerta\nkind: capability\nintents: [abrir puerta]\nsteps: []\n---\n# Abrir puerta\n")

	lib, err := LoadLocal(root, LoadOptions{API: "myapi", Language: "es"})
	require.NoError(t, err)

	hits := lib.Search("crear empleado nuevo", 10)
	require.NotEmpty(t, hits)
	require.Equal(t, "alta", hits[0].Doc.ID, "capability with matching intent should rank first")

	// Natural-language terms used by humans still resolve via intents.
	hits2 := lib.Search("dar de alta a un trabajador", 10)
	require.NotEmpty(t, hits2)
	require.Equal(t, "alta", hits2[0].Doc.ID)
}
