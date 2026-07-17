import { describe, it, expect, vi } from 'vitest';
import { render } from '@testing-library/svelte';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));

import Sidebar from '$lib/components/App/Sidebar.svelte';
import Column from '$lib/components/Column.svelte';
import type { Project } from '$lib/types';

// Mock dependencies
vi.mock('$lib/components/Icon.svelte', () => ({
    default: () => '',
}));
vi.mock('$lib/assets/icons/shield.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/shield-off.svg?raw', () => ({ default: '' }));

function createMockProject(): Project {
    return {
        id: 'test-project',
        name: 'test-project',
        path: '/home/test-project',
        sandbox_enabled: false,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
    };
}

describe('Accessibility (Section 508 / WCAG 2.1 AA)', () => {
    describe('Sidebar', () => {
        it('sidebar nav has aria-label', () => {
            const project = createMockProject();
            const { container } = render(Sidebar, {
                projects: [project],
                currentProject: project,
                projectCounts: { 'test-project': { backlog: 0, in_progress: 0, review: 0 } },
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

            const nav = container.querySelector('nav[aria-label="Projects"]');
            expect(nav).toBeTruthy();
        });

        it('sidebar toggle has aria-expanded', () => {
            const project = createMockProject();
            const { container } = render(Sidebar, {
                projects: [project],
                currentProject: project,
                projectCounts: { 'test-project': { backlog: 0, in_progress: 0, review: 0 } },
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

            const toggleBtn = container.querySelector('[aria-controls="sidebar"]');
            expect(toggleBtn).toBeTruthy();
            const expanded = toggleBtn?.getAttribute('aria-expanded');
            expect(['true', 'false']).toContain(expanded);
        });

        it('decorative SVGs have aria-hidden="true"', () => {
            const project = createMockProject();
            const { container } = render(Sidebar, {
                projects: [project],
                currentProject: project,
                projectCounts: { 'test-project': { backlog: 0, in_progress: 0, review: 0 } },
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

            const svgInButtons = container.querySelectorAll('button > svg[aria-hidden="true"]');
            expect(svgInButtons.length).toBeGreaterThan(0);
        });
    });

    describe('Card', () => {
        // Card has complex store dependencies, so we verify accessibility via source code
        it('cards are keyboard accessible with role="button" and tabindex', () => {
            const cardPath = resolve(__dirname, '../Card.svelte');
            const source = readFileSync(cardPath, 'utf-8');

            expect(source).toContain('role="button"');
            expect(source).toContain('tabindex="0"');
            expect(source).toContain('onkeydown={handleCardKeydown}');
        });

        it('icon-only buttons have aria-labels', () => {
            const cardPath = resolve(__dirname, '../Card.svelte');
            const source = readFileSync(cardPath, 'utf-8');

            // Verify action buttons have aria-labels
            expect(source).toContain('aria-label="Create subtask"');
            expect(source).toContain('aria-label="Edit task"');
            expect(source).toContain('aria-label="Pause task"');
            expect(source).toContain('aria-label="Resume task"');
            expect(source).toContain('aria-label="Delete task"');
        });

        it('expand/collapse buttons have aria-expanded', () => {
            const cardPath = resolve(__dirname, '../Card.svelte');
            const source = readFileSync(cardPath, 'utf-8');

            // The subtask progress bar should have aria-expanded
            expect(source).toContain('aria-expanded={expanded}');
        });
    });

    describe('ToggleSwitch', () => {
        it('toggle switches have role="switch" and aria-checked', () => {
            const togglePath = resolve(__dirname, '../ToggleSwitch.svelte');
            const toggleSource = readFileSync(togglePath, 'utf-8');

            expect(toggleSource).toContain('role="switch"');
            expect(toggleSource).toContain('aria-checked={checked}');
        });

        it('toggle switches respond to click', () => {
            const togglePath = resolve(__dirname, '../ToggleSwitch.svelte');
            const toggleSource = readFileSync(togglePath, 'utf-8');

            // ToggleSwitch uses $bindable checked prop that toggles on click
            expect(toggleSource).toContain('onclick');
            expect(toggleSource).toContain('checked = !checked');
        });
    });

    describe('Column', () => {
        it('columns have ARIA roles for drag targets', () => {
            const { container } = render(Column, {
                columnId: 'backlog',
                tasks: [],
                projectId: 'test-project',
                projectPath: '/home/test',
                childrenMap: new Map(),
                expandedTasks: new Set(),
                onToggleExpanded: vi.fn(),
                onCreateChildTask: vi.fn(),
                onRefreshBoard: vi.fn(),
                onDragStart: vi.fn(),
                onDragOver: vi.fn(),
                onDragLeave: vi.fn(),
                onDrop: vi.fn(),
            });

            const column = container.querySelector('.column');
            expect(column).toBeTruthy();
            expect(column?.getAttribute('role')).toBe('list');
        });
    });

    describe('App-level landmarks', () => {
        it('main landmark is present with id for skip link', () => {
            const appPath = resolve(__dirname, '../App.svelte');
            const appContent = readFileSync(appPath, 'utf-8');

            expect(appContent).toContain('id="main-content"');
            expect(appContent).toContain('class="skip-link"');
            expect(appContent).toContain('href="#main-content"');
            expect(appContent).toContain('Skip to main content');
        });

        it('board has aria-label', () => {
            const appPath = resolve(__dirname, '../App.svelte');
            const appContent = readFileSync(appPath, 'utf-8');
            expect(appContent).toContain('aria-label="Kanban board"');
        });
    });
});
