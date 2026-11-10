# ADR-0002 — A project image build runs in a sandbox container, over a subtree of a commit

**Status:** accepted

**Rules that follow:** `docs/sandbox-runtime.md → A project image builds inside a sandbox container`, `docs/sandbox-runtime.md → Extension sandbox mounts`, `docs/extension-lifecycle.md → The build context is created by this package, and it is the jail`

## Abstract

A project's sandbox image is built by a one-shot gVisor container, not by a host `podman build`. Its inputs
are a subtree of a pinned commit that MIKE stages, never a working tree; its permissions are MIKE's own fixed
grant plus the project's memory limit and environment, never the project's capability list; the finished image
is pushed out through a bind mount and loaded on the host. No build step runs on the host runtime, and no
untracked host file is ever build input.

## Context

A host `podman build` executes its steps on the host runtime — `--runtime` does not affect a build, as
`backend/extensions/entry_guard.go` records — which is outside gVisor and outside the `--cap-drop ALL` posture
every other MIKE container starts from. Letting a build read repository files therefore forces a decision
about what a build may read, and the two obvious answers are both wrong: the checkout holds untracked host
files (`.env`, the database) and usually `.worktrees/`, with every task's in-flight edits in it; and a task
worktree is unreviewed content feeding an image named after the *project*, which every later task boots.

## Decision

`runBuildContainer` runs the build from the argument list `buildContainerArgs` builds. What reaches that list
from the project is its memory limit, its configured environment and its inherited mounts; everything else is
MIKE's: the image is the resolved base ref, the worktree argument is the staged subtree rather than a directory
that already exists, every inherited mount is read-only whatever mode its row spells, and no port is
published. The build's own file goes whole to a nested builder inside that container —
`buildah --isolation=chroot`, measured green under runsc — so MIKE models no build step itself.

The staged subtree is a `git archive` of one pinned commit holding the tracked files under the configured
context root (`StageBuildContext`). That root is a repository path a person set, and its default is the
directory holding the file the build is built from, so the widest build input is always a value from
Settings. Untracked files, `.git` and the worktrees directory are not in the set rather than excluded by a
rule, and the entry count and the byte total are bounded before a byte is written
(`fsutil.CopyMaxEntries`).

**The guest never fetches; the host hands every image in.** A base the host lacks is pulled by the host,
through the same qualified-ref rules every image MIKE uses goes through, and exported with `podman save` into
the read-only `/mike-base`. `--pull=never` holds even though a build has a network: the container environment
carries no registry credentials by design, and a fetch inside the guest would let a spelling MIKE never handed
in resolve to something else. The digests actually used are recorded on the built image (`mike.base-image`),
which is the only durable answer to "which bytes". This host-side pull is the one action build input makes the
host take on the operator's network, with the operator's credentials, before any container exists.

**An image named by build input is qualified, never looked up locally.** A short ref means what the registry
means by it (`docker.io/library/…`). `ResolveImageRef`'s local-store lookup exists for *stored* settings and is
deliberately not applied here: applied to repository text it let a tracked file copy an image out of the host's
store into the project, measured with no network involved. An image in the host's store is otherwise bytes no
sandboxed process can reach. `TestBuildCannotNameAHostLocalImageByShortName` holds the rule, and an explicit
`localhost/…` still builds because that spelling is one a person reviewed.

**A build always has a network, and `sandbox_network_isolation` is not consulted.** That switch answers a
question about the agent — can the thing editing my code phone home. A build fetching what a project asks for
is MIKE running a person's build file, and one switch answering both questions means an isolated project
cannot build anything that installs a dependency. The cost is stated in the doc that owns the subsystem and
not in Settings: an isolated project still has build-time egress. Three things bound a build instead, and none
of them is its reach — what it can read (the staged context, the project's mounts read-only, the grant below),
where it can write (its own scratch directory), and the fact that it publishes no port. Measured 2026-10-30
through the product's own arguments: a step fetched a public page, and the guest could not reach the host's
loopback — the gateway is the CNI address, not `127.0.0.1` — so a build cannot call MIKE's own API.
`TestBuildReachesTheNetworkEvenForAnIsolatedProject` asserts the route exists; a Go test reaches no network, so
the fetch itself stays a measurement (`make build-probe`).

**The artifact leaves through a bind mount, because a guest write is invisible to the host.**
`GVisorCreateArgs` sets `overlay2=root:self`, which keeps the root overlay inside the guest: the container's
`UpperDir` measured 0 bytes with a file written into it, running and stopped. So `podman commit`,
`podman export` and `podman cp` out see nothing, and a commit after a build step would silently yield an image
without it. The builder writes a `docker-archive` to the one read-write bind (`/mike-out`) and the host
`podman load`s it, from a directory MIKE created and destroys on every exit. That staged context arrives by
the same route rather than by `podman cp`, which streams a tar the builder image does not ship and dies on a
closed pipe. The load is where build input becomes the trusted base of the project's later tasks, so anything
MIKE checks about provenance belongs there and nowhere earlier.

**The grant is one named constant, tested whole.** A build's capabilities are `sandboxruntime.BuilderGrant` —
the fourteen names the nested builder hands each of its steps — with `BuildGrant`'s three (`SETUID`, `SETGID`,
`SETFCAP`) as the measured floor inside it: each of the three is necessary, every pair failing at the
`/proc/<pid>/uid_map` or `gid_map` write. A build reads **none** of the project's Capabilities, because that
row describes the container the agent runs in and a build is not the agent. Granting a project's own list
*plus* the floor is the shape that cannot build: a hand-assembled list can sit short of the fourteen, and
denied the set it wants, buildah takes the path that also raises `RLIMIT_NOFILE` and `RLIMIT_NPROC` to
`1048576`, which a rootless gVisor guest — measured inside the builder image at hard `nofile=524288`,
`nproc=63710` — cannot do without `CAP_SYS_RESOURCE`. `podman create` names the capabilities it refused and
the line is discarded, because the create succeeded.

Adding privileges is not monotone either. Measured on the development host 2026-10-30, each stage judged by
the tag existing in the guest's own store — three instruments that read the builder's output instead each
reported a pass that had not happened:

| grant | build |
|---|---|
| nothing | fails at `writing "0 0 4294967295" to /proc/<pid>/gid_map` |
| `SETUID SETGID SETFCAP` | builds, runs a step, and survives `chown -R`, `chmod`, `ln -s`, a hard link, `touch -d` and a `tar` |
| the three ∪ Docker's default set (14) | builds |
| the same, less `CHOWN` (13) | builds |
| that set plus `SYS_ADMIN` and `SYS_RESOURCE` (15) | builds |
| the same fifteen, less `CHOWN` (14) | **fails extracting the base layer at `lchown /etc/shadow: operation not permitted`** |
| Docker's default set less `KILL` and `NET_BIND_SERVICE`, plus `SYS_ADMIN` and `SYS_PTRACE` | **fails at `setting "RLIMIT_NOFILE" limit to soft=1048576,hard=1048576 (was soft=524288,hard=524288): operation not permitted`** (2026-10-31) |
| the three plus `SYS_ADMIN` alone | **fails extracting the base layer at `setting up pivot dir: permission denied`** (2026-10-31) |

The failure mode is a set with a **hole** in it — privileges wide enough to move buildah off the
user-namespace path, without the `CHOWN` that path's absence then requires — not a large grant, and not one
poisoned name. So a candidate grant is tested whole rather than derived from a set that worked, and the
shipped answer is a constant nobody assembles. A step needing a privilege outside that set is a build this
venue declines rather than a grant the list grows to cover; `buildFailureHint` states the fixed grant instead
of handing over the builder's line, and `TestARunStepNeedingMoreThanTheBuildGrantSaysSo` holds the refusal
through the product's own argument path.

**The layer store is `vfs` on the container's rootfs, under `/var/tmp/mike-build`.** A `vfs` layer written
under the guest's tmpfs fails its own extended-attribute read ("no such file or directory") — that is how the
`/tmp` variant was ruled out, on a builder image whose `/tmp` was a tmpfs; the image of 2026-11-10 has `/tmp`
on the container's root filesystem too. `overlay` inside the guest fails at its first mount ("transport
endpoint is not connected"); `fuse-overlayfs` would need `/dev/fuse`, which this package has no grant shape
for. `--tmpfs …:size=` is honoured under runsc and is simply unusable for a store, so the disk bound is taken
from the other side: the output directory is the one artifact visible from the host, `buildScratch.Watch`
samples it while the build runs, and past the cap the build is stopped.

**The builder's temp directory is on the bind, never on the container's root.** Since buildah v1.44 every build
begins by mounting an overlay **over the build context** (`platformSetupContextDirectoryOverlay`,
`ForceMount: true`): there is no fallback to a bind mount and no flag that declines the mount, so a guest that
cannot make it cannot build at all. That mount's upper and work directories go under `$TMPDIR`, whose fallback
is `/var/tmp`. gVisor takes only a tmpfs or a gofer (9p) filesystem as an overlay upper for a mount made
through `mount(2)` (`pkg/sentry/fsimpl/overlay/overlay.go`), and the build container's root is the overlay
GVisor itself mounted (`overlay2=root:self`) — with `--image-volume ignore`, the builder image's own
`VOLUME /var/tmp` stays a plain directory on it. So the default temp path dies before step 1:
`mount overlay:…,userxattr: invalid argument`. The build container therefore exports `TMPDIR` into its one
writable bind (`sandboxruntime.BuildTempDir`), which the host makes inside the watched output tree and the
guest never creates. `userxattr` is not the refusal and never was: gVisor implements it, and forces it for a 9p
upper. A tmpfs answers the mount too and is declined anyway, because `$TMPDIR` also carries a base image being
copied into the guest's store — a tmpfs prices those bytes in guest RAM against the project's memory limit,
where the bind puts them under the cap `buildScratch.Watch` already enforces. Measured 2026-11-10 with buildah
1.45.1 under runsc release-20260721.0 through the product's own arguments, both the failure and the fix; the
same build on the host kernel (crun) never sees the rule, which is why this is a venue constraint and not a
buildah bug.

**The builder ref floats, so a green build is a statement about one image.** `BuilderImageDefault` is
`quay.io/buildah/stable:latest`, fetched by the host only when it is absent, which means the nested builder
changes between builds without any change here — the context overlay above arrived exactly that way, on hosts
that pulled and not on hosts that did not. The ref stays floating: a pinned builder is a known-bad path that
rots quietly, and this package cannot rebuild a pinned image when a base needs an OS fix. What makes the drift
visible instead is that the builder's own `io.buildah.version` is logged with every build, and that
`make build-probe` runs the shipped grant through a real build and reports the stage's own reason.

**One host path is written: the build's own scratch directory.** A directory a person chose is never writable
by a build. The host channel is the mount list, not the context — `types.ValidateSandboxMount` checks shape
(absolute, no `..`, not `/`) and nothing jails a mount's host path, since jailing it against the host root
would stop nothing while `$HOME` sits inside that root — so a build inherits every mount read-only and reports
a missing mount instead of `bindMountArg` creating one on the host.

## Consequences

**Cheaper.** A build sees fewer bytes than either container that exists today: no checkout, no task worktree,
no untracked file, and no `.git` it can write. Its inputs are a commit, so what ran is nameable after the
fact, and the host never executes build bytes.

**Narrower than it looks, and that is the price.** A build's layers live in one disposable container, so a
build container is single-use, its scratch directory grows while it runs, and layer caching is per-build: a
commit is one layer. Build-time egress exists even for a project with network isolation on. A tracked secret
inside the context subtree stays reachable, and nothing at this layer helps — the same bytes are reachable
from a task turn.

**Whoever controls a repository controls the toolchain every later task boots.** The image carries the
project's mounts, its configured environment and its credentials into a read-write worktree of a real
repository, and code from a hostile base runs in every later task, including the turns nobody reviews. The
task container is the same jail, and that is the whole answer: an image that stayed inside the build jail
would be useless. Checked by building rather than by argument, a hostile base gains arbitrary code execution
the product already grants, plus persistence; from inside a task container it reads only the worktree and the
project's configured mounts, and sends them only where the project's own network setting already allows. The
review diff is produced by host git reading the worktree, so an image cannot lie about it either. Tag drift
stays, priced as what it is: a reproducibility annoyance recorded by `mike.base-image`, not an exfiltration
path.

**The venue is the limit, not a reading of the input.** Everything a build step can do is what this container
and the nested builder allow: the builder resolves a path reference under the staged context and clamps one
that tries to escape it, gives a secret mount no secret store and a host-network request no interface, and
builds a multi-stage input over two images (measured in the nested builder). MIKE judges no build input at
this layer, so the one reading that has to be *complete* rather than conservative is the list of images the
host must fetch — a ref missed is a build that dies fetching it. `TestBuildPassesThroughWhatTheVenueAlreadyJails`
and `TestBuildRunsAMultiStageDockerfileOverTwoImages` hold both halves.

**What would reopen this.** *Per-layer caching:* nesting the builder was adopted because it measured green on
a two-line input, and `make build-probe` re-runs that measurement; a builder that cached across builds without
a shared writable store would take the single-use cost back. *The premise itself:* this record holds while a
build container can reach nothing worth harming. The day one can reach a credential, a writable host path,
MIKE's own API, or a privilege outside `BuilderGrant`, containment stops being the whole answer and the
decision is argued again. Both conditions are measurable rather than philosophical.

**Re-measured by.** `backend/sandbox/build_e2e_test.go` runs real builds and is the only place this record is
checked against a container rather than an argument list: committed bytes reach the image, a step's output
survives the `podman load`, and no container or scratch directory survives any exit. `make build-probe`
re-measures the nested-builder question and the whole grant ladder, judging each stage by the guest's own
store; it is not part of `make check`, because it pulls a builder image and takes minutes. Its decisive stage
is the shipped set, and `grant-without-chown` is the stage whose passing would falsify the table above.
