package exec

import "context"

// interruptedToolResult answers every tool call of an interrupted turn. A call that never started is
// answered too: the assistant row already asked for it, so the provider and the card are both waiting.
const interruptedToolResult = "Interrupted"

// toolCallContext returns the context one turn's tool calls run on, cancelled as soon as the user
// sends a message, so a message is not queued behind a command that runs for minutes. What the turn
// settles afterwards (commit, merge, checkpoint) runs on setup.ctx and is never interrupted.
//
// len(setup.userMsgChan) is the state and msgWake only the hint; the count can only grow while this
// watches, because the executor goroutine is that channel's only consumer.
func toolCallContext(setup *taskExecSetup) (ctx context.Context, cancel context.CancelFunc) {
	ctx, cancel = context.WithCancel(setup.ctx)
	pending := len(setup.userMsgChan)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-setup.msgWake:
				if len(setup.userMsgChan) > pending {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}
