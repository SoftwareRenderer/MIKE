import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';

vi.mock('$lib/assets/icons/shield.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/shield-off.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));
vi.mock('$lib/stores', () => ({ appStore: { config: null } }));

import TopBar from '$lib/components/App/TopBar.svelte';
import type { Project } from '$lib/types';

// Drives the useIsMobile() matchMedia stub per test (read at stub-call time).
let mediaMatches = true;

beforeEach(() => {
    mediaMatches = true;
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: mediaMatches,
        media: query,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
    }));
});

afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
});

const PATH = '/home/tester/repos/old-project-dir';
const HOME_RELATIVE = '~/repos/old-project-dir';

function renderBar(project: Partial<Project> = {}) {
    const currentProject = {
        id: 'proj-1',
        name: 'MIKE',
        path: PATH,
        ...project,
    } as Project;
    return render(TopBar, {
        props: {
            currentProject,
            currentBranch: 'main',
            onOpenMenu: vi.fn(),
            onOpenSandboxConfig: vi.fn(),
            onOpenExplorerView: vi.fn(),
            onOpenTerminal: vi.fn(),
            onShowStats: vi.fn(),
            onShowConfig: vi.fn(),
        },
    });
}

function label(container: HTMLElement): string {
    return container.querySelector('.project-path')!.textContent!.trim();
}

describe('Explorer button label', () => {
    it('names the project on mobile instead of its path', () => {
        const { container } = renderBar();
        expect(label(container)).toBe('MIKE');
        expect(container.textContent).not.toContain(HOME_RELATIVE);
    });

    it('shows the home-relative path on desktop', () => {
        mediaMatches = false;
        const { container } = renderBar();
        expect(label(container)).toBe(HOME_RELATIVE);
    });

    it('keeps a label on mobile when the project has no name', () => {
        const { container } = renderBar({ name: '' });
        expect(label(container)).toBe(HOME_RELATIVE);
    });

    it('keeps the button the explorer shortcut and e2e specs click', () => {
        const { container } = renderBar();
        expect(container.querySelector('button.explorer-btn')).toBeTruthy();
        mediaMatches = false;
        cleanup();
        const desktop = renderBar();
        expect(desktop.container.querySelector('button.explorer-btn')).toBeTruthy();
    });
});
