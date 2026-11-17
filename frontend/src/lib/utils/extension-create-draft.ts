import type { MessageKey } from '$lib/i18n';

/** What the Create extension form holds: a name and what the extension should do. */
export interface ExtensionCreateDraft {
    name: string;
    description: string;
}

/** The catalog key of each field's complaint, or nothing when the draft may be sent. */
export type ExtensionCreateErrors = { name?: MessageKey; description?: MessageKey };

/**
 * The type is never asked for: the agent in the first task decides it, so a picker here would be a
 * second, worse answer to a question the task already answers. The server holds the real rule, so this
 * only stops a click that could not have been anything but a refusal.
 */
export function validateExtensionCreateDraft(draft: ExtensionCreateDraft): ExtensionCreateErrors {
    const errors: ExtensionCreateErrors = {};
    if (!draft.name.trim()) {
        errors.name = 'settings.extensions.createNameRequired';
    } else if (!/[\p{L}\p{N}]/u.test(draft.name)) {
        errors.name = 'settings.extensions.createNameUnusable';
    }
    if (!draft.description.trim()) {
        errors.description = 'settings.extensions.createDescriptionRequired';
    }
    return errors;
}
