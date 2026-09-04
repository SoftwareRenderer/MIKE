/**
 * The one `LSPClient` configuration, used by both the initial connect and the reconnect: an option the
 * reconnect drops is a feature that disappears the first time the socket does. It lives outside
 * `FileEditor.svelte` so a test can exercise the wiring the app actually uses.
 */
import type { LSPClientConfig } from '@codemirror/lsp-client';
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
    };
}
