/** A single line in a parsed diff. */
export interface DiffRow {
    /** Unique key for rendering. */
    key: string;
    /** Line number in the new file (or old file for removed lines), or null for headers. */
    lineNumber: number | null;
    /** Type of this line. */
    variant: 'context' | 'added' | 'removed';
    /** The text content (without the +/-/ prefix). */
    text: string;
}

/** A parsed file group from a unified diff. */
export interface DiffFile {
    /** File path (e.g., "src/lib/utils.ts"). */
    path: string;
    /** Diff rows for this file. Empty for binary/large files. */
    rows: DiffRow[];
    /** Count of added lines. */
    added: number;
    /** Count of removed lines. */
    removed: number;
    /** File size in bytes, set when file is binary or too large to diff. */
    fileSize?: number;
    /** True if this file was truncated (has more changes not shown). */
    truncated?: boolean;
    /** Original added count before truncation (for display in header). */
    origAdded?: number;
    /** Original removed count before truncation (for display in header). */
    origRemoved?: number;
}
