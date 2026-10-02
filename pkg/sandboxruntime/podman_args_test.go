package sandboxruntime

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestValidateContainerPathSharedRule is the tape every container's mounts are
// measured with, task sandbox or extension sandbox. The two characters that must
// never appear are the ones the podman mount spec is built out of: a container path of
// "/workspace:rw" is not a weird name, it is a second option field, and it is how a
// read-only mount quietly becomes writable.
func TestValidateContainerPathSharedRule(t *testing.T) {
	for _, ok := range []string{"/workspace/node_modules", "/go", "/tmp/x.y-z_w", "/a/b/c"} {
		if err := ValidateContainerPath(ok); err != nil {
			t.Errorf("ValidateContainerPath(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"", "relative/path", "/", "//", "/.", "/workspace:rw", "/workspace,rw",
		"/workspace/../etc", "..", "/etc/../", "/workspace/..", "/home/$(id)", "/a b",
		"/work\tspace",
	} {
		err := ValidateContainerPath(bad)
		if err == nil {
			t.Errorf("ValidateContainerPath(%q) = nil, want error", bad)
			continue
		}
		if bad == "/workspace:rw" && !strings.Contains(err.Error(), "rewrite the podman mount spec") {
			t.Errorf("ValidateContainerPath(%q) should say why: %v", bad, err)
		}
	}
}

// TestHasDotDotSegment is the near-miss half of the rule: a segment that merely
// contains ".." is a legal file name, and refusing one would refuse real mounts.
func TestHasDotDotSegment(t *testing.T) {
	for _, p := range []string{"/a/../b", "..", "/../a", "a/..", "/a/.."} {
		if !HasDotDotSegment(p) {
			t.Errorf("HasDotDotSegment(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"/a/..b", "/a/b..", "/..hidden/a", "/a"} {
		if HasDotDotSegment(p) {
			t.Errorf("HasDotDotSegment(%q) = true, want false", p)
		}
	}
}

// TestValidateCapabilityRule pins what the capability rule is now: the *shape* of the
// value, never its name. A capability granted to a gVisor container is a guest privilege
// (see GVisorIsolationForCaps), so SYS_ADMIN is accepted here and the runtime check at the
// call site is what keeps that true. What stays refused is anything that would arrive at
// `--cap-add` as something other than one name — that is argument injection, and no
// runtime makes it safe.
func TestValidateCapabilityRule(t *testing.T) {
	for _, ok := range []string{
		"NET_RAW", "CHOWN", "SETFCAP", "cap_net_raw", "SYS_TIME", "fowner",
		// Every name the denylist used to refuse: allowed now, because the container
		// they land in is a gVisor guest that started from --cap-drop ALL.
		"SYS_ADMIN", "SYS_PTRACE", "SYS_MODULE", "SYS_RAWIO", "DAC_OVERRIDE",
		"DAC_READ_SEARCH", "NET_ADMIN", "MKNOD", "SETUID", "SETGID", "SYS_BOOT",
		"LINUX_IMMUTABLE",
	} {
		if err := ValidateCapability(ok); err != nil {
			t.Errorf("ValidateCapability(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"", "   ", "--privileged", "--cap-add", "-v", "SYS_ADMIN ALL", "SYS_ADMIN,ALL",
		"SYS-ADMIN", "SYS.ADMIN", "0SYS", "SYS;REBOOT", "SYS\nADMIN",
	} {
		if err := ValidateCapability(bad); err == nil {
			t.Errorf("ValidateCapability(%q) = nil, want error", bad)
		}
	}
	// The list form names the first offender, because it answers a 400.
	if err := ValidateCapabilities([]string{"CHOWN", "--privileged"}); err == nil {
		t.Error("ValidateCapabilities accepted a list holding an option-shaped value")
	} else if !strings.Contains(err.Error(), "--privileged") {
		t.Errorf("error %q should name the offending entry", err.Error())
	}
	if err := ValidateCapabilities([]string{"CHOWN", "SYS_ADMIN"}); err != nil {
		t.Errorf("ValidateCapabilities(valid names) = %v, want nil", err)
	}
}

// TestGVisorIsolationForCaps pins the pairing a capability grant now relies on. The
// denylist used to be the defence; it is gone, and what is left is the fact that the
// container is runsc-isolated and started with every capability dropped. A command line
// missing either turns `--cap-add SYS_ADMIN` into a host-kernel privilege, which is
// exactly the regression this refuses.
func TestGVisorIsolationForCaps(t *testing.T) {
	isolated := []string{"create", "--runtime", "runsc", "--cap-drop", "ALL"}
	if err := GVisorIsolationForCaps(isolated); err != nil {
		t.Errorf("GVisorIsolationForCaps(%v) = %v, want nil", isolated, err)
	}

	cases := map[string][]string{
		"no runtime selected":    {"create", "--cap-drop", "ALL"},
		"a different runtime":    {"create", "--runtime", "crun", "--cap-drop", "ALL"},
		"caps not dropped":       {"create", "--runtime", "runsc"},
		"caps partially dropped": {"create", "--runtime", "runsc", "--cap-drop", "SYS_ADMIN"},
	}
	for name, args := range cases {
		if err := GVisorIsolationForCaps(args); err == nil {
			t.Errorf("%s: GVisorIsolationForCaps(%v) = nil, want a refusal", name, args)
		}
	}
}

// indexOfArg returns the first index of want, or -1.
func indexOfArg(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

// TestGVisorCreateArgsIsTheIsolatedShape pins the shared preface as a whole: its exact contents,
// that it is by itself the shape GVisorIsolationForCaps demands of a capability grant (so the
// builders cannot compose a command line that their own append path then refuses), that it carries
// the `create` verb rather than expecting a caller to supply it, and that no per-sandbox choice or
// container environment crept into it — an `-e PATH` override dropped a custom image's own tooling,
// and TERM belongs to the TTY session that asks for it.
func TestGVisorCreateArgsIsTheIsolatedShape(t *testing.T) {
	got := GVisorCreateArgs("kanban-sandbox-task-1")
	want := []string{
		"--runtime-flag=overlay2=root:self",
		"--runtime-flag=ignore-cgroups",
		"create",
		"--pull", "never",
		"--name", "kanban-sandbox-task-1",
		"--runtime", "runsc",
		"--security-opt", "label=disable",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--entrypoint", "",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GVisorCreateArgs =\n%v\nwant\n%v", got, want)
	}
	if err := GVisorIsolationForCaps(got); err != nil {
		t.Errorf("the shared preface is not the isolated shape a grant assumes: %v", err)
	}
	for _, absent := range []string{"--workdir", "--network", "-e", "--env", "-v", "--memory"} {
		if indexOfArg(got, absent) != -1 {
			t.Errorf("%s is a per-sandbox choice, not the shared posture: %v", absent, got)
		}
	}
	// Every builder appends to what this returns, so a caller's append must not reach another
	// caller's command line.
	first, second := GVisorCreateArgs("a"), GVisorCreateArgs("b")
	first = append(first, "--network", "none")
	if !reflect.DeepEqual(second, GVisorCreateArgs("b")) {
		t.Errorf("appending to one preface changed another's: first=%v second=%v", first, second)
	}
}

// TestAppendCapabilitiesRefusesAnUnisolatedGrant is the pairing moved out of the tests and into the
// append path: without runsc, or without the full drop, no --cap-add may land, and the argument
// list comes back exactly as it went in — a partial grant is a privilege set nobody asked for.
func TestAppendCapabilitiesRefusesAnUnisolatedGrant(t *testing.T) {
	cases := map[string][]string{
		"no runtime selected":    {"create", "--cap-drop", "ALL"},
		"a different runtime":    {"create", "--runtime", "crun", "--cap-drop", "ALL"},
		"caps not dropped":       {"create", "--runtime", "runsc"},
		"caps partially dropped": {"create", "--runtime", "runsc", "--cap-drop", "SYS_ADMIN"},
	}
	for name, args := range cases {
		out, err := AppendCapabilities(args, []string{"CHOWN", "SYS_ADMIN"})
		if err == nil {
			t.Errorf("%s: AppendCapabilities = nil error, want a refusal", name)
		}
		if !reflect.DeepEqual(out, args) {
			t.Errorf("%s: a refusal changed the argument list:\n got %v\nwant %v", name, out, args)
		}
	}

	// On the isolated shape the grant lands, in order, after the drop.
	isolated := GVisorCreateArgs("kanban-ext-x")
	out, err := AppendCapabilities(isolated, []string{"CHOWN", "SYS_ADMIN"})
	if err != nil {
		t.Fatalf("AppendCapabilities on an isolated command line = %v", err)
	}
	down, added := indexOfArg(out, "--cap-drop"), indexOfArg(out, "--cap-add")
	if added < down {
		t.Errorf("--cap-add at %d must follow --cap-drop at %d: %v", added, down, out)
	}
	if got := out[len(isolated):]; !reflect.DeepEqual(got, []string{"--cap-add", "CHOWN", "--cap-add", "SYS_ADMIN"}) {
		t.Errorf("appended %v", got)
	}
}

// TestWorktreeGitPointerMountIsReadOnly asserts the whole spec because `ro` in the wrong field of
// this string is the bug: the file the rw worktree mount exposes is mounted again, read-only, over the
// guest's view of it.
func TestWorktreeGitPointerMountIsReadOnly(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, GitPointerFile), []byte("gitdir: /project/.git/worktrees/t1\n"), 0o644); err != nil {
		t.Fatalf("seed pointer: %v", err)
	}

	got, err := WorktreeGitPointerMount(worktree, "/home/sandbox/proj")
	if err != nil {
		t.Fatalf("WorktreeGitPointerMount: %v", err)
	}
	want := []string{"-v", filepath.Join(worktree, GitPointerFile) + ":/home/sandbox/proj/.git:rprivate,ro"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WorktreeGitPointerMount() = %q, want %q", got, want)
	}
}

// TestWorktreeGitPointerMountRefusesWithoutAPointer is the fail-closed half: no call returns mount
// arguments unless a real pin exists, so `podman create` can never run without one.
func TestWorktreeGitPointerMountRefusesWithoutAPointer(t *testing.T) {
	empty := t.TempDir()

	notAFile := t.TempDir()
	if err := os.Mkdir(filepath.Join(notAFile, GitPointerFile), 0o755); err != nil {
		t.Fatalf("seed .git directory: %v", err)
	}

	for name, dir := range map[string]string{"no pointer": empty, ".git is a directory": notAFile} {
		if got, err := WorktreeGitPointerMount(dir, "/home/sandbox/proj"); err == nil {
			t.Errorf("%s: expected refusal, got %q", name, got)
		}
	}
	if got, err := WorktreeGitPointerMount(empty, "/home/sandbox/../etc"); err == nil {
		t.Errorf("a container path that is not valid should refuse the mount, got %q", got)
	}
}

// mustRel is the `..` walk from one directory to another, uncleaned on purpose: filepath.Join of it
// with its own start would resolve it.
func mustRel(t *testing.T, from, to string) string {
	t.Helper()
	rel, err := filepath.Rel(from, to)
	if err != nil {
		t.Fatalf("rel %s → %s: %v", from, to, err)
	}
	return rel
}

// TestVerifyWorktreeGitPointer requires the pointer to name this project's admin tree. The refused
// shapes are the attack: a gitdir the guest can write, reached directly or by walking out of the admin
// tree with `..`.
func TestVerifyWorktreeGitPointer(t *testing.T) {
	worktree := t.TempDir()
	projectGit := t.TempDir()
	sep := string(filepath.Separator)
	admin := filepath.Join(projectGit, "worktrees", "t1")
	if err := os.MkdirAll(admin, 0o755); err != nil {
		t.Fatalf("seed admin dir: %v", err)
	}
	pointer := filepath.Join(worktree, GitPointerFile)

	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(pointer, []byte(content), 0o644); err != nil {
			t.Fatalf("seed pointer: %v", err)
		}
	}

	for _, ok := range []string{
		"gitdir: " + admin + "\n",   // what `git worktree add` writes
		"gitdir: " + admin,          // no trailing newline
		"gitdir: " + admin + "\r\n", // a CRLF checkout
		"gitdir: " + admin + "/\n",  // a trailing separator
	} {
		write(ok)
		if err := VerifyWorktreeGitPointer(worktree, projectGit); err != nil {
			t.Errorf("VerifyWorktreeGitPointer(%q) = %v, want nil", ok, err)
		}
	}

	for _, refused := range []string{
		"gitdir: " + filepath.Join(worktree, ".git-evil"),
		"gitdir: " + filepath.Join(projectGit, "worktrees-alongside"),
		"gitdir: /tmp/someone-elses-repo/.git",
		"gitdir: ../outside",
		// The admin root holds worktrees; it is not one.
		"gitdir: " + filepath.Join(projectGit, "worktrees"),
		// Keeps the admin prefix in the text, walks out of it after cleaning.
		"gitdir: " + admin + sep + mustRel(t, admin, filepath.Join(worktree, ".git-evil")),
		// git writes one line.
		"gitdir: " + admin + "\ngitdir: " + filepath.Join(worktree, ".git-evil"),
		"/dev/null",
		"",
	} {
		write(refused)
		if err := VerifyWorktreeGitPointer(worktree, projectGit); err == nil {
			t.Errorf("pointer %q must be refused: it names a gitdir the guest can influence", refused)
		}
	}

	if err := os.Remove(pointer); err != nil {
		t.Fatalf("remove pointer: %v", err)
	}
	if err := VerifyWorktreeGitPointer(worktree, projectGit); err == nil {
		t.Error("a worktree with no pointer at all must be refused")
	}
}
