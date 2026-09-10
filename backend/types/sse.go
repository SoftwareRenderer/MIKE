package types

// SSEMessageType is the vocabulary of the clone stream, read by api/projects.ts. It is separate from the
// app-stream WebSocket because `status`, `done` and `error` mean different things on each socket.
type SSEMessageType string

const (
	SSETypeStatus   SSEMessageType = "status"
	SSETypeProgress SSEMessageType = "progress"
	SSETypeDone     SSEMessageType = "done"
	SSETypeError    SSEMessageType = "error"
)
