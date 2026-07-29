# UI Extensions

UI extensions enable custom functionality in [MIKE](https://getmike.dev). UI extensions are plain JavaScript modules that run directly in the browser and interact with the DOM.

## Overview

The system uses a "slot" based injection model where extensions can register themselves to specific parts of the application.

## Structure

A UI extension is organized as a directory within `extensions/ui/`:

```text
extensions/ui/<name>/
├── manifest.json
└── MyExtension.js
```

### manifest.json

The manifest provides metadata and defines where the extension should be injected into the UI.

```json
{
    "display_name": "My Extension",
    "description": "Adds a custom feature to the UI",
    "author": "Author Name",
    "version": "1.0.0",
    "priority": 0,
    "ui": {
        "module_url": "/extensions/ui/<name>/MyExtension.js",
        "injection_points": ["create-task"]
    },
    "config_schema": {
        "type": "object",
        "properties": {
            "api_key": { "type": "string", "title": "API Key" }
        }
    }
}
```

- `priority`: Controls the loading order. Lower values load first (default `0`).
- `ui.module_url`: The path to the JavaScript module served by the server.
- `ui.injection_points`: An array of scopes where the extension should be activated.

### MyExtension.js

UI extensions must export a default `init` function. This function is called when the corresponding UI component mounts.

```js
export default function init({ component, config, extensionId }) {
    // 1. Setup: Create elements, add event listeners, etc.
    const div = document.createElement('div');
    div.textContent = `Hello from ${config.someSetting}!`;
    document.body.appendChild(div);

    // 2. Cleanup: Return a function to tear down the extension
    return () => {
        div.remove();
    };
}
```

## Injection Points (Scopes)

Extensions can be injected into several named slots across the application. When a component mounts, the `UIInjector` activates all extensions registered for that scope.

| Scope | Component | Purpose |
|---|---|---|
| `task-card` | `Card.svelte` | Enhance individual task cards (e.g. custom badges, actions) |
| `file-editor` | `FileEditor.svelte` | Enhance the code editor (e.g. toolbars, status bars) |
| `project-board` | `Board.svelte` | Enhance the project board view |
| `task-chat` | `TaskChat.svelte` | Enhance the chat interface (e.g. input actions) |
| `project-sidebar` | `Sidebar.svelte` | Enhance the project sidebar |
| `file-browser` | `FileBrowser` | Enhance the project directory selector modal |
| `create-task` | `CreateTaskModal.svelte` | Enhance the task creation flow (e.g. external issue import) |

## API Calls

To make external API requests securely, UI extensions should use the extension proxy endpoint instead of calling external APIs directly from the browser. This avoids CORS issues and allows the server to handle sensitive API keys from the extension's configuration without exposing them to the client.

### 1. Declare Proxy Capability

Your extension must declare the `proxy` capability in its `manifest.json`. This includes an allow-list of domains and optional header templates.

```json
{
    "display_name": "My Extension",
    "capabilities": {
        "proxy": {
            "allowed_domains": ["api.example.com", "github.com"],
            "headers": {
                "Authorization": "Bearer {{api_key}}",
                "X-Custom-Header": "my-app-v1"
            }
        }
    },
    "config_schema": {
        "type": "object",
        "properties": {
            "api_key": { "type": "string", "title": "API Key", "sensitive": true }
        }
    }
}
```

- `allowed_domains`: Only requests to these domains will be proxied.
- `headers`: Templates that are resolved using the extension's configuration. Placeholders like `{{api_key}}` are replaced with the corresponding value from the user's settings.

### 2. Making a Request

The proxy endpoint is located at `/api/extensions/{id}/proxy` and requests must be structured as follows:

**Request Body:**
```json
{
    "url": "https://api.example.com/data",
    "method": "GET",
    "body": "{\"key\": \"value\"}" 
}
```
- `url` (required): The full target URL.
- `method` (optional): HTTP method (defaults to `GET`).
- `body` (optional): The request body as a string.

**Example Usage:**
```js
async function fetchData(extensionId) {
    const csrfToken = window.__csrfToken;

    const response = await fetch(`/api/extensions/${extensionId}/proxy`, {
        method: 'POST',
        headers: { 
            'Content-Type': 'application/json',
            'X-CSRF-Token': csrfToken
        },
        body: JSON.stringify({
            url: 'https://api.example.com/v1/user',
            method: 'GET'
        })
    });
    
    if (!response.ok) throw new Error(`Proxy error: ${response.status}`);
    return await response.json();
}
```
