package pi

// Pi Protocol Data Structs
type piMessageUpdate struct {
	AssistantMessageEvent struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	} `json:"assistantMessageEvent"`
}

type piToolExecution struct {
	ToolName   string         `json:"toolName"`
	ToolCallID string         `json:"toolCallId"`
	Args       map[string]any `json:"args"`
	IsError    bool           `json:"isError"`
	Result     map[string]any `json:"result"`
}

type piAgentEnd struct {
	Model     string           `json:"model"`
	ModelName string           `json:"modelName"`
	Messages  []map[string]any `json:"messages"`
}

type piResponse struct {
	Command string         `json:"command"`
	Success bool           `json:"success"`
	Error   string         `json:"error"`
	Data    map[string]any `json:"data"`
}

type piUIRequest struct {
	Method string `json:"method"`
	ID     string `json:"id"`
}

// piConfig holds the resolved adapter configuration after Init parses
// the raw config JSON. It combines user-provided values with defaults
// and sandbox environment variables from the extension manifest.
type piConfig struct {
	Binary      string
	Mode        string
	SessionName string
	DisplayName string
	Env         []string
	SandboxEnvs []envVar // resolved env key-value pairs from manifest sandbox config
}

// envVar is a resolved key-value pair for sandbox environment variables.
type envVar struct {
	Key   string
	Value string
}
