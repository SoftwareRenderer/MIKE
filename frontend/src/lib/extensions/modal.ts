/**
 * `ui.modal` — app-level modal for sandboxed UI extensions. An extension's own iframe is
 * invisible once it stops overlaying the editor, so anything it must show after handing
 * control back is rendered by the host (plain DOM, because capability handlers run outside
 * any component). Stacked modals close inside-out; the promise resolves when the user
 * dismisses.
 */

import { t } from '$lib/i18n';
import { Z } from '$lib/constants/z-layers';

export interface ExtensionModalOptions {
    /** Dialog heading. Omitted → no header title (message only). */
    title?: string;
    /** Body text (required — enforced by the capability args schema). */
    message: string;
    /** Confirm button label. Defaults to "OK". */
    confirmLabel?: string;
    /** Attributed in the footer ("Shown by <name>"). */
    extensionName?: string;
}

/**
 * How many extension modals may be open at once. The dialog is blocking and
 * user-dismissed, so an unbounded `ui.modal` loop would lock the UI; a granted
 * capability should never be able to do that.
 *
 * @knipignore The budget test opens dialogs up to this cap and one past it.
 */
export const MAX_OPEN_MODALS = 3;

let zCounter = 0;
/** Open overlays in open order; the last entry is the topmost. */
const openOverlays: HTMLDivElement[] = [];

export function showExtensionModal(opts: ExtensionModalOptions): Promise<void> {
    if (openOverlays.length >= MAX_OPEN_MODALS) {
        return Promise.reject(
            new Error(`too many open extension dialogs (max ${MAX_OPEN_MODALS})`)
        );
    }
    const message = String(opts.message || '');
    const title = String(opts.title || '');
    const confirmLabel = String(opts.confirmLabel || t('host.ok'));
    const extensionName = String(opts.extensionName || '');

    // The stacked-dialog tier, plus one per open dialog so a nested `ui.modal` paints over its parent.
    const z = Z.modalStacked + ++zCounter;
    const prevFocus = document.activeElement as HTMLElement | null;

    // ── overlay ────────────────────────────────────────────────
    const overlay = document.createElement('div');
    overlay.setAttribute('data-ext-modal', '');
    overlay.setAttribute('role', 'presentation');
    overlay.style.cssText =
        `position:fixed;inset:0;z-index:${z};background:rgba(0,0,0,0.6);` +
        'display:flex;align-items:center;justify-content:center;';

    // ── dialog ─────────────────────────────────────────────────
    const titleId = title ? `ext-modal-title-${z}` : undefined;
    const dialog = document.createElement('div');
    dialog.setAttribute('role', 'dialog');
    dialog.setAttribute('aria-modal', 'true');
    if (titleId) dialog.setAttribute('aria-labelledby', titleId);
    dialog.style.cssText =
        'background:var(--color-surface-1);border:1px solid var(--color-border);' +
        'border-radius:var(--radius-lg,8px);width:min(480px,90vw);max-height:90vh;' +
        'display:flex;flex-direction:column;overflow:hidden;';

    if (title) {
        const header = document.createElement('div');
        header.style.cssText =
            'display:flex;align-items:center;justify-content:space-between;' +
            'padding:16px 20px;border-bottom:1px solid var(--color-border);flex-shrink:0;';
        const h2 = document.createElement('h2');
        if (titleId) h2.id = titleId;
        h2.textContent = title;
        h2.style.cssText = 'margin:0;font-size:1rem;font-weight:600;color:var(--color-text-primary);';
        header.appendChild(h2);
        dialog.appendChild(header);
    }

    const body = document.createElement('div');
    body.style.cssText =
        'padding:16px 20px;overflow-y:auto;flex:1;font-size:0.875rem;line-height:1.5;' +
        'color:var(--color-text-primary);white-space:pre-wrap;word-break:break-word;';
    body.textContent = message;
    dialog.appendChild(body);

    // ── footer (attribution + confirm) ─────────────────────────
    const footer = document.createElement('div');
    footer.style.cssText =
        'display:flex;align-items:center;justify-content:space-between;gap:8px;' +
        'padding:12px 20px;border-top:1px solid var(--color-border);flex-shrink:0;';
    if (extensionName) {
        const by = document.createElement('span');
        by.textContent = t('host.shownBy', { name: extensionName });
        by.style.cssText = 'font-size:0.75rem;color:var(--color-text-tertiary);';
        footer.appendChild(by);
    }
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.textContent = confirmLabel;
    btn.style.cssText =
        'background:var(--color-accent-dark);color:white;border:none;' +
        'border-radius:var(--radius-md,6px);padding:8px 16px;font-size:0.875rem;' +
        'font-weight:500;font-family:inherit;cursor:pointer;';
    footer.appendChild(btn);
    dialog.appendChild(footer);

    overlay.appendChild(dialog);

    // ── lifecycle ──────────────────────────────────────────────
    let resolve: () => void = () => {};
    const promise = new Promise<void>((r) => { resolve = r; });
    let closed = false;
    const close = () => {
        if (closed) return;
        closed = true;
        openOverlays.splice(openOverlays.indexOf(overlay), 1);
        document.removeEventListener('keydown', onKeydown);
        overlay.remove();
        if (prevFocus && document.contains(prevFocus)) prevFocus.focus();
        resolve();
    };
    const onKeydown = (e: KeyboardEvent) => {
        // Escape acts on the topmost modal only (stacked modals close inside-out).
        if (e.key === 'Escape' && openOverlays[openOverlays.length - 1] === overlay) {
            e.stopPropagation();
            close();
        }
    };
    const onOverlayClick = (e: MouseEvent) => {
        if (e.target === overlay && openOverlays[openOverlays.length - 1] === overlay) close();
    };

    btn.addEventListener('click', close);
    overlay.addEventListener('click', onOverlayClick);
    document.addEventListener('keydown', onKeydown);

    document.body.appendChild(overlay);
    openOverlays.push(overlay);
    btn.focus();

    return promise;
}
