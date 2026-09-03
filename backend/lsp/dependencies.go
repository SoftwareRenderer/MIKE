package lsp

// A granted dependency directory is used by two containers that must not disagree about where it is:
// the extension's own sandbox mounts it read-only (the server reads a cache, it does not build one),
// the task sandbox mounts it read-write so a turn's toolchain can warm it. backend/extensions derives
// the extension side; this file answers the task sandbox's question, with no knowledge of what a
// module cache is.

import (
	"log"
	"sort"

	"kanban/backend/extensions"
	"kanban/backend/types"
)

// DependencyCache is one granted directory in the shape a container needs: host path, container path,
// and the variable pointing a toolchain at it. No mode field: the mode is the consumer's decision
// (read-only for the language server, read-write for the task that fills it).
type DependencyCache struct {
	HostPath      string
	ContainerPath string
	// Its value is always ContainerPath — the host path means nothing inside a sandbox — so there is
	// no second field that could disagree with the first.
	Env string
}

// DependencySandboxArgs translates that list into a container create. Both consumers — a task's
// container and a sandboxed terminal — call it, so they cannot drift into disagreeing about the mode
// or the variable's value.
//
// The mode is read-write whatever was declared: these containers fill the cache; the extension's
// read-only sandbox is assembled elsewhere.
func DependencySandboxArgs(caches []DependencyCache) ([]types.SandboxMount, []types.SandboxEnv) {
	var mounts []types.SandboxMount
	var envs []types.SandboxEnv
	for _, d := range caches {
		if d.HostPath == "" || d.ContainerPath == "" {
			continue
		}
		mounts = append(mounts, types.SandboxMount{HostPath: d.HostPath, ContainerPath: d.ContainerPath, Mode: "rw"})
		if d.Env != "" {
			envs = append(envs, types.SandboxEnv{Key: d.Env, Value: d.ContainerPath})
		}
	}
	return mounts, envs
}

// DependencyCaches reports what every enabled LSP extension can bind right now, for the task sandbox.
// It uses the same derivation the extension sandbox uses (DesiredDependencyMount), so a directory that
// stopped existing, or a grant that is not a root for its own extension, is absent here as it is there.
//
// Two extensions can declare the same container path or variable. Extensions are walked in ID order so
// the winner survives a restart, and a loser is logged rather than dropped quietly: an extension
// resolving from an empty directory, with a good cache mounted elsewhere, looks like a broken server.
func (m *ExtensionLSPManager) DependencyCaches() []DependencyCache {
	if m == nil || m.extStore == nil || m.extSandbox == nil {
		return nil
	}
	exts, err := m.extStore.GetByType(extensions.ExtensionTypeLSP)
	if err != nil {
		log.Printf("[lsp] dependency caches: cannot list extensions: %v", err)
		return nil
	}
	// ID order, not store order: otherwise which of two extensions claims a path would change
	// between two runs of the same code.
	sort.Slice(exts, func(i, j int) bool { return exts[i].ID < exts[j].ID })

	var out []DependencyCache
	claimedPath := map[string]string{} // container path -> extension that claimed it
	claimedEnv := map[string]string{}  // variable -> extension that claimed it
	for i := range exts {
		ext := &exts[i]
		if !ext.Enabled {
			continue
		}
		bind := m.extSandbox.DesiredDependencyMount(ext)
		if !bind.OK {
			// Its own container start reports this one, with the reason attached to the extension
			// the user was looking at.
			continue
		}
		if other, dup := claimedPath[bind.Mount.ContainerPath]; dup {
			log.Printf("[lsp] SKIPPED dependency cache of %s: the container path %s is already claimed by %s — one of the two extensions will resolve its dependencies without a granted directory",
				ext.ID, bind.Mount.ContainerPath, other)
			continue
		}
		claimedPath[bind.Mount.ContainerPath] = ext.ID
		envKey := bind.Env.Key
		cache := DependencyCache{HostPath: bind.Mount.HostPath, ContainerPath: bind.Mount.ContainerPath}
		if other, dup := claimedEnv[envKey]; dup && other != ext.ID {
			// Still mount it — a server that reads the directory another way gets it too — but
			// publish only the winner's assignment, so a variable never points at nothing.
			log.Printf("[lsp] %s: %s is already assigned by %s, leaving it alone", ext.ID, envKey, other)
		} else if envKey != "" {
			claimedEnv[envKey] = ext.ID
			cache.Env = envKey
		}
		// The mount is published whether or not this extension also won the variable: a second Go
		// server mounted at the same path reads the same cache through the first one's assignment.
		out = append(out, cache)
	}
	return out
}
