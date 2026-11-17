import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';
import ExtensionUpdatesPanel from '../ExtensionUpdatesPanel.svelte';
import type { ExtensionUpdate } from '$lib/types';

/**
 * "Updates Available" renders what the check said: the entries newer than the commit pinned here, oldest
 * first, each with the full hash behind the short chip — and an update the registry shipped no CHANGELOG
 * for says nothing about changes.
 *
 * The panel is prop-driven, so the payload is staged as a prop rather than as a route stub: an interception
 * in the e2e lane would only ever be read back by the spec that wrote it.
 */

const COMMIT_A = 'a'.repeat(64);
const COMMIT_B = 'b'.repeat(64);
const COMMIT_C = 'c'.repeat(64);

const UPDATES: ExtensionUpdate[] = [
    {
        id: 'changelog-ext',
        name: 'changelog-ext',
        current_version: '1.0.0',
        available_version: '1.2.0',
        current_commit: COMMIT_A,
        available_commit: COMMIT_C,
        changelog: [
            { hash: COMMIT_B, version: '1.1.0', description: 'Fix hover offsets' },
            { hash: COMMIT_C, version: '1.2.0', description: 'Add dark mode support' },
        ],
    },
    {
        id: 'plain-ext',
        name: 'plain-ext',
        current_version: '1.0.0',
        available_version: '2.0.0',
    },
];

function renderPanel() {
    return render(ExtensionUpdatesPanel, {
        props: {
            updates: UPDATES,
            hasChecked: true,
            updatingAll: false,
            awaitingReview: [],
            onupdate: vi.fn(),
            onupdateall: vi.fn(),
        },
    });
}

function row(container: HTMLElement, name: string): HTMLElement {
    const found = [...container.querySelectorAll<HTMLElement>('.update-item')]
        .find((r) => r.querySelector('.update-name')?.textContent?.trim() === name);
    if (!found) throw new Error(`no update row for ${name}`);
    return found;
}

afterEach(cleanup);

describe('the changelog of an available update', () => {
    it('lists every entry since the installed commit, oldest first, under the version it is since', () => {
        const { container } = renderPanel();
        expect(container.querySelectorAll('.update-item')).toHaveLength(2);

        const changelog = row(container, 'changelog-ext').querySelector('.update-changelog') as HTMLElement;
        expect(changelog).toBeTruthy();
        expect(changelog.querySelector('.update-changelog-title')?.textContent)
            .toBe('Changes since 1.0.0:');

        const items = [...changelog.querySelectorAll('.update-changelog-item')];
        expect(items).toHaveLength(2);
        expect(items[0].querySelector('.update-changelog-desc')?.textContent).toBe('Fix hover offsets');
        expect(items[1].querySelector('.update-changelog-desc')?.textContent).toBe('Add dark mode support');
    });

    it('keeps the full hash reachable behind the short chip', () => {
        const { container } = renderPanel();
        const chips = [...row(container, 'changelog-ext').querySelectorAll('.update-changelog-hash')];
        expect(chips).toHaveLength(2);
        // Seven is what fits the row; the tooltip is what identifies the commit.
        expect(chips[0].textContent).toBe('bbbbbbb');
        expect(chips[0].getAttribute('title')).toBe(COMMIT_B);
        expect(chips[1].getAttribute('title')).toBe(COMMIT_C);
    });

    it('says nothing about changes for an update that shipped no changelog', () => {
        const { container } = renderPanel();
        expect(row(container, 'plain-ext').querySelector('.update-changelog')).toBeNull();
    });
});
