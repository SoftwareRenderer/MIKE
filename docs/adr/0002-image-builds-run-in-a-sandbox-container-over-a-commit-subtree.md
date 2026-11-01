# ADR-0002 — A project image build runs in a sandbox container, over a subtree of a commit

**Status:** accepted (2026-10-30, landed with `backend/sandbox/build.go`)

**Rules that follow:** `docs/sandbox-runtime.md → A project image builds inside a sandbox container`, `docs/api-contract.md → A project image build is a stream with one control`, `docs/sandbox-runtime.md → Extension sandbox mounts`, `docs/extension-lifecycle.md → The build context is created by this package, and it is the jail`

## Abstract

A project's sandbox image is built by a one-shot gVisor container, not by the host runtime. Its
permissions come from the project's Sandbox Configuration unchanged — capabilities, environment, mounts,
memory, network — and its *inputs* come from a subtree of a pinned commit, staged into a directory MIKE
writes. The context defaults to the Dockerfile's own directory, so the widest build input is always a
value a person set. The build's network is deliberately not that value (see the correction below); the base
image is handed to it as an archive, so
no repository text can reach a socket through a build.

## Context

`BuildImageWithOptions` writes the project's Dockerfile into an empty temp directory and builds with that
as the context, so `COPY` of a repository file cannot work: the support is absent, which is also why it is
harmless. Meanwhile the build's `RUN` steps execute on the host runtime — `--runtime` does not affect a
build, as `backend/extensions/entry_guard.go` records — outside gVisor and outside the `--cap-drop ALL`
posture every MIKE container otherwise starts from. Supporting `COPY` therefore forces a choice about what
a build may read, and the two obvious answers are both wrong: the checkout holds untracked host files
(`.env`, the database) and usually `.worktrees/` with every task's in-flight edits, and a task worktree is
unreviewed content feeding an image named after the *project*, which every later task then boots.

## Decision

`Create()` runs the build, with `ProjectOpts(project)` as its permission set and four caller decisions on
top: the image is the resolved `FROM` ref, the worktree argument is the staged subtree instead of a
directory that already exists, every inherited mount is read-only regardless of its configured mode, and
the container publishes no ports. The Dockerfile is handed to a nested builder inside that container
whole — `buildah --isolation=chroot`, measured green — so MIKE models no instruction semantics at all. The
staged subtree is a `git archive` of a pinned commit containing the tracked files under the configured context
root; the root defaults to the directory holding the Dockerfile, and the Dockerfile itself is read from the
same commit rather than from the live path. What MIKE reads out of the file is the image refs and the command
set (see the correction below); `COPY` and `ADD` sources are not MIKE's to judge, because the builder resolves
them under the staged context — measured — and the read-only mount is what keeps them there.

**A build has a network, and it is not the project's network setting.** The host exports
each image the file names with `podman save` and the guest imports it, and a base the host lacks is pulled by
the host, through the same qualified-ref rules every image MIKE uses goes through. The network exists so `RUN`
steps can install what the Dockerfile asks for; the isolation switch is not consulted, because that switch is a
statement about the agent. The cost of the decoupling is written where a person will read it — Settings, and the
bullet in `docs/sandbox-runtime.md` — namely that an isolated project still has build-time egress.

The finished image is pushed by the builder as a `docker-archive` into a per-build scratch directory MIKE
created and mounted for that purpose, then `podman load`ed on the host. The build container's capability
grant is the project's configured set plus the measured builder minimum (see the correction below), and it is
**tested as a whole** rather than composed, because granting more can break a build that a smaller set
completes — see the grant's evidence below.

## Consequences

**Cheaper.** The build sees fewer bytes than either container that exists today — no checkout, no task
worktree, no untracked file at all, `.git` read-only as now — and because the inputs are a commit, what ran
is nameable after the fact. The builder pushes the artifact out and the host loads it, so the host never
executes repository bytes.

**No new egress, and one new input.** For a project whose network isolation is off, a task turn already has
a networked shell over its worktree and `.git`, so a networked build reaches what a turn reaches. What the
decision *does* create is `FROM`: a repository text now names an image ref that the **host** pulls. Validate
it with `ValidateConfiguredImageRef`, pin the digest after the pull, and consider an operator freeze
modelled on `MIKE_RESTRICT_MCP`. Review that as the widening it is.

**The host channel is the mount list, not the context.** `types.ValidateSandboxMount` checks shape —
absolute, no `..`, not `/` — and nothing jails a mount's host path; jailing it against the host root would
stop nothing, because `$HOME` is inside the host root. So a build inherits mounts read-only (`mountMode`
defaults to *rw* today, which is what a build of a repository should never get), and reports a missing
mount instead of `bindMountArg` creating it on the host.

**What the landing decided about the network, and what it reversed.** The first landing air-gapped every build
container (`--network none`, unconditionally) on the reasoning that a builder never needs the network because
its base image arrives as an archive the host pulled. That reasoning was true only of the *base image*. A `RUN`
step installs things, and most real Dockerfiles install things — `apk add`, `apt-get`, `pip install`, `go mod
download`, `npm ci` — so an unconditional `none` was a silent feature ban, paid for with a property nobody had
asked to protect at that price.

The second answer tied the build to `sandbox_network_isolation`, applied the way `SandboxManager.Create`
applies it for a task container, on the principle that one switch should mean one thing. It meant the
opposite: one word was made to answer two different questions — "can the thing editing my code phone home",
which is a question about the agent, and "may MIKE fetch the packages this Dockerfile asks for", which is a
question about MIKE running a person's build file. Setting the first off silently turned the second off too, so
an isolated project could not build anything that installs a dependency. The operator's call was to decouple
them.

**The shipped rule is the third: a build always has a network, and the isolation switch is not consulted.**
What the switch governs is the agent's container, which is what it was written for. Three things bound a build
instead, and none of them moved: what it can *read* (the staged context, the project's mounts read-only, the
measured grant), where it can *write* (its own scratch directory), and the fact that it publishes no port. Its
reach is the sandbox's reach. Measured 2026-10-30 through the product's own arguments: a `RUN` step fetched a
public page, and the guest could not reach the host's loopback — the gateway is the CNI address, not
`127.0.0.1` — so a build cannot call MIKE's own API. `TestBuildReachesTheNetworkEvenForAnIsolatedProject`
asserts the route exists; the fetch itself is not a test, because a Go test reaches no network.

`--pull=never` holds even with a network, and that is about *who* fetches rather than whether: the host pulled
each named ref, recorded its digest on the built image and handed the bytes in, the container environment
carries no registry credentials by design, and a guest pull would let a spelling MIKE never handed in resolve
to something else. The same reasoning covers the **host-side pull** of a base the host lacks, which is the one
network action repository text makes the host take — on the operator's network, with the operator's credentials,
before any container starts.

The cost is recorded here rather than spelled out in the dialog: the switch's own wording no longer over-claims
(it says it blocks the container's network, not all network), and this record plus the bullet in
`docs/sandbox-runtime.md` carry the rest. A Settings notice naming builds was built and removed the same day —
the operator's call was that a fourth warning on a row that already carries two is noise, and the fact belongs
in the doc that owns the subsystem.

**Costs.** Layer caching becomes per-build: a commit is one layer, so cache on the base digest, the ordered
`RUN` text and the context tree hash. MIKE's parser keeps the reading — the `RUN` text the audit hash is
built from and the image refs the host has to hand in — and loses instruction fidelity to the builder, which
is the part that could not be got right twice. What it does not keep is the refusal pass; see the correction
below. A build writes one host path, its own scratch directory, and
nothing else: a directory a person chose is never writable by a build. A tracked secret stays reachable:
nothing at this layer helps, and the same bytes are reachable from a task turn.

**Reopens this if measured green:** nesting a builder under runsc (`buildah --isolation=chroot`, or kaniko,
on a two-line Dockerfile) would restore per-`RUN` caching and remove MIKE's parser from the correctness
path. **Measured 2026-10-30 on the development host: it is green**, so this record adopts it. The egress
correction below is what the same day's measurement forced on the mechanism.

`make build-probe` re-runs the measurement; it is not part of `make check`, because it pulls a builder
image and takes minutes. The recipe, each half of which was found by failing without it:

- **The grant.** With nothing added, buildah dies at `writing "0 0 4294967295" to /proc/<pid>/gid_map:
  operation not permitted` — it wants a user namespace and `--cap-drop ALL` left it unable to make one.
  **The measured minimum is three: `SETUID`, `SETGID`, `SETFCAP`**, in `sandboxruntime.BuildGrant`, which is
  what the probe measures as its floor. Each of the three
  is necessary — every pair fails at that `uid_map`/`gid_map` write — and a stage counts as passing only
  when the guest's own store lists the tag it was supposed to build, because three instruments that read
  buildah's output instead each reported a pass that had not happened. `SYS_ADMIN` is not required, and
  neither are `SYS_CHROOT`, `MKNOD` nor `AUDIT_WRITE`. What a build is *granted* is now
  `sandboxruntime.BuilderGrant`, the fourteen names the nested builder asks each `RUN` step for: see the
  correction at the end of this record.
- **The grant is non-monotone, and the shape of it is now measured rather than inferred.** `make build-probe`
  runs the ladder and reports every grant it tried. Measured 2026-10-30 on the development host, each stage
  judged by the tag existing in the guest's own store:

  | grant | build |
  |---|---|
  | nothing | fails at `writing "0 0 4294967295" to /proc/<pid>/gid_map` |
  | `SETUID SETGID SETFCAP` | builds, runs a `RUN` step, and survives `chown -R`, `chmod`, `ln -s`, a hard link, `touch -d` and a `tar` |
  | the three ∪ Docker's default set (14) | builds |
  | the same, less `CHOWN` (13) | builds |
  | that set plus `SYS_ADMIN` and `SYS_RESOURCE` (15) | builds |
  | the same fifteen, less `CHOWN` (14) | **fails extracting the base layer at `lchown /etc/shadow: operation not permitted`** |
  | Docker's default set less `KILL` and `NET_BIND_SERVICE`, plus `SYS_ADMIN` and `SYS_PTRACE` | **fails at `setting "RLIMIT_NOFILE" limit to soft=1048576,hard=1048576 (was soft=524288,hard=524288): operation not permitted`** (measured 2026-10-31) |
  | the three plus `SYS_ADMIN` alone | **fails extracting the base layer at `setting up pivot dir: permission denied`** (measured 2026-10-31) |

  So the finding is not "bigger breaks it" and not one poisoned name: it is the **pair** — privileges wide
  enough to move buildah off the user-namespace path, without the `CHOWN` that path's absence then requires.
  podman says so at create time (`Capabilities requested by user or image are not allowed by default:
  "CAP_CHOWN"`) and buildah pays for it at extraction. Two consequences hold. Removing one capability from a
  working set measures nothing, so a candidate grant is tested whole. And the recommended defaults are not a
  hazard on this host — a person who clicks the defaults gets a build that builds — while a hand-assembled
  wide grant can be. What the product shipped here was corrected the next day, and both halves of this
  paragraph's shipping decision are superseded below: the pair is measured by the probe's
  `grant-without-chown` stage, and the refusal sentence no longer points at a project's list.
- **The store.** `vfs`, with its root on the container's **rootfs**: the same build commits fine under
  `/var/tmp` and fails under `/tmp`, where the guest's tmpfs answers a layer's xattr read with "no such
  file or directory". The `overlay` driver inside the guest fails at its first mount, "transport endpoint
  is not connected", and `fuse-overlayfs` would need `/dev/fuse`, which this package has no grant shape
  for.
- **The context.** Written by the guest's own shell: `podman cp` into a running container streams a tar
  the builder image does not ship, and the copy dies with a closed pipe.

What it costs, and what the landing must answer: a build's layers live in one disposable container, so a
build container is single-use and the scratch directory grows while it runs; and the grant above is measured
against a two-rung ladder, so the first real project Dockerfile re-owes it.

**Egress, measured the same day, and it corrects this record's `podman commit`.** `--runtime-flag
overlay2=root:self`, which `GVisorCreateArgs` puts on every container, keeps the root overlay inside the
guest: the container's `UpperDir` measured 0 bytes with a file written into it, running and stopped. So
`podman cp` out finds nothing, `podman mount` refuses rootless, and **`podman commit` and `podman export`
cannot see guest writes at all** — a commit after a `RUN` step would silently yield an image without it.
The one path that crosses the boundary is a bind mount: `-v <scratch>:/mike-out:rprivate,rw` was written
from the guest and read from the host while running. The artifact is therefore pushed out by the builder
(`docker-archive:`) and `podman load`ed host-side, from a directory MIKE made. That load is where
repository-authored bytes become the trusted base of the project's later tasks, so the provenance check —
the instruction-set hash and the person's confirmation — belongs at the load and nowhere earlier.

**The store does not go on tmpfs, which is what this record proposed for the disk bound.** A `vfs` layer
whose root is guest tmpfs fails its own xattr reads (the same failure `/tmp` produced), so the store stays on
the container rootfs under `/var/tmp/mike-build` and the disk bound is taken from the other side: the output
directory is sampled while the build runs and the build is stopped past its cap. `--tmpfs …:size=` is
honoured under runsc; it is simply not usable for a layer store.

**Landed 2026-10-30** with the staging, the read-only inheritance and the archive/load egress.
`backend/sandbox/build_e2e_test.go` is the proof that survives this record: committed bytes reach the image,
a `RUN` step's output survives the `podman load`, and no container or scratch directory survives any exit. The
grant is re-measured by `make build-probe`, which is not part of `make check`.

**The parser's refusal pass was measured away the same day, and this record wrote it as a requirement.** The
first landing whitelisted instructions and refused `RUN` flags, `COPY --from` and path shapes the reader had
not read, on the reasoning that a file MIKE cannot fully read must not be executed. Two things were wrong
with that. It claimed a property the venue already supplies — measured in the nested builder, `COPY /etc/passwd`
and `RUN --mount=type=bind,source=/etc/passwd` resolve *under the context directory*, an escaping `COPY ../x`
is clamped inside it, `--mount=type=secret` finds no secret store, `--network=host` finds no interface, and
`ARG`, `SHELL`, `VOLUME`, `HEALTHCHECK`, `ONBUILD`, `STOPSIGNAL`, `RUN --mount=type=cache` and multi-stage
files all build. And it cost real things: multi-stage Dockerfiles, which are the common shape, could not be
built at all, and every refused line was a project's working file turned into an error message.

What the reading has to keep is what the venue cannot do for itself. The **image refs**, because the guest does not fetch and the host must fetch each one — a ref the reader misses is a build that dies pulling it, so this
reading must be complete rather than conservative, which is the opposite of a whitelist. And the **command
set**, because a person approves it and the sandbox is what runs it: `ONBUILD RUN` joins the set since it runs
in a later build of the same image, `SHELL` joins the hash since it changes every command's meaning, and
`# escape=` is refused since it moves where a command ends. `--platform` stays refused for a third reason: the
host pulls, so the build gets this host's architecture and saying otherwise would be a lie. `TestBuildPassesThroughWhatTheVenueAlreadyJails` and `TestBuildRunsAMultiStageDockerfileOverTwoImages` hold the two halves;
`TestBuildProducesNoImageForACopySourceAboveTheContext` replaces the old test that asserted the refusal.

**And the `FROM` policy this record asked for was declined, with one exception measured first.** The record
ended by asking for an operator allowlist, digest pinning and a freeze switch, on the worry that a repository
gets to choose the base image. The operator's rule is that the sandbox is the boundary, so the question is
whether that choice reaches anything the sandbox does not already grant. Checked by building, not by argument:
a hostile base gains arbitrary code execution *that the product already grants*, and it gains persistence —
its code runs in every later task, including turns nobody reviews — but from inside a task container it reads
only the worktree and the project's configured mounts, and can only send them where the project's own network
setting already allows. No host file is reachable that the jail does not already grant, and the review diff is
produced by host git reading the worktree, so a hostile image cannot lie about it either.

One thing was not sandbox-reachable and was reachable by `FROM`: `ResolveImageRef` resolves a short name
against the host's image store first, so `FROM my-runner:1` in a tracked file copied a local image's bytes into
the project's image — measured, no network needed. That is fixed by not applying the local lookup to
repository text, one line, and held by `TestBuildCannotNameAHostLocalImageByShortName`. An allowlist, per-project
digest pinning and a freeze switch are **not** built: they would be a permission system around a choice the
boundary already contains, which is the mistake this record's first landing made with the instruction
whitelist. Tag drift stays, priced as what it is — a reproducibility annoyance recorded by `mike.base-image`,
not an exfiltration path.

The lesson is the one AGENTS.md states generally: a gate that duplicates a boundary is not a boundary, it is
an inventory of what the boundary already handles — and an inventory that is wrong in the pessimistic
direction is invisible except to the person whose file it rejects.

**The grant this record shipped was a union, and the union is what broke a real build.** The Decision above
granted the project's configured capabilities on top of the measured minimum, which reads as generosity — the
person's own list, plus what a builder needs. Measured 2026-10-31 on a real project Dockerfile, that is exactly
the shape that cannot build. A build's nested builder hands each `RUN` step containers-common's **default set**
of fourteen names; a union is a superset of the minimum, not of that set, and a project's list assembled by
hand can sit *short* of it. Denied the set it wants, buildah takes the path that also raises `RLIMIT_NOFILE`
and `RLIMIT_NPROC` to `1048576`, and a rootless gVisor guest — measured inside the builder image at hard
`nofile=524288`, `nproc=63710` — cannot raise a hard limit without `CAP_SYS_RESOURCE`. Every `RUN` step of the
project's Dockerfile died at that raise. `podman create` reported the two refused names (`CAP_KILL`,
`CAP_NET_BIND_SERVICE`) and MIKE discarded the line, because the create succeeded.

Three things follow, and all three are now the product.

- **The grant is a named constant, not a composition.** `sandboxruntime.BuilderGrant` is the fourteen names the
  nested builder asks for, `BuildGrant`'s three measured inside it, so the floor this record measured is still
  a subset of what ships. A grant with a *hole* in it is the failure mode — not a large grant, and not one
  poisoned name — which is why the answer is a set nobody assembles rather than a rule about how to assemble
  one. The probe's decisive stage measures the shipped set, so a green `make build-probe` is now a statement
  about a real build.
- **A build reads none of the project's Capabilities.** That row is a statement about the container the agent
  runs in, and a build is not the agent; a person's task-container list silently breaking their builds is not
  a meaning anyone intended. A `RUN` step needing a privilege outside the set — a mount namespace, `SYS_ADMIN`
  — is a build this venue declines rather than one the list grows to cover; such a base is built outside and
  named `localhost/…`, which is the spelling this record already requires of a host-local image.
- **This is a widening for the common case, said plainly.** A project that configured nothing built under three
  capabilities and builds under fourteen now. All fourteen are guest-local on a `--cap-drop ALL` base with one
  writable bind (its own scratch output), no published port and no route to the host's loopback, which is the
  argument `GVisorIsolationForCaps` makes about every grant: the runtime is what the safety rests on.

`buildFailureHint` is rewritten to match, and it is the part this record got wrong the longest: it keyed on a
permission keyword and then asserted the person's own list was the suspect, so on the build above it advised
removing the capabilities whose absence was the cause. It now states the fixed grant and points at the
Dockerfile. `TestARunStepNeedingMoreThanTheBuildGrantSaysSo` holds the refusal through the product's own
argument path; the pair it replaces is re-measured by the probe's `grant-without-chown` stage.
