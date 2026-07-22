/** Inline word-level diff segment for highlighting changes within a line. */
export interface InlineDiffSegment {
    key: string;
    text: string;
    tone: 'added' | 'removed' | 'context';
}

/** A single line in a parsed diff. */
export interface DiffRow {
    /** Unique key for rendering (optional, index-based fallback). */
    key?: string;
    /** Line number in the new file (or old file for removed lines), or null for headers. */
    lineNumber: number | null;
    /** Type of this line. */
    variant: 'context' | 'added' | 'removed';
    /** The text content (without the +/-/prefix). Omitted when segments present. */
    text?: string;
    /** Word-level inline diff segments (set for paired removed/added rows). */
    segments?: InlineDiffSegment[];
}

/** A diff row as returned by the API (variant/tone are strings, not strict literals). */
export interface ApiDiffRow {
    key?: string;
    lineNumber: number | null;
    variant: string;
    text?: string;
    segments?: Array<{ key: string; text: string; tone: string }>;
}

/** A parsed file group from a unified diff. */
export interface DiffFile {
    /** File path (e.g., "src/lib/utils.ts"). */
    path: string;
    /** Original path for renames (e.g., "old-name.png" when path is "new-name.png"). */
    origPath?: string;
    /** Diff rows for this file. Empty for binary/large files. */
    rows: DiffRow[];
    /** Diff rows committed to branch since baseRef. */
    committedRows?: DiffRow[];
    /** Count of added lines. */
    added: number;
    /** Count of removed lines. */
    removed: number;
    /** File size in bytes, set when file is binary or too large to diff. */
    fileSize?: number;
    /** True if git detected this as a binary file. */
    isBinary?: boolean;
    /** True if this file was truncated (has more changes not shown). */
    truncated?: boolean;
    /** Original added count before truncation (for display in header). */
    origAdded?: number;
    /** Original removed count before truncation (for display in header). */
    origRemoved?: number;
    /** True if this file is staged in the git index. */
    staged?: boolean;
    /** True if this file was deleted (git diff shows +++ /dev/null). */
    isDeleted?: boolean;
    /** True if this file is newly added (git diff shows --- /dev/null). */
    isNewFile?: boolean;
}
