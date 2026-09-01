package types

// WSMessageType is the type of a WebSocket message.
type WSMessageType string

const (
	WSTypeToken              WSMessageType = "token"
	WSTypeReasoning          WSMessageType = "reasoning"
	WSTypeToolCall           WSMessageType = "tool_call"
	WSTypeToolProgress       WSMessageType = "tool_progress"
	WSTypeToolOutput         WSMessageType = "tool_output"
	WSTypeToolResult         WSMessageType = "tool_result"
	WSTypeStatus             WSMessageType = "status"
	WSTypeQueue              WSMessageType = "queue"
	WSTypeComplete           WSMessageType = "complete"
	WSTypeError              WSMessageType = "error"
	WSTypeUserMessage        WSMessageType = "user_message"
	WSTypeStats              WSMessageType = "stats"
	WSTypeDiffChanged        WSMessageType = "diff_changed"
	WSTypeThreadSwitch       WSMessageType = "thread_switch"
	WSTypeCheckpointPending  WSMessageType = "git_checkpoint_pending"
	WSTypeCheckpointComplete WSMessageType = "git_checkpoint_complete"
	WSTypeMergeResolution    WSMessageType = "merge_resolution"
	WSTypeBuildProgress      WSMessageType = "build_progress"
	WSTypeSandboxStats       WSMessageType = "sandbox_stats"
	WSTypePromptProgress     WSMessageType = "prompt_progress"
	WSTypeHint               WSMessageType = "hint"
	WSTypeAdvisory           WSMessageType = "advisory"
	WSTypeExtensionChange    WSMessageType = "extension_change"
	WSTypeSandboxImageStatus WSMessageType = "sandbox_image_status"
)

// Phases of WSTypeSandboxImageStatus: preparing a project's sandbox image (building it from its
// Dockerfile, or pulling the configured one) while the request that needs it waits.
//
// The two working phases double as task states, which is what the card badges show, so keep them in
// step with the frontend's task-state vocabulary (Card.svelte, CardDetail.svelte).
const (
	SandboxImagePhaseBuilding = "building_image"
	SandboxImagePhasePulling  = "pulling_image"
	SandboxImagePhaseDone     = "done"
	SandboxImagePhaseFailed   = "failed"
)

// SandboxImageStatus is the payload of WSTypeSandboxImageStatus: the phase, the image, and podman's
// latest output line.
type SandboxImageStatus struct {
	Phase string `json:"phase"`
	Image string `json:"image"`
	Line  string `json:"line,omitempty"`
	Error string `json:"error,omitempty"`
}

// StreamEvent carries a typed payload to broadcast.
type StreamEvent struct {
	Type WSMessageType
	Data any
}

// TerminalMessage is the envelope for terminal WebSocket frames.
type TerminalMessage struct {
	Type string `json:"type"`
	Data string `json:"data"`
	Rows int    `json:"rows,omitempty"`
	Cols int    `json:"cols,omitempty"`
}
