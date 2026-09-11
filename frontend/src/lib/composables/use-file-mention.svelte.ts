/**
 * Shared @-file-mention state machine for the three chat-style inputs
 * (ChatForm, EditForm, CreateTaskModal).
 *
 * Owns: trigger detection on input, debounced `/api/fs/search`, dropdown
 * position/active-row state, insertion into the bound textarea, and the
 * ArrowUp/ArrowDown/Enter/Escape navigation block the three forms used to
 * each carry a private copy of.
 */
import { detectMentionAtCursor, insertMention, getInputRect } from '$lib/utils/file-mention';

const MENTION_DEBOUNCE_MS = 150;

export interface FileMentionOptions {
    /** Search is skipped entirely while the project is unknown (e.g. during create). */
    getProjectId: () => string | undefined;
    /** Optional task scope for the search (worktree-scoped mentions). */
    getTaskId?: () => string | undefined;
    getValue: () => string;
    setValue: (value: string) => void;
    getTextarea: () => HTMLTextAreaElement | null | undefined;
    /**
     * Element the dropdown aligns to. Defaults to the textarea itself;
     * ChatForm passes the styled input wrapper (the textarea's parent).
     */
    getPositionSource?: () => HTMLElement | null | undefined;
}

export class FileMention {
    trigger = $state<{ atIndex: number; query: string } | null>(null);
    results = $state<Array<{ path: string }>>([]);
    position = $state<{ top: number; bottom: number; left: number; width: number } | null>(null);
    activeIndex = $state(0);

    private timer: ReturnType<typeof setTimeout> | null = null;
    private opts: FileMentionOptions;

    constructor(opts: FileMentionOptions) {
        this.opts = opts;
    }

    /** The suggestion list is on screen. It holds focus while a row is tapped, then hands it back. */
    get isOpen(): boolean {
        return this.position !== null && this.results.length > 0;
    }

    /** Call from the textarea's `oninput`. Detects a trigger and searches. */
    handleInput() {
        const textarea = this.opts.getTextarea();
        if (!textarea) return;
        const cursorPos = textarea.selectionStart ?? 0;
        const trigger = detectMentionAtCursor(this.opts.getValue(), cursorPos);

        if (trigger) {
            this.trigger = trigger;
            this.activeIndex = 0;
            const container = this.opts.getPositionSource?.() ?? textarea;
            this.position = getInputRect(container);

            // Debounced search
            if (this.timer) clearTimeout(this.timer);
            this.timer = setTimeout(() => {
                this.search(trigger.query);
            }, MENTION_DEBOUNCE_MS);
        } else {
            this.close();
        }
    }

    /** ArrowUp/ArrowDown/Enter/Escape while the dropdown is open. Returns true if consumed. */
    handleKeydown(e: KeyboardEvent): boolean {
        if (!this.trigger || this.results.length === 0) return false;
        if (e.key === 'ArrowDown') {
            e.preventDefault();
            this.activeIndex = (this.activeIndex + 1) % this.results.length;
            return true;
        }
        if (e.key === 'ArrowUp') {
            e.preventDefault();
            this.activeIndex = (this.activeIndex - 1 + this.results.length) % this.results.length;
            return true;
        }
        if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault();
            if (this.activeIndex >= 0 && this.activeIndex < this.results.length) {
                this.select(this.results[this.activeIndex].path);
            }
            return true;
        }
        if (e.key === 'Escape') {
            e.preventDefault();
            e.stopPropagation();
            this.close();
            return true;
        }
        return false;
    }

    /** Pick a path from the dropdown (mouse click or Enter). */
    select(path: string) {
        const textarea = this.opts.getTextarea();
        if (!this.trigger || !textarea) return;
        const { value: newValue, cursorPos } = insertMention(this.opts.getValue(), this.trigger.atIndex, this.trigger.query, path);
        this.opts.setValue(newValue);
        this.close();
        // Restore cursor after reactive update
        requestAnimationFrame(() => {
            const el = this.opts.getTextarea();
            if (el) {
                el.focus();
                el.setSelectionRange(cursorPos, cursorPos);
            }
        });
    }

    /** Dropdown's own close affordance: close and return focus to the input. */
    handleClose() {
        this.close();
        this.opts.getTextarea()?.focus();
    }

    close() {
        this.trigger = null;
        this.results = [];
        this.position = null;
        this.activeIndex = 0;
        if (this.timer) {
            clearTimeout(this.timer);
            this.timer = null;
        }
    }

    private async search(query: string) {
        const projectId = this.opts.getProjectId();
        if (!projectId) return;
        const params = new URLSearchParams({ q: query, project: projectId });
        const taskId = this.opts.getTaskId?.();
        if (taskId) params.set('task', taskId);
        try {
            const res = await fetch(`/api/fs/search?${params.toString()}`);
            if (!res.ok) return;
            const data = await res.json();
            this.results = data.results || [];
        } catch {
            this.results = [];
        }
    }
}

export function useFileMention(opts: FileMentionOptions): FileMention {
    return new FileMention(opts);
}
