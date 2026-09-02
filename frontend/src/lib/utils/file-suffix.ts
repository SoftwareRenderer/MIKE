/**
 * The one parser for the suffix a file type is matched on — mirrors `extensions.fileSuffix` on the
 * Go side, including `dot <= 0`: `.gitignore` is a dotfile, not a `.gitignore` file.
 */

/** Lowercase basename of a path. */
export function fileBase(filePath: string): string {
  return filePath.split('/').pop()?.toLowerCase() || '';
}

/** Bare lowercase suffix with no dot — `ts` for `src/App.TSX`, `''` for `Makefile` and `.gitignore`. */
export function fileSuffix(filePath: string): string {
  const base = fileBase(filePath);
  const dot = base.lastIndexOf('.');
  return dot <= 0 ? '' : base.slice(dot + 1);
}
