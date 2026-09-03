package lsp

// A granted dependency directory is used by two containers, and they must not disagree about where
// it is: the extension's own sandbox mounts it read-only (the server reads a cache, it does not
// build one), and the task sandbox mounts it read-write so the toolchain in a turn can warm it. The
// extension side derives both halves of that in backend/extensions; this file is the other door —
// the question the task sandbox asks, phrased without any knowledge of what a module cache is.

import (
	"log"
	"sort"

	"kanban/backend/extensions"
	"kanban/backend/types"
)

// DependencyCache is one granted dependency directory in the shape a container needs it: the host
// directory, where to mount it, and the variable that points a toolchain at it. There is no mode
// here on purpose — the mode is the *consumer's* decision (read-only for the language server,
// read-write for the task that fills it), not something the extension declares.
type DependencyCache struct {
	HostPath      string
	ContainerPath string
	// Env is the variable that points a toolchain at the directory. Its value is always
	// ContainerPath — the host path means nothing to a process inside a sandbox — which is why there
	// is no second field: two that must always agree are two that can disagree.
	Env string
}

// DependencySandboxArgs is the one translation from the union above to what a container create
// needs. Both consumers — a task's container and a sandboxed terminal — call it, because two copies
// would mean one of them mounts the cache read-only, or points the variable at the host path, and
// the symptom is a toolchain that quietly re-downloads the world.
//
// The mode is read-write whatever the declaration said: these are the containers that *fill* the
// cache, and the extension's own read-only sandbox is assembled elsewhere.
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

// DependencyCaches reports what every *enabled* LSP extension could bind right now, for the task
// sandbox. Both halves come from the same derivation the extension sandbox uses
// (DesiredDependencyMount), so a directory that has stopped existing, or a grant that is not a root
// for its own extension, is absent here exactly as it is absent there.
//
// Two extensions can declare the same container path or the same variable — a second Go language
// server declares /go/pkg/mod and GOMODCACHE too. Extensions are walked in ID order so the winner
// is the same process after a restart as before, and the loser is reported rather than quietly
// dropped: a silently-lost cache is a language server that answers from an empty directory while a
// perfectly good one sits mounted somewhere else.
func (m *ExtensionLSPManager) DependencyCaches() []DependencyCache {
	if m == nil || m.extStore == nil || m.extSandbox == nil {
		return nil
	}
	exts, err := m.extStore.GetByType(extensions.ExtensionTypeLSP)
	if err != nil {
		log.Printf("[lsp] dependency caches: cannot list extensions: %v", err)
		return nil
	}
	// Store order is not a contract; ID order is, or which of two extensions claims a path would
	// change between two runs of the same code.
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
			// Nothing to report here: this extension's own container start says the same thing,
			// with the reason attached to the extension the user was looking at.
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
			// The mount is still worth making — a server that reads the directory through a
			// different route gets it too — but only the winner's assignment is published, so a
			// variable never points at a directory that is not mounted.
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
