import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const editFormPath = resolve(__dirname, '../Chat/EditForm.svelte');
const source = readFileSync(editFormPath, 'utf-8');

describe('EditForm rendering', () => {
    it('shows textarea for editing content', () => {
        expect(source).toContain('textareaEl');
        expect(source).toContain('editContent');
    });

    it('supports @ file mentions', () => {
        expect(source).toContain('FileMentionDropdown');
        expect(source).toContain('useFileMention');
    });

    it('delegates mention search with debounce', () => {
        // The state machine (debounce + /api/fs/search) lives in the shared composable
        const composable = readFileSync(resolve(__dirname, '../../composables/use-file-mention.svelte.ts'), 'utf-8');
        expect(source).toContain('mention.handleInput()');
        expect(composable).toContain('MENTION_DEBOUNCE_MS');
        expect(composable).toContain('/api/fs/search');
    });

    it('supports image attachments', () => {
        expect(source).toContain('editAttachments');
        expect(source).toContain('editFileInput');
    });

    it('enables submit when content or attachments exist', () => {
        expect(source).toContain('canSubmit');
        expect(source).toContain('editContent.trim().length > 0');
        expect(source).toContain('editAttachments.length > 0');
    });

    it('calls onSubmit with content and attachments', () => {
        expect(source).toContain('onSubmit');
        expect(source).toContain('editContent');
        expect(source).toContain('editAttachments');
    });

    it('calls onCancel when cancel is pressed', () => {
        expect(source).toContain('onCancel');
    });

    it('disables submit while submitting', () => {
        expect(source).toContain('isSubmitting');
    });

    it('handles keyboard navigation for mentions', () => {
        const composable = readFileSync(resolve(__dirname, '../../composables/use-file-mention.svelte.ts'), 'utf-8');
        expect(source).toContain('mention.handleKeydown(e)');
        expect(composable).toContain('insertMention');
        expect(composable).toContain('activeIndex');
    });

    it('initializes edit content from props', () => {
        expect(source).toContain('$effect(() => { editContent = content; })');
    });
});
