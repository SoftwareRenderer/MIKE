/**
 * Which names the explorer opens as an archive. The server decides what an archive is, from its bytes
 * (`backend/archive`); this only picks the viewer — including for formats the server will refuse, since
 * its own sentence beats the editor drawing compressed bytes as text.
 */

import { baseName } from './path';

/** tar, gzip, bzip2 and zip are what the server reads; the rest are refused by name. */
const ARCHIVE_SUFFIXES = [
    '.tar', '.tar.gz', '.tgz', '.tar.bz2', '.tbz2', '.tbz', '.zip',
    '.tar.zst', '.tzst', '.tar.xz', '.txz', '.lzma', '.xz', '.7z', '.rar',
];

/** A name that carries a suffix is not the suffix itself: `.tar.gz` is a dotfile, as `fileSuffix` rules. */
function endsWithSuffix(name: string, suffix: string): boolean {
    return name.length > suffix.length && name.endsWith(suffix);
}

/** True when the file view opens the archive listing rather than the editor. */
export function isArchiveViewPath(path: string): boolean {
    const name = baseName(path).toLowerCase();
    return ARCHIVE_SUFFIXES.some(suffix => endsWithSuffix(name, suffix));
}
