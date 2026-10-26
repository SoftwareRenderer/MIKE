# The application image

Read this before touching `Dockerfile`, `compose.yaml`, `scripts/docker-entrypoint.sh` or
`scripts/mike-healthcheck.sh`.

## What this owns

How this tree becomes an image, and how that image becomes a running server: the flags and volumes a
container needs, the two required values, the three build stages, and what to expect on first boot. It
stops at the container's edge. What runs inside a task is the gVisor container
(`docs/sandbox-runtime.md`), what a variable means is `docs/configuration.md`, and how the process
stops is `docs/lifecycle.md`.

## What must stay true

- **Build images from source, never from existing artifacts.** `.dockerignore` keeps `frontend/dist`, `dist/` packages, the committed bridge, and `node_modules` out of the context. Stage 2 copies `frontend/dist` from Stage 1 so that `//go:embed` can bundle it into the binary. `prompts/` are included in full, including tracked skill packages, so deployed tasks have the same skills as source runs.
- **The server binary is a release build and is CGO-free.** A `-tags release` build serves its own embedded frontend and refuses `--frontend-dist`. `CGO_ENABLED=0` ensures the bridge is statically linked, allowing it to run on guests with different C libraries.
- **gVisor is installed as a release bundle with its sidecar tree.** The bundle's sha512 is verified before execution. `runsc` uses binaries in `gvisor-bin/` and expects that directory to be adjacent; the extraction must be complete. Installing a single binary from the bundle is insufficient.
- **Each stage defines its own `ARG` defaults.** Whether a global `ARG` default reaches a stage that re-declares the name depends on the builder; we use podman/buildah 5.8. A `:=` default in the stage costs nothing, and `--build-arg` still takes precedence.
- **Stages verify tool availability without root privileges.** `podman --runtime-flag ignore-cgroups=true --version`, `runsc --version`, the `runsc` PATH name, and the sidecar tree are all checked at build. `podman info` is not checked because it requires privileges available only at runtime; the entrypoint probes it instead.
- **`privileged` and `seccomp=unconfined` enable the nested runtime.** `compose.yaml` binds to `127.0.0.1` for this reason; TLS is mandatory, and the Host allowlist defaults to `localhost` to prevent unauthorized access. Task work runs with `--cap-drop ALL` inside gVisor.
- **Storage is kept separate from the engine's overlayfs.** Podman refuses `overlay` on `overlayfs`, so `compose.yaml` provides a volume for `/var/lib/containers`. The entrypoint falls back to `fuse-overlayfs` or `vfs` if needed.
- **`MIKE_ENCRYPTION_KEY` is required and has no default.** `compose.yaml` fails if it is missing, and the entrypoint exits with an error naming the variable. The image does not include an agent CLI; adapters requiring a host binary must use a derived image.
- **`safe.directory` is granted at runtime, by the image, and never by the binary.** A mounted checkout keeps its host uid while this server runs as root, so git would refuse every project the person adds. The build cannot make the grant: `git config --global` writes `$HOME/.gitconfig`, which the documented mount covers with the checkouts. `GIT_CONFIG_GLOBAL` is the other wrong file — it replaces the one `resolveGitIdentity()` reads the author from.
- **The container does not update itself.** Installs that are not unpacked release directories are refused. A container is refused before this check: `classifyInstall` checks the runtime's marker file before the layout. Even if a container looks like a release, it will refuse to update because a restart preserves the state, but a new `compose up` replaces the container. See `docs/auto-update.md`.

## How it is held

Stage `web` runs `npm ci` against the lockfile, then `npm run build`. Stage `go` builds `mike` and
`sandbox-bridge` from `pkg/bridge/agent`, so a sandbox can never run a bridge older than this tree.
Stage `runtime` installs Debian stable's podman (5.x, which this app's global `--runtime-flag` call
needs), crun, netavark and aardvark-dns, then the gVisor bundle.

The layout is what the binary's sibling lookups require: `WORKDIR /mike`, the bridge beside the
executable (`GetAgentBinaryPath`), `prompts/` beside it (`resolvePromptDir`), and `extensions/`
relative to the working directory. `/mike/data` is `0700`: `kanban.db`, the local CA and its key.

`scripts/docker-entrypoint.sh` is the part that cannot be a build-time check. It refuses a missing
`MIKE_ENCRYPTION_KEY`, defaults `MIKE_LISTEN` to `0.0.0.0` and `MIKE_ALLOWED_HOSTS` to `localhost`,
probes `podman info` for the graph driver, writes `/etc/containers/storage.conf` when the kernel
refuses `overlay`, then `exec`s the server so SIGTERM reaches the bounded shutdown path. The
`MIKE_LISTEN` default is why no example below sets it: the server's own default is loopback, and a
published port cannot reach it.
`MIKE_PREPULL_IMAGE=1` pulls the default sandbox image in the background instead of during the first
task.

The entrypoint also grants git every repository uid by appending `[safe] directory = *` to
`/etc/gitconfig`, the image's own file, so no mount can cover it and no rebuilt container loses it.
The grant belongs to the image because the only other uid inside this container is the operator's own;
the server binary refuses to carry it, since a host install runs git over several people's checkouts.
`docs/git-operations.md → Whose repository git will open`.

`scripts/mike-healthcheck.sh` answers three questions separately — runsc, the podman store, and two
HTTPS requests where one cannot say both: `/api/health` proves the listener, an unauthenticated
`/api/config` proves the gate.

## Start it

Everything below assumes the image exists — built here or pulled, it makes no difference. The name
used is the one `compose.yaml` gives a local build. One value has no default, `MIKE_ENCRYPTION_KEY`,
and the entrypoint exits naming it. `MIKE_ALLOWED_HOSTS` defaults to `localhost`, which is all the
loopback every example here publishes needs; the name a browser types instead — a proxy's domain,
`hostname.local`, a LAN IP — has to be listed.

### From this checkout

```bash
cp .env.example .env      # fill in the one required value, then:
docker compose up -d
docker compose logs mike  # a data directory with no passkey prints a boxed pairing code here
```

`up` builds only when the image named under `image:` is absent; `--build` forces a rebuild. Set
`MIKE_IMAGE_TAG` to the tag of an image you already built and it is used as-is. Open
`https://localhost:3000` and register a passkey with the code from the log.

### Your own compose file

The same service without the build stage. The flags belong to the nested runtime and the volumes to
the server's state; each line is load-bearing.

```yaml
services:
  mike:
    image: getmike/mike:local
    restart: unless-stopped
    privileged: true                       # nested podman + runsc need netlink, cgroup, devices
    security_opt: [seccomp=unconfined]     # docker's default profile blocks runsc's syscalls
    shm_size: 256m                         # sandbox containers take /dev/shm from here
    tmpfs: [/tmp]
    stop_grace_period: 40s                 # the server's own budget is 15s, then container teardown
    ports: ["127.0.0.1:3000:3000"]
    environment:
      MIKE_ENCRYPTION_KEY: ${MIKE_ENCRYPTION_KEY:?openssl rand -hex 32}
      MIKE_DATA_DIR: /mike/data
      MIKE_WORKTREES_DIR: /mike/worktrees   # unset, worktrees land in /mike/.worktrees instead
    volumes:
      - mike-data:/mike/data                # kanban.db, the local CA and its key
      - mike-worktrees:/mike/worktrees
      - mike-extensions:/mike/extensions    # installed extension code, deleted with the extension
      - mike-containers:/var/lib/containers # podman's store, off the engine's own overlayfs
      - /srv/projects:/root        # the checkouts agents edit and commit into

volumes:
  mike-data:
  mike-worktrees:
  mike-extensions:
  mike-containers:
```

`docs/configuration.md` holds the optional variables. Nothing above sets `MIKE_ALLOWED_HOSTS`: the
entrypoint's `localhost` default is what a browser reaching the published loopback port presents, and
a name beyond that belongs in the list. `MIKE_PREPULL_IMAGE=1` pulls the default sandbox
image at boot instead of during the first task.

A certificate pair the operator owns arrives as `MIKE_TLS_CERT` and `MIKE_TLS_KEY`, beside a mount of
the files — a path inside the image is not a path on the host. The leaf under `/mike/data/tls` then
goes unused. **`docs/tls.md`**.

### docker run

```bash
docker run -d --name mike \
  --privileged --security-opt seccomp=unconfined \
  --shm-size 256m --tmpfs /tmp --stop-timeout 40 \
  -p 127.0.0.1:3000:3000 \
  -e MIKE_ENCRYPTION_KEY \
  -e MIKE_DATA_DIR=/mike/data -e MIKE_WORKTREES_DIR=/mike/worktrees \
  -v mike-data:/mike/data \
  -v mike-worktrees:/mike/worktrees \
  -v mike-extensions:/mike/extensions \
  -v mike-containers:/var/lib/containers \
  -v /srv/projects:/root \
  getmike/mike:local

docker logs mike        # the pairing code, on a data directory with no passkey yet
```

`-e MIKE_ENCRYPTION_KEY` with no value takes it from the shell, so the secret stays out of the command
history and out of `docker inspect`. Losing every passkey is a one-shot command, not a GUI action:
stop the service, then run the same image with `--reset-passkeys` as its only argument, against the
same data volume and with the key in its environment. The entrypoint checks for it before
it hands anything to the server, and the server refuses a missing `MIKE_ENCRYPTION_KEY` on its own
too.

The browser warns on the first visit either way. The certificate is signed by the server's own CA in
the data volume, so import `data/tls/mike-ca.crt` from that volume or accept the warning; reaching the
server is the listen address plus the Host allowlist, never the certificate.

## Rebuild it

```bash
docker build -t getmike/mike:local -f Dockerfile .
```

| Build arg | Meaning |
|---|---|
| `GVISOR_RELEASE` | `latest`, or a release spelled as its bare date: `20260928` |
| `VERSION` | stamped into the binary and answered by `/api/version` |
| `GO_IMAGE`, `NODE_IMAGE`, `RUNTIME_IMAGE` | the three base images |

Pin the release when the runtime a rebuild downloads must be the one you tested; leave it `latest`
when a rebuild should pick up the runtime the way a fresh install does. Reproducing a whole rebuild
also needs `GO_IMAGE`, `NODE_IMAGE` and `RUNTIME_IMAGE` pinned by digest, because each tag floats.
Either way the bundle's published sha512 is verified before anything is executed.

## Limits an operator meets

- **`/mike/projects` is a mount the server cannot see.** The no-project file jail is `$HOME` plus the
  directories declared at startup, and nothing declares that path: adding a project from it answers
  `path is outside the allowed host directories` with HTTP 403. It is a bind mount plus a startup
  declaration, and only the first half exists today.
- **`MIKE_ALLOW_ROOT_FS` is not forwarded.** `compose.yaml` leaves it alone on purpose, so `.env`
  cannot widen the jail; the refusal names the variable because the widening is a boot decision.
- **A hostname nobody listed is HTTP 421.** `localhost`, `127.0.0.1`, `::1` and the listen host are
  accepted, and the entrypoint's `MIKE_ALLOWED_HOSTS` default adds nothing to them; a proxy's domain,
  `hostname.local` or a LAN IP goes into that list first.
- **A project must live under the bind mount** for its sandboxes to reach its `.git` at the same path.
- **An agent adapter that shells out to a CLI fails until that CLI is installed** in a derived image;
  the base image ships no node or python.

## Checking this

No gate builds this image: the `Makefile` has no docker target and `make check` never reads
`Dockerfile`. So two commands, and be clear about what each one catches.

```bash
make docs-check          # a path or pointer in these lines that has stopped resolving
docker build -t mike-image-check -f Dockerfile .
```

The build is the only thing that catches a wrong gVisor object, a missing package in the apt list or
a stage that lost its `ARG` default — it fails on the step, with the step printed. After a run, the
image's own `/usr/local/bin/mike-healthcheck` is the claim that the runtime is intact: the image
installs `scripts/mike-healthcheck.sh` under that name, and it is what `HEALTHCHECK` runs.
