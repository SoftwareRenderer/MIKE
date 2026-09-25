/**
 * What a manifest declares, in the words Settings → Extensions shows: the type badge, the
 * capability badges and their explanations, the UI injection points, the locale tag a translation
 * pack names, and the built-at line.
 *
 * Every dictionary here is a function, so the wording comes from the active translation pack and
 * not from whichever one loaded first. Capability names are never interpreted: the badge list is
 * what the manifest declared, and an unrecognised type still renders — a manifest the host does not
 * know is most likely a newer one.
 */
import { t } from '$lib/i18n';
import { timeAgo } from './format-time';

/**
 * Badge text for an extension type. An unrecognized type still renders UI because that is what a
 * manifest the host does not know most likely is — but a translation pack must not wear that label,
 * or the row the user came to Settings to toggle is indistinguishable from an iframe extension.
 */
export function typeBadge(type: string): string {
    switch (type) {
        case 'lsp': return 'LSP';
        case 'css': return 'CSS';
        case 'agent': return 'AGENT';
        case 'llm': return 'LLM';
        case 'i18n': return 'I18N';
        case 'tool': return 'TOOL';
        default: return 'UI';
    }
}

/** The capability keys an installed extension declares, in the order the detail dialog lists them. */
export function capabilityKeys(ext: { manifest?: { capabilities?: Record<string, any> | null } | null }): string[] {
    const caps = ext.manifest?.capabilities;
    if (!caps) return [];
    const keys: string[] = [];
    if (caps.lsp) keys.push('lsp');
    if (caps.ui) keys.push('ui');
    if (caps.css) keys.push('css');
    if (caps.agent) keys.push('agent');
    if (caps.network) keys.push('network');
    if (caps.llm) keys.push('llm');
    if (caps.i18n) keys.push('i18n');
    if (caps.tools) keys.push('tools');
    return keys;
}

/**
 * Capability keys for a registry entry. Prefers the entry's declared capabilities (which may include
 * e.g. network alongside ui), falling back to the capability implied by its type.
 */
/** The two fields a Browse entry carries that decide which badges it shows. */
export interface RegistryShape {
    type?: string;
    capabilities?: Record<string, any> | null;
}

export function registryCapabilityKeys(entry: RegistryShape): string[] {
    if (entry.capabilities && Object.keys(entry.capabilities).length > 0) {
        return Object.keys(entry.capabilities).filter((k) => !!capabilityDescriptions()[k]);
    }
    const capMap: Record<string, string> = { lsp: 'lsp', ui: 'ui', css: 'css', agent: 'agent', llm: 'llm', i18n: 'i18n' };
    const k = capMap[entry.type ?? ''];
    return k ? [k] : [];
}

/**
 * A capabilities object for a registry entry: the declared capability config when present, so the
 * detail dialog can show the same details as the Installed view, otherwise empty objects per
 * capability.
 */
export function registryCapabilities(entry: RegistryShape): Record<string, any> {
    if (entry.capabilities && Object.keys(entry.capabilities).length > 0) {
        return entry.capabilities;
    }
    const caps: Record<string, any> = {};
    for (const k of registryCapabilityKeys(entry)) caps[k] = {};
    return caps;
}

/** The label and explanation the badges and the capability dialog show for one capability. */
export function capabilityDescriptions(): Record<string, { label: string; description: string }> {
    return {
        network: {
            label: t('settings.extensions.cap.network.label'),
            description: t('settings.extensions.cap.network.description'),
        },
        lsp: {
            label: t('settings.extensions.cap.lsp.label'),
            description: t('settings.extensions.cap.lsp.description'),
        },
        ui: {
            label: t('settings.extensions.cap.ui.label'),
            description: t('settings.extensions.cap.ui.description'),
        },
        css: {
            label: t('settings.extensions.cap.css.label'),
            description: t('settings.extensions.cap.css.description'),
        },
        agent: {
            label: t('settings.extensions.cap.agent.label'),
            description: t('settings.extensions.cap.agent.description'),
        },
        llm: {
            label: t('settings.extensions.cap.llm.label'),
            description: t('settings.extensions.cap.llm.description'),
        },
        i18n: {
            label: t('settings.extensions.cap.i18n.label'),
            description: t('settings.extensions.cap.i18n.description'),
        },
        tools: {
            label: t('settings.extensions.cap.tools.label'),
            description: t('settings.extensions.cap.tools.description'),
        },
    };
}

/** Labels for the UI injection points a `ui` capability declares. */
export function injectionPointLabels(): Record<string, string> {
    return {
        'taskCard': t('settings.extensions.point.taskCard'),
        'fileEditor': t('settings.extensions.point.fileEditor'),
        'projectBoard': t('settings.extensions.point.projectBoard'),
        'taskChat': t('settings.extensions.point.taskChat'),
        'projectSidebar': t('settings.extensions.point.projectSidebar'),
        'fileBrowser': t('settings.extensions.point.fileBrowser'),
        'createTask': t('settings.extensions.point.createTask'),
    };
}

/**
 * The locale an `i18n` pack declares. It is manifest data, so it is read through an
 * untyped accessor rather than the capability struct: the pack names its own language.
 */
export function packLocale(capabilities: unknown): string {
    const code = (capabilities as { i18n?: { locale?: unknown } } | undefined)?.i18n?.locale;
    return typeof code === 'string' ? code : '';
}

/** A build timestamp as a readable relative time. */
export function formatBuiltAt(builtAt: string): string {
    if (!builtAt) return t('settings.extensions.notBuilt');
    // 'calendar': a build from last March is a date, not an age.
    return timeAgo(builtAt, { scale: 'calendar', template: 'time.built' }) || t('time.built', { when: builtAt });
}
