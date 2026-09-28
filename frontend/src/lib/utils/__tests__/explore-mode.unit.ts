import { describe, it, expect } from 'vitest';
import { applyExploreModeRule } from '$lib/utils/explore-mode';

function state(mode: string, changedByUser = false, base: string | null = null) {
    return { mode, modeChangedByUser: changedByUser, autoExploreBase: base };
}

describe('applyExploreModeRule', () => {
    it('leaves state unchanged when there is no ? and mode is not explore', () => {
        const result = applyExploreModeRule('fix the login bug', state('act'));
        expect(result).toEqual({ mode: 'act', modeChangedByUser: false, autoExploreBase: null });
    });

    it('switches to explore when ? appears and the user has not changed the mode', () => {
        const result = applyExploreModeRule('why is login broken?', state('act'));
        expect(result).toEqual({ mode: 'explore', modeChangedByUser: false, autoExploreBase: 'act' });
    });

    it('records plan as base mode when auto-switching from plan', () => {
        const result = applyExploreModeRule('what does this do?', state('plan'));
        expect(result).toEqual({ mode: 'explore', modeChangedByUser: false, autoExploreBase: 'plan' });
    });

    it('does not switch when the user has manually changed the mode', () => {
        const result = applyExploreModeRule('why is login broken?', state('plan', true));
        expect(result).toEqual({ mode: 'plan', modeChangedByUser: true, autoExploreBase: null });
    });

    it('is idempotent while ? remains in the prompt', () => {
        const result = applyExploreModeRule('why is login broken?', state('explore', false, 'act'));
        expect(result).toEqual({ mode: 'explore', modeChangedByUser: false, autoExploreBase: 'act' });
    });

    it('reverts to the base mode when ? is removed after an auto-switch', () => {
        const result = applyExploreModeRule('why is login broken', state('explore', false, 'act'));
        expect(result).toEqual({ mode: 'act', modeChangedByUser: false, autoExploreBase: null });
    });

    it('does not revert when the user manually selected explore', () => {
        const result = applyExploreModeRule('no question here', state('explore', true));
        expect(result).toEqual({ mode: 'explore', modeChangedByUser: true, autoExploreBase: null });
    });

    it('does not revert when the default mode is already explore (no base recorded)', () => {
        const result = applyExploreModeRule('no question here', state('explore'));
        expect(result).toEqual({ mode: 'explore', modeChangedByUser: false, autoExploreBase: null });
    });

    it('leaves a non-explore mode untouched when ? is removed (no base to revert to)', () => {
        const result = applyExploreModeRule('no question here', state('plan'));
        expect(result).toEqual({ mode: 'plan', modeChangedByUser: false, autoExploreBase: null });
    });

    it('does not revert when the user has manually changed the mode', () => {
        const result = applyExploreModeRule('no question here', state('explore', true, 'act'));
        expect(result).toEqual({ mode: 'explore', modeChangedByUser: true, autoExploreBase: 'act' });
    });

});
