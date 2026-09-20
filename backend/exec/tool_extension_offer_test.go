package exec

import (
	"encoding/json"
	"testing"

	"mike/backend/extensions"
	"mike/backend/types"
)

// Declared as registry text rather than Go structs: that is the only form an extension can ship.
const searxngManifest = `{
  "display_name": "SearXNG Search",
  "description": "Web search through your own SearXNG instance",
  "author": "local",
  "version": "0.1.0",
  "config_schema": {"properties": [
    {"key": "base_url", "type": "url", "title": "SearXNG base URL"},
    {"key": "auth_token", "type": "string", "sensitive": true, "title": "Auth token"}
  ]},
  "capabilities": {
    "network": {"headers": {"Authorization": "Bearer {{auth_token}}"}},
    "tools": [{
      "name": "web_search",
      "display_name": "Search",
      "description": "Search the web. Returns numbered results with title, URL and snippet.",
      "parameters": {
        "type": "object",
        "properties": {
          "query": {"type": "string", "description": "What to search for"},
          "category": {"type": "string", "description": "SearXNG category"}
        },
        "required": ["query"]
      },
      "request": {
        "method": "GET",
        "url": "{{config.base_url}}/search",
        "query": {"format": "json", "q": "{{args.query}}", "categories": "{{args.category|general}}"}
      },
      "response": {
        "results": "results",
        "fields": [
          {"label": "title", "key": "title"},
          {"label": "url", "key": "url"},
          {"label": "snippet", "key": "content"}
        ],
        "total": "number_of_results",
        "max_results": 5
      }
    }]
  }
}`

// searchToolExtension is the installed row for that manifest, configured with an address.
func searchToolExtension(address string) *extensions.Extension {
	var manifest extensions.ExtensionManifest
	if err := json.Unmarshal([]byte(searxngManifest), &manifest); err != nil {
		panic("the test manifest does not parse: " + err.Error())
	}
	return &extensions.Extension{
		ID: "searxng", Name: "searxng", Version: "0.1.0",
		Type: extensions.ExtensionTypeTool, Enabled: true,
		Manifest: manifest,
		Config:   map[string]any{"base_url": address},
	}
}

func toolRegistryWith(t *testing.T, exts ...*extensions.Extension) *extensions.ToolRegistry {
	t.Helper()
	s := testStore(t)
	es := extensions.NewExtensionStore(s, nil)
	if err := es.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	for _, ext := range exts {
		if err := es.Create(ext); err != nil {
			t.Fatalf("create extension: %v", err)
		}
	}
	return extensions.NewToolRegistry(es)
}

func definitionNames(defs []types.ChatTool) map[string]bool {
	names := map[string]bool{}
	for i := range defs {
		if defs[i].Function != nil {
			names[defs[i].Function.Name] = true
		}
	}
	return names
}

// A search tool that worked only in act mode would disappear as soon as a task switched to plan.
func TestContributedToolOfferedInEveryMode(t *testing.T) {
	te := newTestExecutorWith(t, Deps{ExtensionTools: toolRegistryWith(t, searchToolExtension("https://searx.example"))})

	for _, mode := range []types.OperatingMode{types.ModeAct, types.ModePlan, types.ModeExplore} {
		names := definitionNames(te.toolDefinitionsFor(&types.Task{ID: "t", OperatingMode: mode}))
		if !names["web_search"] {
			t.Errorf("%s mode does not offer the contributed tool: %v", mode, names)
		}
		if !names["read_file"] {
			t.Errorf("%s mode lost its core tools: %v", mode, names)
		}
	}

	// The model is told what the extension declared, not what the core guessed.
	defs := te.toolDefinitionsFor(&types.Task{ID: "t"})
	for i := range defs {
		fn := defs[i].Function
		if fn == nil || fn.Name != "web_search" {
			continue
		}
		props, _ := fn.Parameters["properties"].(map[string]any)
		if _, ok := props["query"]; !ok {
			t.Errorf("the offered definition lost the declared parameters: %+v", fn.Parameters)
		}
		if fn.Description == "" {
			t.Error("the offered definition has no description")
		}
		return
	}
	t.Fatal("web_search was not in the offered definitions")
}

// With no registry, or one that offers nothing, the tools offered are exactly the core set.
func TestNoContributedToolsWithoutAnExtension(t *testing.T) {
	core := definitionNames(newBareTestExecutor(t).toolDefinitionsFor(&types.Task{ID: "t"}))
	if core["web_search"] {
		t.Error("a bare executor offered a contributed tool")
	}

	empty := newTestExecutorWith(t, Deps{ExtensionTools: toolRegistryWith(t)})
	if names := definitionNames(empty.toolDefinitionsFor(&types.Task{ID: "t"})); names["web_search"] {
		t.Error("an empty registry offered a contributed tool")
	}
	// Installed but unconfigured offers nothing.
	unconfigured := newTestExecutorWith(t, Deps{ExtensionTools: toolRegistryWith(t, searchToolExtension(""))})
	if names := definitionNames(unconfigured.toolDefinitionsFor(&types.Task{ID: "t"})); names["web_search"] {
		t.Error("an extension with no address offered its tool")
	}
}

// TestContributedToolDisplayName: the card label comes from the manifest, and a core tool keeps its own.
func TestContributedToolDisplayName(t *testing.T) {
	te := newTestExecutorWith(t, Deps{ExtensionTools: toolRegistryWith(t, searchToolExtension("https://searx.example"))})
	if got := te.toolDisplayName("web_search"); got != "Search" {
		t.Errorf("display name = %q, want the manifest's Search", got)
	}
	if got := te.toolDisplayName("bash"); got != "Execute" {
		t.Errorf("core display name = %q, want Execute", got)
	}
	if got := te.toolDisplayName("something_unknown"); got != "something_unknown" {
		t.Errorf("unknown name = %q, want it echoed", got)
	}
}
