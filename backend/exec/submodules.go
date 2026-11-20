package exec

// A task never reaches the network for a submodule. What it does instead is stop and say what is missing, so
// the person decides in the task's chat — from the addresses the notice lists — whether to pull them for the
// project. The one answer that decides anything here is the project's own: sandbox_network_isolation says
// whether anything this project runs ever leaves the machine.
//
// docs/git-operations.md → Submodules: share the person's copy, never re-fetch.

import (
	"errors"

	"mike/backend/git"
	"mike/backend/types"
)

// prepareSubmodules refuses a task whose own commit names a submodule the person's checkout never fetched.
//
// Nothing is fetched here. A submodule that appeared after the project was added is the repository's decision,
// and MIKE dials its address with the operator's credentials, so the approval is a click in the chat: the row
// goes to Review with the path, the address and MIKE's reason in it, and the card turns that into the notice
// with the button. A task is never left running on an empty submodule directory.
func (te *TaskExecutor) prepareSubmodules(setup *taskExecSetup) error {
	unpopulated, err := te.unpopulatedSubmodules(setup)
	if err != nil || len(unpopulated) == 0 {
		return err
	}
	return &git.SubmodulesUninitialized{Project: setup.project.Path, Submodules: unpopulated}
}

// unpopulatedSubmodules asks the git layer what this task's own commit names and the person's checkout does
// not hold. The rows carry the address and MIKE's own reason, so the notice and the refusal say the same thing.
func (te *TaskExecutor) unpopulatedSubmodules(setup *taskExecSetup) ([]git.SubmoduleState, error) {
	states, err := git.SubmoduleStates(setup.ctx, setup.worktreePath, setup.project.Path, te.submoduleFetch)
	if err != nil {
		return nil, err
	}
	var out []git.SubmoduleState
	for _, s := range states {
		if !s.Populated {
			out = append(out, s)
		}
	}
	return out, nil
}

// allowStoreFetch answers whether a task may answer a stale submodule pointer with one fetch into the person's
// shared store. That submodule was approved when its copy arrived, and keeping the store current is what the
// copy is for. Isolation still refuses it: nothing a project runs reaches the network then, and this fetch
// leaves the machine from the server process, over the store's own recorded remote.
func allowStoreFetch(p *types.Project) bool {
	return !p.SandboxNetworkIsolation
}

// submoduleFailCode is the code a failed sandbox setup is reported under. An uninitialized submodule gets its
// own because the card answers it with a button rather than a retry; everything else keeps the step it came
// from.
func submoduleFailCode(err error) string {
	var needInit *git.SubmodulesUninitialized
	if errors.As(err, &needInit) {
		return "submodules_uninitialized"
	}
	return "sandbox_setup_failed"
}
