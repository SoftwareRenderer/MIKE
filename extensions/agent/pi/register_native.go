//go:build !wasm

package pi

import (
	"kanban/backend/agent"
)

func init() {
	agent.RegisterAdapterFactory("pi", func() agent.AgentAdapter {
		return NewNativeAdapter()
	})
}
