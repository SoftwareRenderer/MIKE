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

function makeProject(id: string, name: string) {
    return {
        id,
        name,
        path: `/home/${id}`,
        sandbox_enabled: true,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
    };
}

describe('Sidebar project sorting', () => {
    it('should sort projects alphabetically, case-insensitively', () => {
        // Input is deliberately unsorted with mixed case: 'Zebra' would come
        // before 'alpha' in a case-sensitive (ASCII) sort.
        const projects = [
            makeProject('zebra', 'Zebra'),
            makeProject('alpha', 'alpha'),
            makeProject('bravo', 'Bravo'),
            makeProject('apple', 'Apple'),
        ];

        const { container } = render(Sidebar, {
            projects,
            currentProject: null,
            projectCounts: {},
            collapsed: false,
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

        const names = [...container.querySelectorAll('.project-name')].map((el) => el.textContent);
        expect(names).toEqual(['alpha', 'Apple', 'Bravo', 'Zebra']);
    });

    it('should not mutate the input projects array', () => {
        const projects = [
            makeProject('bravo', 'Bravo'),
            makeProject('alpha', 'alpha'),
        ];

        render(Sidebar, {
            projects,
            currentProject: null,
            projectCounts: {},
            collapsed: false,
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

        expect(projects.map((p) => p.name)).toEqual(['Bravo', 'alpha']);
    });
});
