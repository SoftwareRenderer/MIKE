import type { Extension } from '$lib/types';
import { listExtensions } from '$lib/api';
import { uiInjector } from './ui-injector.svelte';
import { loadI18NFromExtensions } from '$lib/i18n/load-pack';

/**
 * Reactive holder of the installed-extension list.
 *
 * Settings → Extensions reads the server directly (it owns the install/update/config flows its own
 * rows drive); this singleton is the app-wide copy every other reader consults, refreshed by
 * `App.svelte` on start and on each `extension_change` broadcast.
 */
class ExtensionManager {
    state = $state({
        extensions: [] as Extension[],
    });

    /** Load all installed extensions from the server. */
    async loadExtensions() {
        try {
            this.state.extensions = await listExtensions();
            uiInjector.registerFromExtensions(this.state.extensions);
            // The active language is whichever i18n pack is enabled, derived from
            // this same list — so this one call is the live switch in both
            // directions, driven by the `extension_change` broadcast that already
            // lands here. It is app-global like the css pack's stylesheet: no
            // iframe, no channel, no permission. A catalog that will not load is
            // reported under its own prefix and must never surface as an
            // extension-list error (which would blame the wrong thing).
            try {
                await loadI18NFromExtensions(this.state.extensions);
            } catch (e) {
                console.error('[i18n] could not reconcile the active language:', e);
            }
        } catch (e) {
            console.error('[extensions] could not load the installed extensions:', e);
        }
    }

    /**
     * Enabled LSP extensions, whatever their editor gate says.
     *
     * Their manifests are what tells the editor what a file is: the file types it owns, the wire id
     * each one is opened as, and the syntax profile that colors it. Reading that is a data read — it
     * starts no server and opens no channel — so it must not wait for `editor_enabled`, which the
     * Settings row sells as "diagnostics, completions, go-to-definition, and hover". Answering "no
     * grammar" for that switch is how turning off LSP features quietly turned off highlighting too.
     */
    getEnabledLSPExtensions(): Extension[] {
        return this.state.extensions.filter(ext => ext.type === 'lsp' && ext.enabled);
    }
}

// Singleton instance
export const extensionManager = new ExtensionManager();
