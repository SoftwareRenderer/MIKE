# MIKE: Integrated Kanban Environment

<p align="center"> <a href="https://getmike.dev"> <img alt="screenshot mockup of MIKE, showing features as kanban cards" src="https://getmike.dev/assets/mike_screenshot.jpg" width="800"> </a> </p>

[MIKE](https://getmike.dev) is a LLM harness that streamlines software development work. It integrates strong sandbox isolation (via [gVisor](https://gvisor.dev/)) and securely supports custom extensions (via [iframe sandbox](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Elements/iframe#sandbox)/[wazero](https://wazero.io/)). It also features a Progressive Web App optimized to improve usability on spotty networks. Works with [llama.cpp](https://llama.app/) and inference providers that support the OpenAI Chat Completions API.

> [!WARNING] The API and Extension surfaces are still being solidified. No stable API/Extension interfaces will be guaranteeed until version 1.0.

## Quick Start

TBD.

## Manual Setup

Prerequisites:

- [Podman](https://podman.io/docs/installation)
- [gVisor](https://gvisor.dev/docs/user_guide/install/)
- [git](https://git-scm.com/install/)

TBD.

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

3. Navigate to https://localhost:3000

4. You will be prompted to pair a Passkey using a code provided after starting the application.
  - Firefox on Linux: As of 2026-10-10, there is limited support for Passkeys.
