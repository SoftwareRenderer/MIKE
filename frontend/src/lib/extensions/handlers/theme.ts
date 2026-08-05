/**
 * `theme.*` — expose the host app's live CSS custom properties to sandboxed
 * UI extensions.
 *
 * Sandboxed extension iframes (opaque origin, no `allow-same-origin`) cannot
 * inherit the host page's stylesheet, including a theme extension's `:root`
 * override (e.g. the Light mode extension). These capabilities let an
 * extension read the host's current palette (`theme.get`) and keep in sync
 * when it changes (`theme.update`), so extension-rendered content matches the
 * app's active theme.
 */
import type { CapabilityDef } from '../types';

export const CAP_THEME_GET = 'theme.get';
export const CAP_THEME_UPDATE = 'theme.update';

/**
 * Read every CSS custom property currently in effect on the host document
 * (base `:root` palette from app.css plus any active theme extension override).
 * Returns the resolved values so the extension can re-apply them verbatim.
 */
export function readThemeVars(): Record<string, string> {
    const out: Record<string, string> = {};
    const cs = getComputedStyle(document.documentElement);
    for (let i = 0; i < cs.length; i++) {
        const name = cs[i];
        if (name.startsWith('--')) {
            const value = cs.getPropertyValue(name).trim();
            if (value) out[name] = value;
        }
    }
    return out;
}

export const themeCapabilities: Record<string, CapabilityDef> = {
    [CAP_THEME_GET]: {
        kind: 'call',
        argsSchema: {},
        handler: (): { variables: Record<string, string> } => ({
            variables: readThemeVars(),
        }),
    },
    // Push the current variables to an extension when the theme changes.
    // The payload is the same shape as `theme.get`'s return value.
    [CAP_THEME_UPDATE]: { kind: 'event' },
};
