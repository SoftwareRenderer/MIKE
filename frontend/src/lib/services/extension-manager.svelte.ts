import type { Extension, RegistryEntry, ExtensionUpdate } from '$lib/types';
import { listExtensions, getRegistryIndex, installExtension, deleteExtension, setExtensionEnabled, checkUpdates, updateExtension } from '$lib/api';
import { uiInjector } from './ui-injector.svelte';
import { t } from '$lib/i18n';
import { loadI18NFromExtensions } from '$lib/i18n/load-pack';

/** Runtime status of an extension sandbox. */
type ExtensionStatus = 'running' | 'stopped' | 'error' | 'unknown';

/** ExtensionManager is a reactive singleton that manages extension state. */
class ExtensionManager {
    state = $state({
        extensions: [] as Extension[],
        registry: [] as RegistryEntry[],
        updates: [] as ExtensionUpdate[],
        loading: false,
        loadingRegistry: false,
        loadingUpdates: false,
        error: '',
    });

    /** Load all installed extensions from the server. */
    async loadExtensions() {
        this.state.loading = true;
        this.state.error = '';
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
        } catch (e: any) {
            this.state.error = e.message || t('api.loadExtensions');
        } finally {
            this.state.loading = false;
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

    /** Enabled LSP extensions whose editor LSP features are on — the ones a channel may open for. */
    getEditorLSPExtensions(): Extension[] {
        return this.getEnabledLSPExtensions().filter(
            ext => ext.manifest.capabilities?.lsp?.editor_enabled !== false
        );
    }

    /** Get extension runtime status. */
    getExtensionStatus(extId: string): ExtensionStatus {
        const ext = this.state.extensions.find(e => e.id === extId);
        if (!ext) return 'unknown';
        if (ext.running) return 'running';
        if (!ext.enabled) return 'stopped';
        return 'stopped';
    }

    /** Load registry index. */
    async loadRegistry() {
        this.state.loadingRegistry = true;
        this.state.error = '';
        try {
            this.state.registry = await getRegistryIndex();
        } catch (e: any) {
            this.state.error = e.message || t('api.loadRegistry');
        } finally {
            this.state.loadingRegistry = false;
        }
    }

    /** Install an extension from the registry (consumes the SSE stream). */
    async install(name: string) {
        this.state.error = '';
        try {
            for await (const ev of installExtension(name)) {
                if (ev.done && ev.error) {
                    this.state.error = ev.error;
                    return;
                }
            }
            await this.loadExtensions();
        } catch (e: any) {
            this.state.error = e.message || t('api.installExtension');
        }
    }

    /** Delete an extension. */
    async remove(id: string) {
        this.state.error = '';
        try {
            await deleteExtension(id);
            await this.loadExtensions();
        } catch (e: any) {
            this.state.error = e.message || t('api.deleteExtension');
        }
    }

    /** Toggle extension enabled state. */
    async toggleEnabled(id: string, enabled: boolean) {
        this.state.error = '';
        try {
            await setExtensionEnabled(id, enabled);
            await this.loadExtensions();
        } catch (e: any) {
            this.state.error = e.message || t('api.updateExtension');
        }
    }

    /** Check for available updates. */
    async checkUpdates() {
        this.state.loadingUpdates = true;
        this.state.error = '';
        try {
            this.state.updates = await checkUpdates();
        } catch (e: any) {
            this.state.error = e.message || t('api.checkForUpdates');
        } finally {
            this.state.loadingUpdates = false;
        }
    }

    /** Update extension config. */
    async updateConfig(id: string, config: Record<string, any>) {
        this.state.error = '';
        try {
            await updateExtension(id, { config });
            await this.loadExtensions();
        } catch (e: any) {
            this.state.error = e.message || t('api.updateExtension');
        }
    }

    /** Check if an extension is installed by name. */
    isInstalled(name: string): boolean {
        return this.state.extensions.some(ext => ext.name === name);
    }

    /** Get installed extension by name. */
    getByname(name: string): Extension | undefined {
        return this.state.extensions.find(ext => ext.name === name);
    }

    clearError() {
        this.state.error = '';
    }
}

// Singleton instance
export const extensionManager = new ExtensionManager();
