import { describe, it, expect, vi } from 'vitest';
import { render } from '@testing-library/svelte';

import Sidebar from '$lib/components/App/Sidebar.svelte';
import type { Project } from '$lib/types';

// Mock the Icon component
vi.mock('$lib/components/Icon.svelte', () => ({
    default: () => '',
}));

// Mock SVG imports
vi.mock('$lib/assets/icons/shield.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/shield-off.svg?raw', () => ({ default: '' }));

/**
 * THE PROJECT ROW.
 *
 * One row per project, and everything on it comes from the row the server sent: the name decides where
 * it sits in the list, the counts decide which badges it wears, and `sandbox_enabled` decides whether the
 * collapsed abbreviation warns. The list is the person's own order of work, so a row that paints a figure
 * the board did not answer with is worse than no figure at all.
 *
 * That a row is a real link — href, modifier clicks, Space — is `link-rows.unit.svelte.ts`.
 */

function makeProject(id: string, name: string, overrides: Partial<Project> = {}): Project {
    return {
        id,
        name,
        path: `/home/${id}`,
        sandbox_enabled: true,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
        ...overrides,
    } as Project;
}

interface Props {
    projects?: Project[];
    currentProject?: Project | null;
    counts?: { backlog: number; in_progress: number; review: number };
    collapsed?: boolean;
}

function renderSidebar({ projects = [], currentProject = null, counts, collapsed = false }: Props = {}) {
    return render(Sidebar, {
        projects,
        currentProject,
        projectCounts: counts ? { [projects[0]?.id ?? '']: counts } : {},
        collapsed,
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

const ALPHA = makeProject('proj-1', 'alpha');

describe('the project row’s badges', () => {
    it.each([
        { counts: { backlog: 7, in_progress: 2, review: 3 }, block: true, badges: [
            { title: 'In Progress', text: '2' },
            { title: 'Review', text: '3' },
        ] },
        { counts: { backlog: 4, in_progress: 0, review: 1 }, block: true, badges: [{ title: 'Review', text: '1' }] },
        { counts: { backlog: 0, in_progress: 1, review: 0 }, block: true, badges: [{ title: 'In Progress', text: '1' }] },
        { counts: { backlog: 5, in_progress: 0, review: 0 }, block: false, badges: [] },
    ])('wears $badges for $counts', ({ counts, block, badges: expected }) => {
        const { container } = renderSidebar({ projects: [ALPHA], currentProject: ALPHA, counts });

        expect(container.querySelector('.project-name')?.textContent).toBe('alpha');
        const stats = container.querySelector('.project-stats');
        expect(stats !== null).toBe(block);
        expect(badges(container)).toEqual(expected);
        // The backlog is where work waits, not a figure worth wearing.
        if (block && counts.backlog > 0) expect(stats?.textContent).not.toContain(String(counts.backlog));
    });

    it('highlights the row and says what is waiting once a task is in Review', () => {
        const { container } = renderSidebar({
            projects: [ALPHA], currentProject: ALPHA, counts: { backlog: 0, in_progress: 0, review: 1 },
        });

        expect(container.querySelector('.project-item-wrapper.has-review')).toBeTruthy();
        expect(container.querySelector('.project-item')?.getAttribute('title') ?? '')
            .toContain('available for review');
    });

    it('warns in the collapsed abbreviation what sandboxing the row declined', () => {
        const unguarded = makeProject('test-project', 'test-project', { sandbox_enabled: false });
        const { container } = renderSidebar({
            projects: [unguarded], currentProject: unguarded, collapsed: true,
        });

        expect(container.querySelector('.sidebar')?.classList.contains('collapsed')).toBe(true);
        expect(container.querySelector('.project-abbr')?.classList.contains('project-abbr-no-sandbox')).toBe(true);
    });
});

describe('the project list’s order', () => {
    /** Deliberately unsorted with mixed case: 'Zebra' would lead a case-sensitive (ASCII) sort. */
    const UNSORTED = [
        makeProject('zebra', 'Zebra'),
        makeProject('alpha', 'alpha'),
        makeProject('bravo', 'Bravo'),
        makeProject('apple', 'Apple'),
    ];

    it('orders the rows by name whatever the case', () => {
        const { container } = renderSidebar({ projects: UNSORTED });

        expect([...container.querySelectorAll('.project-name')].map((el) => el.textContent))
            .toEqual(['alpha', 'Apple', 'Bravo', 'Zebra']);
    });

    it('reads the caller’s array without rearranging it', () => {
        const projects = [...UNSORTED];
        renderSidebar({ projects });

        expect(projects.map((p) => p.name)).toEqual(['Zebra', 'alpha', 'Bravo', 'Apple']);
    });
});

/**
 * A clone that stopped keeps its row: the bar stops where it got to, in the failure colour, the title
 * says why, the row is still not a link, and the remove button comes back — dropping it is the exit.
 */
describe('a project row whose clone stopped', () => {
    const failed = () => makeProject('clone-pending-1', 'repo', {
        cloning: true,
        cloneProgress: 60,
        cloneFailure: { message: 'git clone failed', detail: 'fatal: no such repository' },
    });

    it('names the failure, keeps its bar stopped, and offers the way out', () => {
        const { container } = renderSidebar({ projects: [failed()] });

        const row = container.querySelector('.project-item') as HTMLAnchorElement;
        expect(row.getAttribute('href')).toBeNull();
        expect(row.getAttribute('aria-busy')).toBeNull();
        expect(row.getAttribute('title')).toContain('Clone failed');
        expect(container.querySelector('.clone-progress.failed .clone-progress-bar')).not.toBeNull();
        expect((container.querySelector('.clone-progress') as HTMLElement).getAttribute('aria-valuenow')).toBe('60');
        expect(container.querySelector('.project-remove-btn')).not.toBeNull();
    });

    it('offers nothing to remove while the clone still runs', () => {
        const running = makeProject('clone-pending-2', 'repo', { cloning: true, cloneProgress: 20 });
        const { container } = renderSidebar({ projects: [running] });

        expect((container.querySelector('.project-item') as HTMLElement).getAttribute('aria-busy')).toBe('true');
        expect(container.querySelector('.project-remove-btn')).toBeNull();
    });
});
