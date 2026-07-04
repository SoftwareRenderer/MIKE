/** Shared tool args formatting for chat messages */

export function formatToolArgs(toolName: string, args: Record<string, unknown> | null, rawArgs?: string): string {
    if (!args) return '';
    switch (toolName) {
        case 'bash': {
            let cmd = args.command as string;
            // During streaming, args may be empty but rawArgs has the JSON.
            // Try full parse first, then regex fallback for partial JSON.
            if (!cmd && rawArgs) {
                try {
                    const parsed = JSON.parse(rawArgs);
                    cmd = parsed.command as string;
                } catch {
                    // Partial JSON (e.g. "{\"command\":\"apt ge") — extract via regex
                    const m = rawArgs.match(/"command"\s*:\s*"((?:[^"\\]|\\.)*)/);
                    if (m) cmd = m[1].replace(/\\"/g, '"');
                }
            }
            return cmd ? `$ ${cmd}` : '$ (building command...)';
        }
        case 'read_file': {
            const p = args.path as string;
            if (!p) return '';
            return p.startsWith('./') ? p : `./${p}`;
        }
        case 'write_file': {
            const p = args.path as string;
            if (!p) return '';
            return p.startsWith('./') ? p : `./${p}`;
        }
        case 'edit_file': {
            const p = args.path as string;
            if (!p) return '(editing...)';
            const edits = args.edits as Array<{ oldText: string; newText: string }>;
            const editCount = edits ? edits.length : 0;
            const pathDisplay = p.startsWith('./') ? p : `./${p}`;
            if (editCount <= 0) return pathDisplay;
            const suffix = editCount === 1 ? '1 edit' : `${editCount} edits`;
            return `${pathDisplay} (${suffix})`;
        }
        case 'task_complete':
            return args.summary as string || '(task complete)';
        case 'create_task': {
            const title = args.title as string;
            return title || '(create task)';
        }
        default:
            return JSON.stringify(args, null, 2);
    }
}

/** Tools that should not show their args inline (shown in result instead) */
export const HIDE_ARGS_FOR_TOOLS = new Set(['create_task', 'task_complete']);

/** Tools that can run in a sandbox */
export const SANDBOXABLE_TOOLS = new Set(['read_file', 'write_file', 'edit_file', 'bash']);

/** Max length for truncated args display */
export const ARGS_MAX_LEN = 50;

/** Max lines for edit diff display */
export const EDIT_DIFF_MAX_LINES = 20;
