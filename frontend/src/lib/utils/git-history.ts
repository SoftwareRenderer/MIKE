/**
 * Layout utilities for the git History list (commit rows in BranchGitGraph).
 *
 * The History panel lives in the resizable Explorer left column (default 250px,
 * min 180px; the column's 1px border means row width = panel width − 1), so
 * rows degrade in density as the panel narrows:
 *
 *   normal  (row width >= 245)  full layout — origin text badge, author, date
 *   compact (row width >= 205)  origin badge collapses to icon-only
 *   tight   (row width >= 180)  date hidden
 *   tighest (row width <  180)  author hidden
 *
 * The default panel (row width 249) stays at `normal` — today's layout — and
 * the minimum panel (row width 179) degrades to `tighest`.
 *
 * Fields hidden by the density are always present in the row tooltip
 * (buildCommitTooltip) so no information is lost.
 */
import type { GitCommit } from '$lib/types';

export type HistoryDensity = 'normal' | 'compact' | 'tight' | 'tighest';

/** Row-width breakpoints (px) for each density step-down. */
export const HISTORY_DENSITY_BREAKPOINTS = {
    /** Below this row width the origin badge becomes icon-only. */
    compact: 245,
    /** Below this row width the date is hidden. */
    tight: 205,
    /** Below this row width the author is hidden. */
    tighest: 180,
} as const;

/** Resolve the layout density for a commit-row width in px. */
export function getHistoryDensity(width: number): HistoryDensity {
    if (width >= HISTORY_DENSITY_BREAKPOINTS.compact) return 'normal';
    if (width >= HISTORY_DENSITY_BREAKPOINTS.tight) return 'compact';
    if (width >= HISTORY_DENSITY_BREAKPOINTS.tighest) return 'tight';
    return 'tighest';
}

export interface CommitTooltipContext {
    /** Display branch for the commit (current branch preferred), if any. */
    primaryBranch: string | null;
    /** Whether the commit is the origin tip of the current branch. */
    isOriginTip: boolean;
    /** Human-readable full date (e.g. "2024-05-04 14:30:00"), if parseable. */
    dateFormatted: string;
}

/**
 * Build the multi-line row tooltip for a commit.
 *
 * Returns `undefined` for `normal` density (per-field span titles are used
 * there). For narrower densities the tooltip carries every field the row may
 * hide — subject, author, full date, branch, origin marker, short hash.
 */
export function buildCommitTooltip(commit: GitCommit, density: HistoryDensity, ctx: CommitTooltipContext): string | undefined {
    if (density === 'normal') return undefined;

    const lines: string[] = [];
    if (commit.subject) lines.push(commit.subject);

    const who = commit.author;
    const when = ctx.dateFormatted || commit.date;
    if (who || when) lines.push([who, when].filter(Boolean).join(' · '));

    const where = [
        ctx.primaryBranch ?? '',
        ctx.isOriginTip ? 'origin' : '',
    ].filter(Boolean).join(' · ');
    if (where) lines.push(where);

    if (commit.short) lines.push(commit.short);

    return lines.length > 0 ? lines.join('\n') : undefined;
}
