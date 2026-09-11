/** Shared tool args formatting for chat messages */
import { t } from '$lib/i18n';

/** A renderable edit_file edit pair — both sides guaranteed to be strings. */
export interface EditDiffEntry {
    oldText: string;
    newText: string;
}

/**
 * Coerce edit_file `edits` args into string pairs safe to render.
 *
 * Args arrive from the model (or an agent adapter), so they can be partial
 * (a side still streaming), truncated, or malformed (hallucinated key names,
 * non-object entries). The edit diff renders them straight from the live
 * stream and from persisted history, so it must never throw: an entry missing
 * one side renders as a pure insertion/deletion, and an entry with neither
 * side renderable is dropped.
 */
export function normalizeEdits(edits: unknown): EditDiffEntry[] {
    if (!Array.isArray(edits)) return [];
    const out: EditDiffEntry[] = [];
    for (const raw of edits) {
        if (!raw || typeof raw !== 'object') continue;
        const entry = raw as Record<string, unknown>;
        const oldText = typeof entry.oldText === 'string' ? entry.oldText : null;
        const newText = typeof entry.newText === 'string' ? entry.newText : null;
        if (oldText === null && newText === null) continue;
        out.push({ oldText: oldText ?? '', newText: newText ?? '' });
    }
    return out;
}

/**
 * The file a tool names, as far as its arguments have arrived.
 *
 * `eventPath` leads: it is the path the server read out of the arguments as they streamed, so it is
 * known before the client has collected them. The arguments follow, whole first and then as a
 * partial JSON body — the `path` key is generated long before the object around it parses.
 */
export function streamedPath(
    args: Record<string, unknown> | null | undefined,
    rawArgs?: string,
    eventPath?: string,
): string {
    if (eventPath) return eventPath;
    const fromArgs = args?.path;
    if (typeof fromArgs === 'string' && fromArgs) return fromArgs;
    if (!rawArgs) return '';
    try {
        const parsed = JSON.parse(rawArgs) as { path?: unknown };
        return typeof parsed.path === 'string' ? parsed.path : '';
    } catch {
        const m = rawArgs.match(/"path"\s*:\s*"((?:[^"\\]|\\.)*)/);
        return m ? m[1].replace(/\\"/g, '"') : '';
    }
}

/**
 * True once the streamed arguments are the whole object. While the model is still generating
 * them the JSON does not parse, which is how a card tells generation from execution.
 */
export function rawArgsComplete(rawArgs?: string): boolean {
    if (!rawArgs) return false;
    try {
        JSON.parse(rawArgs);
        return true;
    } catch {
        return false;
    }
}

/** A path as the chat displays it: rooted at the project. */
function dotPath(p: string): string {
    return p && !p.startsWith('./') ? `./${p}` : p;
}

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
            // `$ ` is the shell prompt marker shared with the command branch, not copy.
            return cmd ? `$ ${cmd}` : t('chat.args.bashBuilding');
        }
        case 'read_file': {
            const p = args.path as string;
            if (!p) return '';
            return dotPath(p);
        }
        case 'write_file': {
            const p = streamedPath(args, rawArgs);
            if (!p) return '';
            return dotPath(p);
        }
        case 'edit_file': {
            const p = streamedPath(args, rawArgs);
            let edits: unknown = args.edits;
            // Edits are countable only once the whole array has streamed; a partial body is left
            // alone, so the path shows on its own while the edits are still arriving.
            if (!Array.isArray(edits) && rawArgs) {
                try {
                    edits = (JSON.parse(rawArgs) as { edits?: unknown }).edits;
                } catch {
                    /* still streaming */
                }
            }
            if (!p) return '';
            const editCount = normalizeEdits(edits).length;
            const pathDisplay = dotPath(p);
            if (editCount <= 0) return pathDisplay;
            // The path is data; only the parenthetical is copy, so it is translated whole.
            return `${pathDisplay} (${t('chat.args.editCount', { count: editCount })})`;
        }
        case 'request_review':
            return args.summary as string || t('chat.args.taskComplete');
        case 'create_task': {
            const title = args.title as string;
            return title || t('chat.args.createTask');
        }
        case 'edit_task': {
            const title = args.title as string;
            const task_id = args.task_id as string;
            if (title) return title;
            return task_id || t('chat.args.editTask');
        }
        case 'ask_question':
            return args.question as string || t('chat.args.question');
        case 'view_tasks':
            return t('chat.args.viewTasks');
        case 'lsp_hover': {
            const sym = args.symbol as string;
            const path = args.path as string || t('chat.args.fileWord');
            return sym ? t('chat.args.lspHover', { sym, path }) : t('chat.args.lspHoverIn', { path });
        }
        case 'lsp_references': {
            const sym = args.symbol as string;
            const path = args.path as string || t('chat.args.fileWord');
            return sym ? t('chat.args.lspReferences', { sym, path }) : t('chat.args.lspReferencesIn', { path });
        }
        case 'lsp_definition': {
            const sym = args.symbol as string;
            const path = args.path as string || t('chat.args.fileWord');
            return sym ? t('chat.args.lspDefinition', { sym, path }) : t('chat.args.lspDefinitionIn', { path });
        }
        case 'lsp_rename': {
            const sym = args.symbol as string;
            const newName = (args as Record<string, unknown>).new_name as string;
            const path = args.path as string || t('chat.args.fileWord');
            if (sym && newName) return t('chat.args.lspRenameTo', { sym, newName, path });
            return sym ? t('chat.args.lspRename', { sym, path }) : t('chat.args.lspRenameIn', { path });
        }
        default:
            return JSON.stringify(args, null, 2);
    }
}

/** Tools that should not show their args inline (shown in result instead) */
export const HIDE_ARGS_FOR_TOOLS = new Set(['create_task', 'edit_task', 'view_tasks', 'request_review', 'ask_question']);

/** Tools that can run in a sandbox */
export const SANDBOXABLE_TOOLS = new Set(['read_file', 'write_file', 'edit_file', 'bash']);

/** Max length for truncated args display */
export const ARGS_MAX_LEN = 50;

/** Max lines for edit diff display */
export const EDIT_DIFF_MAX_LINES = 20;
