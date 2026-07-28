package pi

import (
	"encoding/json"
	"strings"

	"kanban/pkg/agent"
)

// mapToStruct is a helper to convert map[string]any to a struct via JSON.
func mapToStruct(m map[string]any, target any) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}

func defaultString(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func extractToolResultText(msg map[string]any) string {
	if c, ok := msg["content"].(string); ok {
		return c
	}
	if ca, ok := msg["content"].([]any); ok {
		var p []string
		for _, i := range ca {
			if m, ok := i.(map[string]any); ok && m["type"] == "text" {
				if t, ok := m["text"].(string); ok {
					p = append(p, t)
				}
			}
		}
		return strings.Join(p, "\n")
	}
	return ""
}

func parseAssistantMessage(msg map[string]any) (text, reasoning string, toolCalls []map[string]any, pt, ptok int) {
	if content, ok := msg["content"].([]any); ok {
		var texts []string
		for _, c := range content {
			if cm, ok := c.(map[string]any); ok {
				switch cm["type"] {
				case "text":
					if t, ok := cm["text"].(string); ok {
						texts = append(texts, t)
					}
				case "thinking":
					if t, ok := cm["thinking"].(string); ok {
						reasoning = t
					}
				case "toolCall":
					toolCalls = append(toolCalls, cm)
				}
			}
		}
		text = strings.Join(texts, "\n")
	}
	if usage, ok := msg["usage"].(map[string]any); ok {
		if v, ok := usage["input"].(float64); ok {
			pt = int(v)
		}
		if v, ok := usage["output"].(float64); ok {
			ptok = int(v)
		}
	}
	return
}

func parseSessionStats(data map[string]any) agent.SessionStatsData {
	var s agent.SessionStatsData
	if cost, ok := data["cost"].(float64); ok {
		s.Cost = cost
	}
	if tokens, ok := data["tokens"].(map[string]any); ok {
		if v, ok := tokens["input"].(float64); ok {
			s.TotalInput = int(v)
		}
		if v, ok := tokens["output"].(float64); ok {
			s.TotalOutput = int(v)
		}
		if v, ok := tokens["cacheRead"].(float64); ok {
			s.TotalCacheRead = int(v)
		}
		if v, ok := tokens["cacheWrite"].(float64); ok {
			s.TotalCacheWrite = int(v)
		}
	}
	if cu, ok := data["contextUsage"].(map[string]any); ok {
		if v, ok := cu["tokens"].(float64); ok {
			s.ContextTokens = int(v)
		}
		if v, ok := cu["contextWindow"].(float64); ok {
			s.ContextWindow = int(v)
		}
		if v, ok := cu["percent"].(float64); ok {
			s.ContextPercent = v
		}
	}
	return s
}

func extractModelNameFromEvent(raw map[string]any) (string, bool) {
	if mn, ok := raw["model"].(string); ok && mn != "" {
		return mn, true
	}
	if mn, ok := raw["modelName"].(string); ok && mn != "" {
		return mn, true
	}
	msgs, _ := raw["messages"].([]any)
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := msg["role"].(string); role == "assistant" {
			if mn, ok := msg["model"].(string); ok && mn != "" {
				return mn, true
			}
		}
	}
	return "", false
}
