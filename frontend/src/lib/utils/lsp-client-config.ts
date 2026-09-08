/**
 * The one `LSPClient` configuration, used by both the initial connect and the reconnect: an option the
 * reconnect drops is a feature that disappears the first time the socket does. It lives outside
 * `FileEditor.svelte` so a test can exercise the wiring the app actually uses.
 */
import type { LSPClientConfig } from '@codemirror/lsp-client';
import { sanitizeHTML } from '$lib/utils/sanitize';
import { getGrammarForFence } from './editor-language';
import { createLSPNotificationHandlers } from './lsp-connection-manager';

/** Gopls can take 10-20s to initialize on large workspaces. */
const LSP_INIT_TIMEOUT_MS = 30000;

interface LspClientConfigOptions {
    /** The LSP features this editor uses, chosen per editor rather than taken from the bundle. */
    extensions: LSPClientConfig['extensions'];
    /** The workspace root the server was started for, as a `file://` URI. */
    rootUri: string;
    /** The file on screen, read when asked: the client belongs to a workspace and outlives the file it
     *  connected for, so a captured path would answer forever with that first file's grammar. */
    getFile: () => string;
    /** Server log/telemetry messages, for the footer's LSP tooltip. */
    onMessage: (message: string) => void;
}

export function lspClientConfig({ extensions, rootUri, getFile, onMessage }: LspClientConfigOptions): LSPClientConfig {
    return {
        extensions,
        rootUri,
        notificationHandlers: createLSPNotificationHandlers(onMessage),
        timeout: LSP_INIT_TIMEOUT_MS,
        // A code fence in a language server's markdown (the signature a hover card opens with) needs this
        // hook: our grammars are all named `mike-declared`, so the renderer's "fence tag equals the
        // editor's language name" rule matches nothing and every signature would be monochrome.
        highlightLanguage: (tag: string) => getGrammarForFence(tag, getFile()),
        // The package writes a server's rendered documentation into `innerHTML` in two places this
        // repository cannot see with html-sinks.py — the completion info panel (`renderDocInfo`) and
        // the signature-help docs element — and this hook is the only control it offers: without it
        // the markup a server returned is the markup the page gets. Documentation is the least
        // trustworthy text in the editor: gopls lifts it out of a comment in whatever repository is
        // open, so it is the same content class the chat sanitizer exists for. Core's own hover card
        // sanitizes separately (`hoverDocsHTML` in lsp-hover-actions), which is exactly why this one
        // was easy to miss — the visible hover path was always clean.
        sanitizeHTML: (html: string) => sanitizeHTML(html),
    };
}
