package types

import (
	"encoding/json"
	"fmt"
)

// DefaultReasoningEffort is the default reasoning effort for models without an explicit setting.
const DefaultReasoningEffort = "medium"

// UnmarshalJSON implements backward-compatible JSON unmarshaling for Provider.
// Accepts both the new format (models as []ModelConfig) and the old format (models as []string).
func (p *Provider) UnmarshalJSON(data []byte) error {
	type providerAlias Provider
	var raw struct {
		*providerAlias
		Models json.RawMessage `json:"models"`
	}
	raw.providerAlias = (*providerAlias)(p)

	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// Try new format: []ModelConfig
	var newModels []ModelConfig
	if err := json.Unmarshal(raw.Models, &newModels); err == nil && len(newModels) > 0 {
		if newModels[0].Name != "" || len(newModels) == 0 {
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

	// Fallback: empty array
	if err := json.Unmarshal(raw.Models, &[]ModelConfig{}); err == nil {
		p.Models = []ModelConfig{}
		return nil
	}

	return fmt.Errorf("invalid models format")
}
