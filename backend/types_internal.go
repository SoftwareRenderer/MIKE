package backend

// -- Internal types --

// ToolCall represents a parsed tool call from the model.
type ToolCall struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Arguments   string `json:"arguments"`
}

// ToolResult represents the result of executing a tool.
type ToolResult struct {
	Success bool   `json:"success"`
	Output  string `json:"output"`
}
