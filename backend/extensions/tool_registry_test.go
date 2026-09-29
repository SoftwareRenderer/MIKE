package extensions

import (
	"strings"
	"testing"
)

// An enabled tool extension with the reference declaration, at `address` when that is not "".
func addToolExtension(t *testing.T, es *ExtensionStore, id, name, address string, mutate func(*ToolConfig)) *Extension {
	t.Helper()
	tool := validSearchTool()
	if mutate != nil {
		mutate(tool)
	}
	ext := makeTestExt(id, name, ExtensionTypeTool)
	ext.Manifest.DisplayName = "SearXNG"
	ext.Manifest.ConfigSchema = searxngLikeSchema()
	ext.Manifest.Capabilities = &Capabilities{Network: &NetworkConfig{}, Tools: []ToolConfig{*tool}}
	ext.Config = map[string]any{}
	if address != "" {
		ext.Config["base_url"] = address
	}
	if err := es.Create(ext); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return ext
}

// What the registry withheld, and why. Production reads only the offered half and logs this one at
// debug, so the question is asked here.
func withheld(reg *ToolRegistry) []ToolProblem {
	_, problems := reg.survey()
	return problems
}

// The offer needs the extension enabled and pointed at an address.
func TestToolRegistryOffersConfiguredExtension(t *testing.T) {
	t.Parallel()
	es := testStore(t)
	reg := NewToolRegistry(es)
	addToolExtension(t, es, "ext-search", "searxng", "https://searx.example", nil)

	offered := reg.Contributed()
	if len(offered) != 1 {
		t.Fatalf("offered %d tools, want 1: %+v", len(offered), withheld(reg))
	}
	spec := offered[0].Spec
	if spec.Name != "web_search" || spec.DisplayName != "Search" || spec.Extension != "searxng" {
		t.Errorf("spec = %+v, want web_search / Search / searxng", spec)
	}
	if !strings.Contains(spec.Description, "Search the web") {
		t.Errorf("description = %q, want the manifest's own sentence", spec.Description)
	}
	if offered[0].Extension == nil || offered[0].Extension.Config["base_url"] != "https://searx.example" {
		t.Error("the offered tool lost the row that runs it")
	}

	if _, ok := reg.Lookup("web_search"); !ok {
		t.Error("Lookup did not find the offered tool")
	}
	if _, ok := reg.Lookup("read_file"); ok {
		t.Error("Lookup answered for a core tool")
	}
}

func TestToolRegistryWithholds(t *testing.T) {
	t.Parallel()
	t.Run("no address configured", func(t *testing.T) {
		t.Parallel()
		es := testStore(t)
		addToolExtension(t, es, "ext-search", "searxng", "", nil)
		reg := NewToolRegistry(es)

		if offered := reg.Contributed(); len(offered) != 0 {
			t.Fatalf("an extension with no address offered %+v", offered)
		}
		unmet := withheld(reg)
		if len(unmet) != 1 || !strings.Contains(unmet[0].Reason, "set base_url") {
			t.Fatalf("unmet = %+v, want the reason naming base_url", unmet)
		}
		if _, ok := reg.Lookup("web_search"); ok {
			t.Error("a withheld tool was dispatched anyway")
		}
	})

	t.Run("extension disabled", func(t *testing.T) {
		t.Parallel()
		es := testStore(t)
		addToolExtension(t, es, "ext-search", "searxng", "https://searx.example", nil)
		if err := es.SetEnabled("ext-search", false); err != nil {
			t.Fatalf("disable: %v", err)
		}
		if offered := NewToolRegistry(es).Contributed(); len(offered) != 0 {
			t.Errorf("a disabled extension offered %+v", offered)
		}
	})

	t.Run("address is unusable", func(t *testing.T) {
		t.Parallel()
		es := testStore(t)
		addToolExtension(t, es, "ext-search", "searxng", "https://searx.example/", nil)
		reg := NewToolRegistry(es)
		if offered := reg.Contributed(); len(offered) != 0 {
			t.Fatalf("a trailing-slash address offered %+v", offered)
		}
		if unmet := withheld(reg); len(unmet) != 1 || !strings.Contains(unmet[0].Reason, "trailing slash") {
			t.Fatalf("unmet = %+v, want the address's own refusal", unmet)
		}
	})

	t.Run("name belongs to a core tool", func(t *testing.T) {
		t.Parallel()
		es := testStore(t)
		addToolExtension(t, es, "ext-search", "searxng", "https://searx.example", func(tool *ToolConfig) {
			tool.Name = "bash"
		})
		unmet := withheld(NewToolRegistry(es))
		if len(unmet) != 1 || !strings.Contains(unmet[0].Reason, "core tool") {
			t.Fatalf("unmet = %+v, want the core-name refusal", unmet)
		}
	})

	// Dispatch resolves tools by name alone, so the second of two extensions sharing a name is reported
	// rather than lost.
	t.Run("two extensions contribute the same name", func(t *testing.T) {
		t.Parallel()
		es := testStore(t)
		addToolExtension(t, es, "ext-a", "alpha-search", "https://a.example", nil)
		addToolExtension(t, es, "ext-b", "beta-search", "https://b.example", nil)
		reg := NewToolRegistry(es)

		offered := reg.Contributed()
		if len(offered) != 1 || offered[0].Spec.Extension != "alpha-search" {
			t.Fatalf("offered = %+v, want the first extension's tool only", offered)
		}
		unmet := withheld(reg)
		if len(unmet) != 1 || !strings.Contains(unmet[0].Reason, "already contributes") {
			t.Fatalf("unmet = %+v, want the duplicate-name reason", unmet)
		}
	})

	t.Run("no tool extensions at all", func(t *testing.T) {
		t.Parallel()
		es := testStore(t)
		if offered := NewToolRegistry(es).Contributed(); offered != nil {
			t.Errorf("offered %+v, want nothing", offered)
		}
	})

	// A registry with nothing behind it is a normal state (an executor built without one), not a crash.
	t.Run("nil registry", func(t *testing.T) {
		t.Parallel()
		var reg *ToolRegistry
		if len(reg.Contributed()) != 0 || len(withheld(reg)) != 0 {
			t.Error("a nil registry answered with something")
		}
		if _, ok := reg.Lookup("web_search"); ok {
			t.Error("a nil registry dispatched a tool")
		}
	})
}

// Pointing the extension at another address makes the tool appear on the next turn.
func TestToolRegistrySeesAConfigurationChange(t *testing.T) {
	t.Parallel()
	es := testStore(t)
	ext := addToolExtension(t, es, "ext-search", "searxng", "", nil)
	reg := NewToolRegistry(es)
	if len(reg.Contributed()) != 0 {
		t.Fatal("an unconfigured extension offered a tool")
	}

	ext.Config["base_url"] = "http://127.0.0.1:8888"
	if err := es.Update("ext-search", ext); err != nil {
		t.Fatalf("update: %v", err)
	}
	offered := reg.Contributed()
	if len(offered) != 1 {
		t.Fatalf("after configuring the address, offered = %+v", offered)
	}
	if granted := offered[0].Extension.GrantedHosts(); len(granted) != 1 || granted[0] != "127.0.0.1" {
		t.Errorf("granted hosts = %v, want the loopback host the person typed", granted)
	}
}
