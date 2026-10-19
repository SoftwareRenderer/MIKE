# MIKE: Integrated Kanban Environment

<p align="center"> <a href="https://getmike.dev"> <img alt="screenshot mockup of MIKE, showing features as kanban cards" src="https://getmike.dev/assets/mike_screenshot.jpg" width="800"> </a> </p>

[MIKE](https://getmike.dev) is a LLM harness that streamlines software development work. It integrates strong sandbox isolation (via [gVisor](https://gvisor.dev/)) and securely supports custom extensions (via [iframe sandbox](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Elements/iframe#sandbox)/[wazero](https://wazero.io/)). It also features a Progressive Web App optimized to improve usability on spotty networks. Works with [llama.cpp](https://llama.app/) and inference providers that support the OpenAI Chat Completions API.

> [!NOTE]
> The API and Extension surfaces are still being solidified. No stable API/Extension interfaces will be guaranteeed until version 1.0.

## Quick Start

Prerequisite:
- docker or podman must be installed

1. Generate a secure key

```bash
openssl rand -hex 32
```

2. Create a `.env` file, containing the secure key:
```
MIKE_ENCRYPTION_KEY=secure_key
```

3. Use docker / podman:

```bash
podman run -p 3000:3000 --env-file .env --privileged docker.io/softwarerenderer/mike:latest
```
Note that privileged access is only needed in this containerized Quick Start in order to support nested containers.

4. Navigate to https://localhost:3000

5. You will be prompted to pair a Passkey using a code provided after starting the application.
  - Firefox on Linux: As of 2026-10-10, there is limited support for Passkeys.


## Manual Setup

Prerequisites:
- [Podman](https://podman.io/docs/installation)
- [gVisor](https://gvisor.dev/docs/user_guide/install/)
- [git](https://git-scm.com/install/)

1. Download the latest release
2. Set the encryption key and run:
```bash
MIKE_ENCRYPTION_KEY=testkey ./mike-server -port 3000
```
3. Continue from step 4 in the [Quick Start](#quick-start) section.

## Development

Prerequisites, in addition to Manual Setup:

- [Go](https://go.dev/doc/install)
- [npm](https://docs.npmjs.com/downloading-and-installing-node-js-and-npm)

1. Build frontend
```bash
cd frontend && npm run build
```

2. Run backend

Navigate back to the main directory, then run
```bash
MIKE_ENCRYPTION_KEY=testkey go run .
```

If hosting beyond localhost:
This example listens on all network interfaces.
```bash
MIKE_ENCRYPTION_KEY=testkey go run . -listen 0.0.0.0 -allowed-hosts your.hostname.com
```

3. Continue from step 4 in the [Quick Start](#quick-start) section.

# Backend Highlights

In additon to common features such as automatic context compaction, Skills, AGENTS.md, MCP, and background tasks, MIKE also includes a few "invisible" improvements that help maintain smooth operation:

- RAM Management
  - Parallel tasks means that more builds and tests are running. To avoid out-of-memory errors, MIKE monitors usage and automatically pauses/resumes tasks.
- RESTful Web API
  - This enables programmatic control over the board state, and has built in security controls such as enforced encryption, Bearer tokens, rate limits, and authorized hosts.
- Durable Progress
  - Potential issues related to the inference provider or model such as failed tool calls, loops, crashes, or rate limits are recoverable and don't result in lost work.
- Enterprise Readiness
  - Administrators have control over deployed configuration, and can prevent changes such as installation of extensions and inference providers.