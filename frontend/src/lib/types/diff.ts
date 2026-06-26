/** Variant of a diff row: unchanged context, added line, or removed line. */
export type DiffVariant = 'context' | 'added' | 'removed';

/** A single line in a parsed diff. */
export interface DiffRow {
    /** Unique key for rendering. */
    key: string;
    /** Line number in the new file (or old file for removed lines), or null for headers. */
    lineNumber: number | null;
    /** Type of this line. */
    variant: DiffVariant;
    /** The text content (without the +/-/ prefix). */
    text: string;
}

/** A node in the file tree: either a directory or a file leaf. */
export interface FileTreeNode {
    /** Display name (last path segment). */
    name: string;
    /** Full path from root. */
    path: string;
    /** Whether this is a directory or a file. */
    type: 'file' | 'directory';
    /** Child nodes (directories only). */
    children: FileTreeNode[];
    /** Aggregated added lines. */
    added: number;
    /** Aggregated removed lines. */
    removed: number;
    /** The DiffFile for leaf nodes. */
    file?: DiffFile;
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
