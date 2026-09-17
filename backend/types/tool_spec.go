package types

// NetworkedExtension is an enabled extension whose contributed tools make requests from the server
// process. It is served with the configuration and never stored.
type NetworkedExtension struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Tools       []string `json:"tools"`
}

// ToolSpec is a tool as an extension declares it. types is the floor backend/extensions and backend/llama
// stand on, so neither imports the other to describe a contributed tool.
type ToolSpec struct {
	Name        string
	DisplayName string
	Description string
	// The JSON Schema the provider is given verbatim.
	Parameters map[string]any
	Extension  string
}
