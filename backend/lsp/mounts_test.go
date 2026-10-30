package lsp

import "testing"

// TestShadowsToolState: borrowing a project's mounts is how the server reads its dependency roots,
// but borrowing the server's *own* cache or scratch space silences it — borrowed mounts are
// read-only.
func TestShadowsToolState(t *testing.T) {
	cases := []struct {
		containerPath string
		shadowed      bool
		why           string
	}{
		{"/root/.cache/go-build", true, "gopls' GOCACHE — the mount that silenced this repo's diagnostics"},
		{"/root/.cache", true, "the cache root itself"},
		{"/home/node/.cache", true, "a cache root under a non-root home, as node-based images have"},
		{"/.cache", true, "a cache root at the filesystem root"},
		{"/tmp", true, "scratch space the container expects to write"},
		{"/var/tmp", true, "the other scratch space"},
		{"/tmp/fixtures", false, "a subtree of tmpfs does not stop the container writing to /tmp"},
		{"/root/projects", false, "the user's data under the container's home is exactly what borrowing is for"},
		{"/go", false, "a toolchain root the user may share read-only"},
		{"/mnt/MIKE/frontend/node_modules", false, "dependencies under the workspace mount"},
		{"/mnt/MIKE/frontend/.cache/dist", false, "a project's own .cache is the user's file tree, not the tool's state"},
	}
	for _, c := range cases {
		if got := shadowsToolState(c.containerPath); got != c.shadowed {
			t.Errorf("shadowsToolState(%q) = %v, want %v (%s)", c.containerPath, got, c.shadowed, c.why)
		}
	}
}
