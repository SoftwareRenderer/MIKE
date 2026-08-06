# UI Extensions

UI extensions add custom functionality to [MIKE](https://getmike.dev). Extensions run in a sandboxed iframe and interacts with the app through capabilities declared in `manifest.json`, executed via the `extHost` client API (`host-client.js`).

A working example is [`github-issue-importer`](github-issue-importer/), which imports a GitHub issue when a URL is pasted into a task description.

## Contents

- [Quick Start](#quick-start)
- [Architecture](#architecture)
- [Structure](#structure)
- [manifest.json](#manifestjson)
- [MyExtension.js](#myextensionjs)
- [Capabilities](#capabilities)
- [extHost client API](#exthost-client-api)
- [Injection Points (Scopes)](#injection-points-scopes)
- [Network access](#network-access-networkproxy)
- [Host-rendered status](#host-rendered-status-uistatus)

## Quick Start

The minimal extension is an HTML entry page plus a script that calls a capability.

```html
<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><title>My Extension</title></head>
<body>
    <script src="/host-client.js"></script>
    <script src="MyExtension.js"></script>
</body>
</html>
```

```js
extHost.ready().then(function () {
    extHost.createTask.description.insertText('Hello from an extension');
});
```

The extension can only call capabilities it declared in `manifest.json`. For example, `createTask.description.insertText` must be listed under `capabilities.ui.permissions`. Calls for undeclared capabilities are rejected. See [manifest.json](#manifestjson).

## Architecture

```
App (UIInjector) ── init message (granted caps, config, context) ──► iframe (extHost)
App (handlers)    ◄──── capability call / event ───────────────────── iframe (extHost)
```

- The host creates a sandboxed iframe for each extension at its injection point and starts a message channel.
- The iframe calls capabilities through `window.extHost`; the host resolves them in `frontend/src/lib/extensions/handlers/`.
- Only capabilities declared in the manifest are callable.

## Structure

Extensions are served statically at `/extensions/ui/<name>/`:

```text
extensions/ui/<name>/
├── index.html          # sandboxed entry page
├── MyExtension.js      # your logic (plain JS, uses extHost)
└── manifest.json
```

The iframe is hidden by default (`height: 0`); call `extHost.setSize()` to show custom visualizations.

## manifest.json

Declares metadata, integration points, and requested capabilities.

```json
{
    "display_name": "My Extension",
    "description": "Adds a custom feature to the UI",
    "author": "Author Name",
    "homepage": "https://Optional_Author_Homepage",
    "version": "1.0.0",
    "priority": 0,
    "capabilities": {
        "ui": {
            "injection_points": ["createTask"],
            "permissions": [
                "createTask.events.paste",
                "network.proxy"
            ]
        },
        "proxy": {
            "allowed_domains": ["api.example.com"]
        }
    },
    "config_schema": {
        "properties": [
            { "key": "api_key", "type": "string", "title": "API Key" }
        ]
    }
}
```

- `priority` — loading order; lower values load first (default `0`).
- `capabilities.ui.injection_points` — [scopes](#injection-points-scopes) where the extension activates.
- `capabilities.ui.permissions` — requested capabilities.
- `capabilities.proxy` — domain allow-list and header templates for `network.proxy`.
- `config_schema.properties` — array of `{ key, type, title, description?, sensitive? }` fields (array order is preserved). `sensitive` values are encrypted at rest and usable in header templates.

## MyExtension.js

Plain JavaScript that uses `extHost`. Wait for `ready()`, subscribe to events, call capabilities.

```js
(function () {
    'use strict';

    async function handlePaste(data) {
        const text = (data && data.text) || '';
        if (!/example\.com/.test(text)) return;

        extHost.loading('Fetching…');
        try {
            const result = await extHost.proxy('https://api.example.com/thing', 'GET', '');
            await extHost.createTask.description.insertText('# ' + result.title);
            extHost.hide();
        } catch (err) {
            extHost.error('Failed: ' + err.message);
        }
    }

    extHost.ready().then(function () {
        extHost.on('createTask.events.paste', handlePaste);
    });
})();
```

**Paste ownership.** The host calls `preventDefault()` when forwarding a `createTask.events.paste` event, so the browser never inserts the raw text. The extension must re-insert the original text for any paste it does **not** handle — otherwise the paste is dropped.

### Persisting state (`storage.*`)

`storage.*` provides a namespaced key-value store, scoped to the extension *and* the current project/task (the same key on different tasks never collides). Values may be any JSON; up to 64KiB per value.

```js
extHost.ready().then(async function () {
    const count = (await extHost.storage.get('clicks')) || 0;
    extHost.setSize(24); // reveal a small counter
    document.body.textContent = 'Clicks: ' + count;

    document.addEventListener('click', async function () {
        const next = ((await extHost.storage.get('clicks')) || 0) + 1;
        await extHost.storage.set('clicks', next);
        document.body.textContent = 'Clicks: ' + next;
    });
});
```

## Capabilities

Capabilities are namespaced (`area.operation`) and come in two kinds:

- **`call`** — request/response; the extension invokes it (e.g. `createTask.description.insertText`).
- **`event`** — subscription; the host forwards app events to the extension (e.g. `createTask.events.paste`).

| Capability | Kind | Description |
|---|---|---|
| `createTask.description.insertText` | call | Insert plain text into the task description at the cursor. |
| `createTask.events.paste` | event | Receive paste events (clipboard text) from the description. |
| `network.proxy` | call | HTTP request through the server's domain-allowlisted proxy. |
| `config.get` | call | Read the extension's own config. *(implicit — no declaration needed)* |
| `log.write` | call | Emit a namespaced, prefixed console log. *(implicit — no declaration needed)* |
| `ui.status` | call | Set a host-rendered status indicator (see below). |
| `ui.setSize` | call | Set the iframe height in pixels (for custom visualizations). |
| `storage.get` / `storage.set` / `storage.delete` / `storage.list` | call | Scoped key-value storage. |
| `editor.addFooterButton` | call | Add/update a button in the editor footer (next to "Wrap"). `label`/`active` optional. Also grants `editor.removeFooterButton` and `editor.footerButtonClick` implicitly. |
| `editor.removeFooterButton` | call | Remove a previously added footer button. *(implicit with `editor.addFooterButton` — no declaration needed)* |
| `editor.getContent` | call | Resolve with the current editor document text. |
| `editor.setOverlay` | call | Hide/show the code editor and fill the editor area with the extension iframe (`{ visible }`). |
| `editor.contentChange` | event | Fired on every editor edit with the new document text (`{ content }`). |

References:
- Available `injection_points` — `./frontend/src/lib/extensions/types.ts`
- Available `permissions` (handlers) — `./frontend/src/lib/extensions/handlers/`

## extHost client API

`window.extHost` is exposed by `host-client.js`. It resolves once the host `init` message arrives.

| Method | Capability | Description |
|---|---|---|
| `ready()` | — | Resolves once the host `init` message is received. |
| `.granted` | — | Array of granted capability names. |
| `.config` / `.context` | — | Extension config / host context (after `ready()`). |
| `getConfig()` | `config.get` | Resolve with the extension's own config. |
| `proxy(url, method, body)` | `network.proxy` | Proxy an HTTP request via the server (domain-allowlisted). |
| `createTask.description.insertText(text)` | `createTask.description.insertText` | Insert text into the task description. |
| `on(cap, handler)` | event caps | Subscribe to an event; returns an unsubscribe function. |
| `log(level, message)` | `log.write` | Log with a prefix. |
| `loading(message)` / `error(message)` / `hide()` | `ui.status` | Host-rendered status (see below). |
| `setSize(heightPx)` | `ui.setSize` | Size the extension iframe. |
| `storage.get(key)` | `storage.get` | Read a value (resolves `null` if absent). |
| `storage.set(key, value)` | `storage.set` | Write any JSON value. |
| `storage.delete(key)` | `storage.delete` | Delete a key (no-op if absent). |
| `storage.list(prefix?)` | `storage.list` | List stored key/value pairs, optionally by prefix. |

Any call for a capability that was **not granted** is rejected by the host — the extension cannot do more than its `permissions` allow.

## Injection Points (Scopes)

When a component mounts, the `UIInjector` creates a sandboxed iframe for each extension registered at that scope.

| Scope | Component | Purpose |
|---|---|---|
| `taskCard` | `Card.svelte` | Enhance individual task cards (badges, actions) |
| `fileEditor` | `FileEditor.svelte` | Enhance the code editor (toolbars, status bars) |
| `projectBoard` | `Board.svelte` | Enhance the project board view |
| `taskChat` | `TaskChat.svelte` | Enhance the chat interface (input actions) |
| `projectSidebar` | `Sidebar.svelte` | Enhance the project sidebar |
| `fileBrowser` | `FileBrowser.svelte` | Enhance the directory selector modal |
| `createTask` | `CreateTaskModal.svelte` | Enhance the task creation flow (external issue import) |

## Network access (`network.proxy`)

The extension cannot fetch external services directly (it is cross-origin and the app session is out of reach). Instead it calls `extHost.proxy(url, method, body)`, routed through the server. The server enforces the manifest's `allowed_domains` allow-list and resolves header templates (e.g. `Authorization: token {{github_token}}`) from the extension's config — keeping secrets server-side.

Requests to domains outside the allow-list are rejected server-side.

## Host-rendered status (`ui.status`)

Instead of each extension building its own status UI in its (invisible) iframe, use the host-rendered indicators. They render in the parent document at the injection point, matching the app theme:

```js
extHost.loading('Importing…');     // spinner + message
extHost.error('Something failed'); // red error text
extHost.hide();                    // clear
```

Extensions that render their own custom content inside the iframe should call `extHost.setSize(height)` to reveal it.
