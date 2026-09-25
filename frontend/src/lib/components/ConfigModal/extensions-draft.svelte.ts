/**
 * The Edit dialog's draft: the values a person is editing plus the state of the one dialog that
 * holds them — not the `Extension` the server sends. `drafts.ts` beside it is the same shape for
 * Settings → Providers.
 *
 * Two rules the fields here exist to hold:
 *
 * - A directory grant stops the running sandbox, so it rides the dialog's Save and Cancel has to
 *   mean nothing changed. That is why the draft holds the *wanted* path and the record keeps the
 *   stored one until the save says otherwise.
 * - The dependency field may open with a suggestion rather than an answer, and the suggestion can
 *   land after the dialog opened because it costs one fetch. `setHomeDirectory` fills an empty draft
 *   once and never overwrites what someone typed.
 */
import type { Extension } from '$lib/types';
import type { ExtensionUrlProbe } from '$lib/api';
import { initialEditConfig } from '$lib/utils/extension-config';
import {
    agentMountDraft,
    agentMountUpdate,
    dependencyDraft,
    type AgentMountProposal,
    type AgentMountRow,
} from '$lib/utils/extension-permissions';

/** The three LSP feature gates, per extension and independent of each other. */
export interface LspFeatureFlags {
    editor_enabled: boolean;
    tool_calls_enabled: boolean;
    chat_diagnostics_enabled: boolean;
}

/** The flags a record that declares none is read with. Chat diagnostics is the one that defaults off. */
function defaultLspFlags(): LspFeatureFlags {
    return { editor_enabled: true, tool_calls_enabled: true, chat_diagnostics_enabled: false };
}

export class ExtensionDraft {
    /** The stored record being edited; null while no dialog is open. */
    extension = $state<Extension | null>(null);
    config = $state<Record<string, any>>({});
    lsp = $state<LspFeatureFlags>(defaultLspFlags());
    /** The dependency directory the person wants, which is not yet the one granted. */
    dependency = $state('');
    dependencyError = $state('');
    /** One row per folder the manifest suggested, plus every folder already approved. */
    mounts = $state<AgentMountRow[]>([]);
    mountsError = $state('');
    saving = $state(false);
    /** The probe's answer belongs to this dialog, never to a row of the database. */
    probingKey = $state('');
    probeAnswers = $state<Record<string, ExtensionUrlProbe>>({});
    /** The server's own home, once reported — the root the dependency suggestion is mirrored under. */
    homeDirectory = $state('');

    // Not reactive state: nothing renders from these, so no effect re-runs when they are read.
    private homeTried = false;
    private dependencyFilled = false;

    /** Open the dialog on one record: every field starts from what the server stored. */
    open(ext: Extension) {
        this.extension = ext;
        // For LSP extensions this seeds the host-owned memory_limit from the effective
        // sandbox_config value (0 = no limit).
        this.config = initialEditConfig(ext);
        const lsp = ext.manifest.capabilities?.lsp;
        this.lsp = {
            editor_enabled: lsp?.editor_enabled ?? true,
            tool_calls_enabled: lsp?.tool_calls_enabled ?? true,
            chat_diagnostics_enabled: lsp?.chat_diagnostics_enabled ?? false,
        };
        this.dependency = dependencyDraft(ext, this.homeDirectory);
        this.dependencyError = '';
        this.mounts = agentMountDraft(ext);
        this.mountsError = '';
        this.probingKey = '';
        this.probeAnswers = {};
        this.saving = false;
        // "Filled" means the draft holds the real answer rather than a placeholder waiting on the
        // home directory: a grant is final, and a suggestion needs no home to have been read from.
        this.dependencyFilled = !!ext.sandbox_config?.dependency_host_path || this.homeDirectory !== '';
    }

    /** Close the dialog. Nothing here is read while it is closed. */
    close() {
        this.extension = null;
        this.config = {};
        this.lsp = defaultLspFlags();
        this.dependency = '';
        this.dependencyError = '';
        this.mounts = [];
        this.mountsError = '';
        this.saving = false;
        this.probingKey = '';
        this.probeAnswers = {};
        this.dependencyFilled = false;
    }

    /** True once, for whoever asks first: the home directory costs one fetch for the whole tab. */
    wantsHomeDirectory(): boolean {
        if (this.homeTried || this.homeDirectory) return false;
        this.homeTried = true;
        return true;
    }

    setHomeDirectory(home: string) {
        this.homeDirectory = home;
        if (!home || this.dependencyFilled || !this.extension || this.dependency) return;
        this.dependencyFilled = true;
        this.dependency = dependencyDraft(this.extension, home);
    }

    /** The folders this dialog would approve, in the shape the endpoint takes. */
    mountsToSend(): AgentMountProposal[] {
        return agentMountUpdate(this.mounts);
    }

    /** After a save the server's list is the one to show: it expands `~` and resolves symlinks. */
    reseedMounts() {
        if (this.extension) this.mounts = agentMountDraft(this.extension);
    }

    /** The row a "use the suggestion" click fills. */
    setMountHost(index: number, hostPath: string) {
        const row = this.mounts[index];
        if (row) row.host_path = hostPath;
    }

    /** One probe at a time: a click while one is in flight answers nothing. */
    beginProbe(key: string) {
        this.probingKey = key;
        delete this.probeAnswers[key];
    }

    /** A failed request is still an answer: it fills the same slot as a successful one. */
    answerProbe(key: string, answer: ExtensionUrlProbe) {
        this.probeAnswers[key] = answer;
        this.probingKey = '';
    }
}
