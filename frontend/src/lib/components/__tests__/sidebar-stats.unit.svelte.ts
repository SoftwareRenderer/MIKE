import { describe, it, expect, vi } from 'vitest';
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

function renderSidebar(counts: { backlog: number; in_progress: number; review: number } | undefined) {
    return render(Sidebar, {
        projects: [project],
        currentProject: project,
        projectCounts: counts ? { 'proj-1': counts } : {},
        collapsed: false, // stats are hidden while collapsed
        isMobile: false,
        mobileMenuOpen: false,
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

/** Badge titles in render order, with their count text. */
function badges(container: Element) {
    return [...container.querySelectorAll('.stat-badge')].map((el) => ({
        title: el.getAttribute('title'),
        text: el.textContent?.replace(/\s+/g, '').trim(),
    }));
}

describe('Sidebar project stats', () => {
    it('never renders the backlog badge', () => {
        const { container } = renderSidebar({ backlog: 7, in_progress: 2, review: 3 });

        expect(container.querySelector('.stat-badge[title="Backlog"]')).toBeNull();
        const stats = container.querySelector('.project-stats');
        expect(stats).toBeTruthy();
        expect(stats?.textContent).not.toContain('7');
    });

    it('shows a badge only for counts greater than zero (review only)', () => {
        const { container } = renderSidebar({ backlog: 4, in_progress: 0, review: 1 });

        expect(badges(container)).toEqual([{ title: 'Review', text: '1' }]);
    });

    it('shows a badge only for counts greater than zero (in-progress only)', () => {
        const { container } = renderSidebar({ backlog: 0, in_progress: 1, review: 0 });

        expect(badges(container)).toEqual([{ title: 'In Progress', text: '1' }]);
    });

    it('shows both badges when both counts are non-zero', () => {
        const { container } = renderSidebar({ backlog: 9, in_progress: 2, review: 3 });

        expect(badges(container)).toEqual([
            { title: 'In Progress', text: '2' },
            { title: 'Review', text: '3' },
        ]);
    });

    it('renders no stats block at all when nothing is active', () => {
        const { container } = renderSidebar({ backlog: 5, in_progress: 0, review: 0 });

        expect(container.querySelector('.project-stats')).toBeNull();
        expect(container.querySelectorAll('.stat-badge')).toHaveLength(0);
        // The project row itself still renders
        expect(container.querySelector('.project-name')?.textContent).toBe('alpha');
    });

    it('keeps the review affordances (row highlight + tooltip) at review > 0', () => {
        const { container } = renderSidebar({ backlog: 0, in_progress: 0, review: 1 });

        expect(container.querySelector('.project-item-wrapper.has-review')).toBeTruthy();
        const title = container.querySelector('button.project-item')?.getAttribute('title') ?? '';
        expect(title).toContain('available for review');
    });
});
