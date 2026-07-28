import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const taskHeaderPath = resolve(__dirname, '../CardDetail/TaskHeader.svelte');
const source = readFileSync(taskHeaderPath, 'utf-8');

describe('TaskHeader rendering', () => {
    it('shows worktree display path', () => {
        expect(source).toContain('getWorktreeDisplayPath');
        expect(source).toContain('workspaceDir');
    });

    it('shows sandbox status indicator', () => {
        expect(source).toContain('sandboxEnabled');
        expect(source).toContain('shield');
    });

    it('shows status badge', () => {
        expect(source).toContain('badge');
        expect(source).toContain('badge.text');
        expect(source).toContain('badge.color');
    });

    it('shows close/back button', () => {
        expect(source).toContain('onClose');
        expect(source).toContain('btn-back');
    });

    it('shows terminal button', () => {
        expect(source).toContain('onOpenTerminal');
    });

    it('shows delete button', () => {
        expect(source).toContain('onDelete');
    });

    it('terminal button is always enabled (init happens in CardDetail)', () => {
        expect(source).toContain('onOpenTerminal');
        // worktreeExists prop removed — init is handled by CardDetail wrapper
        expect(source).not.toContain('worktreeExists');
    });

    it('renders header layout', () => {
        expect(source).toContain('header');
        expect(source).toContain('header-left');
        expect(source).toContain('header-actions');
    });
});
