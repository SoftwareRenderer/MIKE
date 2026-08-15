/**
 * Auto-switch to Explore mode if the user types in a question mark
 */
export interface ExploreModeState {
    mode: string; // "act" | "plan" | "explore"
    modeChangedByUser: boolean; // user picked a mode explicitly
    autoExploreBase: string | null; // mode before auto-switch; null if not auto-set
}

export function applyExploreModeRule(prompt: string, prev: ExploreModeState): ExploreModeState {
    if (prev.modeChangedByUser) return prev;
    if (prompt.includes('?')) {
        if (prev.mode !== 'explore') {
            return { ...prev, mode: 'explore', autoExploreBase: prev.mode };
        }
        return prev;
    }
    if (prev.mode === 'explore' && prev.autoExploreBase !== null) {
        return { ...prev, mode: prev.autoExploreBase, autoExploreBase: null };
    }
    return prev;
}
