import { fileSuffix } from './file-suffix';

/**
 * Mirrors `rawImageTypes` in `backend/server/handler_fs_raw.go` case for case: a name offered here but
 * refused there is a button that only ever fails. SVG is on neither list — it is markup.
 */
const DISPLAYABLE_IMAGE_SUFFIXES = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'ico', 'avif']);

/** True when a path names an image the browser can render from bytes. */
export function isDisplayableImage(path: string): boolean {
    return DISPLAYABLE_IMAGE_SUFFIXES.has(fileSuffix(path));
}

/**
 * One image for a file the change created, two for a file it changed, none for one it removed — a
 * deleted image is in no tree the panel can read.
 */
export function imageSides(file: { isNewFile?: boolean; isDeleted?: boolean }): 'single' | 'compare' | null {
    if (file.isDeleted) return null;
    if (file.isNewFile) return 'single';
    return 'compare';
}
