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

describe('Sidebar collapsed abbreviation color', () => {
    it('should show red abbreviation for project with sandbox disabled', () => {
        const project = {
            id: 'test-project',
            name: 'test-project',
            path: '/home/test-project',
            sandbox_enabled: false,
            created_at: new Date().toISOString(),
            updated_at: new Date().toISOString(),
        };

        const { container } = render(Sidebar, {
            projects: [project],
            currentProject: project,
            projectCounts: { 'test-project': { backlog: 0, in_progress: 0, review: 0 } },
            collapsed: true,
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

        // Sidebar should have collapsed class
        const sidebar = container.querySelector('.sidebar');
        expect(sidebar).toBeTruthy();
        expect(sidebar?.classList.contains('collapsed')).toBe(true);

        // The project abbreviation should be visible
        const abbr = container.querySelector('.project-abbr');
        expect(abbr).toBeTruthy();

        // The project abbreviation should have the no-sandbox class (red color)
        expect(abbr?.classList.contains('project-abbr-no-sandbox')).toBe(true);
    });
});
