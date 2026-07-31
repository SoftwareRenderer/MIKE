import type { Extension, RegistryEntry, ExtensionUpdate } from '$lib/types';
import { listExtensions, getRegistryIndex, installExtension, deleteExtension, setExtensionEnabled, checkUpdates, updateExtension } from '$lib/api';
import { uiInjector } from './ui-injector.svelte';

/** Runtime status of an extension sandbox. */
export type ExtensionStatus = 'running' | 'stopped' | 'error' | 'unknown';

/** ExtensionManager is a reactive singleton that manages extension state. */
export class ExtensionManager {
    state = $state({
        extensions: [] as Extension[],
        registry: [] as RegistryEntry[],
        updates: [] as ExtensionUpdate[],
        loading: false,
        loadingRegistry: false,
        loadingUpdates: false,
        error: '',
        registryUrl: '',
    });

    /** Load all installed extensions from the server. */
    async loadExtensions() {
        this.state.loading = true;
        this.state.error = '';
        try {
            this.state.extensions = await listExtensions();
            uiInjector.registerFromExtensions(this.state.extensions);
        } catch (e: any) {
            this.state.error = e.message || 'Failed to load extensions';
        } finally {
            this.state.loading = false;
        }
    }

    /** Get enabled LSP extensions with editor features enabled. */
    getEditorLSPExtensions(): Extension[] {
        return this.state.extensions.filter(
            ext => ext.type === 'lsp' && ext.enabled && ext.manifest.capabilities?.lsp?.editor_enabled !== false
        );
    }

    /** Get enabled UI extensions. */
    getUIExtensions(): Extension[] {
        return this.state.extensions.filter(
            ext => ext.type === 'ui' && ext.enabled
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

    /** Check if an LSP extension is available for a language (editor features enabled). */
    hasLSPForLanguage(language: string): boolean {
        return this.getEditorLSPExtensions().some(
            ext => ext.manifest.capabilities?.lsp?.language === language
        );
    }

    /** Get the LSP extension for a given language (editor features enabled). */
    getLSPForLanguage(language: string): Extension | undefined {
        return this.getEditorLSPExtensions().find(
            ext => ext.manifest.capabilities?.lsp?.language === language
        );
    }

    /** Get the LSP extension for a given file extension (editor features enabled). */
    getLSPForFile(file: string): Extension | undefined {
        const ext = file.split('.').pop()?.toLowerCase();
        if (!ext) return undefined;
        return this.getEditorLSPExtensions().find(
            e => e.manifest.capabilities?.lsp?.extensions?.includes(ext)
        );
    }

    /** Get all supported languages from installed LSP extensions (editor features enabled). */
    getSupportedLanguages(): string[] {
        const langs = new Set<string>();
        for (const ext of this.getEditorLSPExtensions()) {
            if (ext.manifest.capabilities?.lsp?.language) {
                langs.add(ext.manifest.capabilities.lsp.language);
            }
        }
        return [...langs];
    }

    /** Load registry index. */
    async loadRegistry(url?: string) {
        this.state.loadingRegistry = true;
        this.state.error = '';
        try {
            this.state.registry = await getRegistryIndex(url || this.state.registryUrl || undefined);
        } catch (e: any) {
            this.state.error = e.message || 'Failed to load registry';
        } finally {
            this.state.loadingRegistry = false;
        }
    }

    /** Install an extension from the registry. */
    async install(name: string) {
        this.state.error = '';
        try {
            await installExtension(name, this.state.registryUrl || undefined);
            await this.loadExtensions();
        } catch (e: any) {
            this.state.error = e.message || 'Failed to install extension';
        }
    }

    /** Delete an extension. */
    async remove(id: string) {
        this.state.error = '';
        try {
            await deleteExtension(id);
            await this.loadExtensions();
        } catch (e: any) {
            this.state.error = e.message || 'Failed to delete extension';
        }
    }

    /** Toggle extension enabled state. */
    async toggleEnabled(id: string, enabled: boolean) {
        this.state.error = '';
        try {
            await setExtensionEnabled(id, enabled);
            await this.loadExtensions();
        } catch (e: any) {
            this.state.error = e.message || 'Failed to update extension';
        }
    }

    /** Check for available updates. */
    async checkUpdates(url?: string) {
        this.state.loadingUpdates = true;
        this.state.error = '';
        try {
            this.state.updates = await checkUpdates(url || this.state.registryUrl || undefined);
        } catch (e: any) {
            this.state.error = e.message || 'Failed to check for updates';
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
            this.state.error = e.message || 'Failed to update extension';
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

    /** Update registry URL. */
    setRegistryUrl(url: string) {
        this.state.registryUrl = url;
    }

    /** Clear error. */
    clearError() {
        this.state.error = '';
    }
}

// Singleton instance
export const extensionManager = new ExtensionManager();
