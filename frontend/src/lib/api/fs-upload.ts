import { ensureCsrfToken } from './helpers';
import { t } from '$lib/i18n';

/**
 * One dropped file, written into a project's directory tree by `POST /api/fs/upload`.
 *
 * XMLHttpRequest rather than fetch because the drop zone promises two things fetch
 * cannot deliver: byte-level *upload* progress (fetch has no upload stream to report
 * on) and an abort that stops the transfer, not only the response.
 */

type UploadStatus = 'ok' | 'conflict' | 'cancelled' | 'error';

export interface UploadResult {
    status: UploadStatus;
    /** Server message for an `error` — the 409's message is deliberately not surfaced, the UI asks its own question. */
    message?: string;
}

export interface FsUploadRequest {
    projectId: string;
    /** Task whose worktree receives the file; omit for the project's own checkout. */
    taskId?: string;
    /** Directory the file lands in, in the form the file browser shows it (a container path when the project is sandboxed). */
    dir: string;
    /** Path inside that directory — `logo.png` for one file, `assets/img/logo.png` for a folder drop. */
    relPath: string;
    file: File;
    /** Set only after the user confirmed replacing an existing file. */
    overwrite?: boolean;
    /** Bytes transferred so far; the caller totals the run itself. */
    onProgress?: (loaded: number) => void;
    signal?: AbortSignal;
}

function errorMessage(body: string, fallback: string): string {
    try {
        const parsed = JSON.parse(body);
        return typeof parsed?.error === 'string' && parsed.error ? parsed.error : fallback;
    } catch {
        return fallback;
    }
}

export async function uploadFsFile(req: FsUploadRequest): Promise<UploadResult> {
    if (req.signal?.aborted) return { status: 'cancelled' };
    const csrfToken = await ensureCsrfToken();
    // ensureCsrfToken reaches the network on a cold session; a cancel during it must
    // not be followed by an upload nobody asked for.
    if (req.signal?.aborted) return { status: 'cancelled' };

    return new Promise<UploadResult>((resolve) => {
        const form = new FormData();
        form.set('path', req.dir);
        form.set('relpath', req.relPath);
        if (req.overwrite) form.set('overwrite', '1');
        form.set('file', req.file, req.file.name);

        const params = new URLSearchParams({ project: req.projectId });
        if (req.taskId) params.set('task', req.taskId);

        const xhr = new XMLHttpRequest();
        xhr.open('POST', `/api/fs/upload?${params.toString()}`);
        xhr.withCredentials = true;
        if (csrfToken) xhr.setRequestHeader('X-CSRF-Token', csrfToken);

        const total = req.file.size;
        let loaded = 0;
        xhr.upload.onprogress = (event) => {
            loaded = event.loaded;
            req.onProgress?.(loaded);
        };
        const settle = (result: UploadResult) => {
            req.signal?.removeEventListener('abort', onAbort);
            resolve(result);
        };
        const onAbort = () => {
            xhr.abort();
            settle({ status: 'cancelled' });
        };
        req.signal?.addEventListener('abort', onAbort);

        xhr.onload = () => {
            if (xhr.status >= 200 && xhr.status < 300) {
                settle({ status: 'ok' });
            } else if (xhr.status === 409) {
                settle({ status: 'conflict' });
            } else {
                settle({ status: 'error', message: errorMessage(xhr.responseText, t('explorer.shared.dropErrHttp', { status: xhr.status })) });
            }
        };
        xhr.onerror = () => {
            // A connection that dies mid-transfer: report how far it got.
            settle({ status: 'error', message: t('explorer.shared.dropErrNetwork', { loaded, total }) });
        };
        xhr.onabort = () => settle({ status: 'cancelled' });

        xhr.send(form);
    });
}
