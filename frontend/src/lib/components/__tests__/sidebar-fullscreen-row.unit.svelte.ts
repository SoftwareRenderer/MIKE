import { describe, it, expect, vi, afterEach } from 'vitest';
import { render } from '@testing-library/svelte';

import Sidebar from '$lib/components/App/Sidebar.svelte';

// Mock the Icon component
vi.mock('$lib/components/Icon.svelte', () => ({
    default: () => '',
}));

// Mock SVG imports
vi.mock('$lib/assets/icons/shield.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/shield-off.svg?raw', () => ({ default: '' }));

const project = {
    id: 'proj-1',
    name: 'alpha',
    path: '/home/proj-1',
    sandbox_enabled: true,
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
};

/** jsdom answers no media query at all (no `matchMedia` on window), so the installed-window answer needs a stub. */
function stubDisplayMode(matches: boolean) {
    window.matchMedia = vi.fn(() => ({
        matches,
        media: '(display-mode: standalone)',
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        dispatchEvent: vi.fn(),
    } as unknown as MediaQueryList));
}

function clearDisplayMode() {
    delete (window as unknown as { matchMedia?: unknown }).matchMedia;
}

function renderSidebar(isMobile: boolean) {
    return render(Sidebar, {
        projects: [project],
        currentProject: project,
        projectCounts: {},
        collapsed: false,
        isMobile,
        mobileMenuOpen: true,
        getProjectAbbr: (name: string) => name.slice(0, 2).toUpperCase(),
        onSelectProject: vi.fn(),
        onRemoveProject: vi.fn(),
        onToggleSidebar: vi.fn(),
        onShowStats: vi.fn(),
        onShowConfig: vi.fn(),
        onToggleFullScreen: vi.fn(),
        onAddProject: vi.fn(),
    });
}

const FULL_SCREEN_ROW = '[aria-label="Toggle full screen"]';

describe('Sidebar full screen row', () => {
    afterEach(() => clearDisplayMode());

    it('is gone when a mobile viewport runs the installed app', () => {
        stubDisplayMode(true);
        const { container } = renderSidebar(true);

        expect(container.querySelector(FULL_SCREEN_ROW)).toBeNull();
        // The rest of the mobile menu is untouched — an empty menu would pass the assertion above too.
        expect(container.querySelector('[aria-label="Token usage stats"]')).toBeTruthy();
    });

    it('stays when a mobile viewport is a browser tab', () => {
        stubDisplayMode(false);
        const { container } = renderSidebar(true);

        expect(container.querySelector(FULL_SCREEN_ROW)).toBeTruthy();
    });

    it('stays in the installed window on a desktop viewport, where CSS hides the whole menu', () => {
        stubDisplayMode(true);
        const { container } = renderSidebar(false);

        expect(container.querySelector(FULL_SCREEN_ROW)).toBeTruthy();
    });
});
