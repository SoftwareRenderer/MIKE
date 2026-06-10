package types

// toolCall represents a parsed tool call from the model (internal use).
type toolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// toolResult represents the result of executing a tool (internal use).
type toolResult struct {
	Success bool   `json:"success"`
	Output  string `json:"output"`
}
