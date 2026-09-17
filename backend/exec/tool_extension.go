package exec

import (
	"context"
	"fmt"
	"time"

	"mike/backend/extensions"
	"mike/backend/toolresult"
	"mike/pkg/logging"
)

// A contributed tool's definition, endpoint and answer shape all come from an extension's manifest; this
// file is the orchestration around it and knows nothing about the upstream.
//
// It runs in this process, so a project's network isolation does not cover it: that bounds the container,
// and this call never enters it.

// The production implementation is extensions.Egress; a test swaps in an answer.
type egressFunc func(ctx context.Context, ext *extensions.Extension, req extensions.EgressRequest) (*extensions.EgressResult, error)

func (r *toolRunner) runContributedTool(ctx context.Context, name string, args map[string]any, env *toolEnv) (string, bool) {
	var reg *extensions.ToolRegistry
	if r.extTools != nil {
		reg = r.extTools()
	}
	contrib, ok := reg.Lookup(name)
	if !ok {
		// The same wording a core tool uses for an unknown name.
		return fmt.Sprintf("Unknown tool: %s", name), false
	}
	ext, tool := contrib.Extension, contrib.Tool

	request, err := tool.FillRequest(args, ext.Config)
	if err != nil {
		return failed(err)
	}

	timeout := time.Duration(tool.Request.TimeoutSecondsOrDefault()) * time.Second
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	egress := extensions.EgressRequest{
		URL:    request.URL,
		Method: request.Method,
		// The only hosts the egress policy may reach at a loopback address.
		GrantedHosts: ext.GrantedHosts(),
		Timeout:      timeout,
		Source:       name,
	}
	if request.Method == "POST" {
		egress.Form = request.Query
	}

	result, err := r.egressOrReal()(callCtx, ext, egress)
	if err != nil {
		logging.For(logging.Tools).With(logging.TaskID, env.task.ID).Warnf(
			"%s via extension=%s failed: %v", name, ext.Name, err)
		return failed(err)
	}
	if !result.OK() {
		failure := fmt.Errorf("%s answered HTTP %d%s", ext.Manifest.DisplayName, result.StatusCode, extensions.FailureHint(result))
		logging.For(logging.Tools).With(logging.TaskID, env.task.ID).Warnf(
			"%s via extension=%s answered %d", name, ext.Name, result.StatusCode)
		return failed(failure)
	}

	answer, err := tool.Response.RenderAnswer(result.Body, result.IsJSON())
	if err != nil {
		logging.For(logging.Tools).With(logging.TaskID, env.task.ID).Warnf(
			"%s via extension=%s: unreadable answer: %v", name, ext.Name, err)
		return failed(err)
	}
	logging.For(logging.Tools).With(logging.TaskID, env.task.ID).Infof(
		"%s via extension=%s ok bytes=%d", name, ext.Name, len(answer))
	return capContributedAnswer(answer), true
}

func (r *toolRunner) egressOrReal() egressFunc {
	if r.egress != nil {
		return r.egress
	}
	return extensions.Egress
}

// The backstop after the declaration's own caps, capped like a command's output.
func capContributedAnswer(answer string) string {
	if len(answer) <= toolresult.MaxCaptureBytes {
		return answer
	}
	dropped := len(answer) - toolresult.MaxCaptureBytes
	return answer[:toolresult.MaxCaptureBytes] + toolresult.CaptureMarker(dropped)
}
