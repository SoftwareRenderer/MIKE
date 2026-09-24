/**
 * The one parser for the suffix a file type is matched on — mirrors `extensions.fileSuffix` on the
 * Go side, including `dot <= 0`: `.gitignore` is a dotfile, not a `.gitignore` file.
 */

import { baseName } from './path';

/** The four extensions vim writes for a swap or undo file, as `git.ShouldIgnoreFile` lists them. */
const VIM_SWAP_SUFFIXES = ['.swp', '.swo', '.ssn', '.uno'];

/** Lowercase basename of a path. */
export function fileBase(filePath: string): string {
  return baseName(filePath).toLowerCase();
}

/**
 * A vim swap or undo file: a dotfile ending in one of the four extensions vim writes. Mirrors the vim
 * clause of `git.ShouldIgnoreFile` (`backend/git/worktree.go`) — that list is the set, so a `.swo` the
 * server hides cannot stay visible in the panel.
 */
export function isVimSwapFile(path: string): boolean {
  const base = baseName(path);
  return base.startsWith('.') && VIM_SWAP_SUFFIXES.some(suffix => base.endsWith(suffix));
}

/** Bare lowercase suffix with no dot — `ts` for `src/App.TSX`, `''` for `Makefile` and `.gitignore`. */
export function fileSuffix(filePath: string): string {
  const base = fileBase(filePath);
  const dot = base.lastIndexOf('.');
  return dot <= 0 ? '' : base.slice(dot + 1);
}
