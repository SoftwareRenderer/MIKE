package types

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"strings"
)

// DefaultReasoningEffort applies to models without an explicit setting: server decides.
const DefaultReasoningEffort = "default"

// UnmarshalJSON decodes a Provider, accepting models either as []ModelConfig or as []string, taking an
// absent or null list as no models, and normalizing an empty Kind to "llm".
func (p *Provider) UnmarshalJSON(data []byte) error {
	type providerAlias Provider
	var raw struct {
		*providerAlias
		Models jsontext.Value `json:"models"`
	}
	raw.providerAlias = (*providerAlias)(p)

	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// Normalize kind: empty → "llm", legacy "pi" → "agent".
	if p.Kind == "" {
		p.Kind = ProviderKindLLM
	} else if p.Kind == "pi" {
		p.Kind = ProviderKindAgent
	}

	// A provider with no model list is a provider that answers nothing: an agent extension's row
	// carries no models, and a hand-written file may leave the key out entirely. Only a value that is
	// neither list is a shape this cannot read.
	switch models := strings.TrimSpace(raw.Models.String()); models {
	case "", "null":
		return nil
	}

	// Try new format: []ModelConfig
	var newModels []ModelConfig
	if err := json.Unmarshal(raw.Models, &newModels); err == nil && len(newModels) > 0 {
		if newModels[0].Name != "" {
			p.Models = newModels
			for i := range p.Models {
				if p.Models[i].ReasoningEffort == "" {
					p.Models[i].ReasoningEffort = DefaultReasoningEffort
				}
			}
			return nil
		}
	}

	// Try old format: []string
	var oldModels []string
	if err := json.Unmarshal(raw.Models, &oldModels); err == nil {
		p.Models = make([]ModelConfig, len(oldModels))
		for i, name := range oldModels {
			p.Models[i] = ModelConfig{Name: name, ReasoningEffort: DefaultReasoningEffort}
		}
		return nil
	}

	return fmt.Errorf("invalid models format")
}
