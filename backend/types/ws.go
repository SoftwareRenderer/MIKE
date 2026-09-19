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
	WSTypeLockWait           WSMessageType = "lock_wait"
	WSTypeComplete           WSMessageType = "complete"
	WSTypeError              WSMessageType = "error"
	WSTypeUserMessage        WSMessageType = "user_message"
	WSTypeStats              WSMessageType = "stats"
	WSTypeSummarization      WSMessageType = "summarization"
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

	// Frame-level names: what changed on the board. The constants above are per-turn events that ride
	// inside a task_event envelope.
	WSTypeTaskEvent     WSMessageType = "task_event"
	WSTypeBoardRefresh  WSMessageType = "board_refresh"
	WSTypeProjectCounts WSMessageType = "project_counts"
	WSTypeProjectBranch WSMessageType = "project_branch"
	WSTypeProjectPorts  WSMessageType = "project_ports"
	WSTypeTaskUpdated   WSMessageType = "task_updated"
	WSTypeTaskDeleted   WSMessageType = "task_deleted"
	WSTypeTaskDiffStats WSMessageType = "task_diff_stats"
	WSTypeCommitStatus  WSMessageType = "commit_status"
	WSTypeFsEvent       WSMessageType = "fs_event"
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

// TurnStatus is the data of a WSTypeStatus frame. It is a type rather than a map because the
// senders and the reader of the frame's *purpose* — stream's SendTaskEvent, which drops a task's
// replay buffer when the turn ends — have to agree on it by construction: a payload read with a
// type assertion skips that decision in silence.
//
// Activity is declared first because encoding/json writes struct fields in declaration order and
// a map's keys sorted: this order is what keeps the wire bytes of the map payload it replaced.
type TurnStatus struct {
	Activity string `json:"activity"`
	State    string `json:"state"`
}

// TurnStatusEvent builds the WSTypeStatus frame for one turn state. `state` is the client's
// badge vocabulary, which is wider than ExecutionState: "cancelled" is a response replaced by a
// new message, not an execution state.
func TurnStatusEvent(state, activity string) StreamEvent {
	return StreamEvent{Type: WSTypeStatus, Data: TurnStatus{Activity: activity, State: state}}
}

// TerminalMessage is the envelope for terminal WebSocket frames.
type TerminalMessage struct {
	Type string `json:"type"`
	Data string `json:"data"`
	Rows int    `json:"rows,omitempty"`
	Cols int    `json:"cols,omitempty"`
}
