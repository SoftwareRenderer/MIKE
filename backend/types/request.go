package types

// MoveTaskRequest is the request body for moving a task.
type MoveTaskRequest struct {
	Status string `json:"status"`
}

// CreateProjectRequest is the request body for creating a project.
type CreateProjectRequest struct {
	Path                    string         `json:"path"`
	GitURL                  string         `json:"git_url,omitempty"`
	Name                    string         `json:"name,omitempty"`
	SandboxEnabled          bool           `json:"sandbox_enabled"`
	SandboxNetworkIsolation *bool          `json:"sandbox_network_isolation"`
	SandboxImage            string         `json:"sandbox_image,omitempty"`
	SandboxDockerfilePath   string         `json:"sandbox_dockerfile_path,omitempty"`
	SandboxMemoryLimit      int            `json:"sandbox_memory_limit"`
	SandboxForwardPorts     []PortForward  `json:"sandbox_forward_ports,omitempty"`
	SandboxMounts           []SandboxMount `json:"sandbox_mounts,omitempty"`
	SandboxEnvs             []SandboxEnv   `json:"sandbox_envs,omitempty"`
	SandboxCapabilities     []string       `json:"sandbox_capabilities,omitempty"`
}

// UpdateProjectRequest is the request body for updating a project.
type UpdateProjectRequest struct {
	Name                    string         `json:"name,omitempty"`
	SandboxEnabled          *bool          `json:"sandbox_enabled,omitempty"`
	SandboxNetworkIsolation *bool          `json:"sandbox_network_isolation,omitempty"`
	SandboxImage            *string        `json:"sandbox_image,omitempty"`
	SandboxDockerfilePath   *string        `json:"sandbox_dockerfile_path,omitempty"`
	SandboxMemoryLimit      *int           `json:"sandbox_memory_limit,omitempty"`
	SandboxForwardPorts     []PortForward  `json:"sandbox_forward_ports,omitempty"`
	SandboxMounts           []SandboxMount `json:"sandbox_mounts,omitempty"`
	SandboxEnvs             []SandboxEnv   `json:"sandbox_envs,omitempty"`
	SandboxCapabilities     []string       `json:"sandbox_capabilities,omitempty"`
	CommitAssistedBy        *bool          `json:"commit_assisted_by,omitempty"`
	// MCPServerIDs is this project's MCP opt-in list. A pointer because a partial update must tell
	// "leave it alone" from "enable nothing"; the list is written whole either way.
	MCPServerIDs *[]string `json:"mcp_server_ids,omitempty"`
}

// GitCheckoutRequest is the request body for git checkout.
type GitCheckoutRequest struct {
	Branch string `json:"branch"`
}
