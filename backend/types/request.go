package types

// MoveTaskRequest is the request body for moving a task.
type MoveTaskRequest struct {
	Status string `json:"status"`
}

// CreateProjectRequest is the request body for creating a project.
type CreateProjectRequest struct {
	Path                    string         `json:"path"`
	GitUrl                  string         `json:"git_url,omitempty"`
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
}

// CreateDependencyRequest is the request body for creating a dependency.
type CreateDependencyRequest struct {
	ChildTaskID string `json:"child_task_id"`
	AutoStart   bool   `json:"auto_start"`
}

// CreateReviewCommentRequest is the request body for creating a review comment.
type CreateReviewCommentRequest struct {
	FilePath string `json:"file_path"`
	LineNum  int    `json:"line_num"`
	Comment  string `json:"comment"`
}

// UpdateReviewCommentRequest is the request body for updating a review comment.
type UpdateReviewCommentRequest struct {
	Comment  *string `json:"comment"`
	Resolved *bool   `json:"resolved"`
}

// GitCheckoutRequest is the request body for git checkout.
type GitCheckoutRequest struct {
	Branch string `json:"branch"`
}
