/**
 * Announces tasks that enter Review, in every project and not only the one on screen. Two frames carry
 * the news and either can arrive first, so both feed one ledger and the ledger decides who hears it.
 */
import type { Task } from '$lib/types';
import { getBoard } from '$lib/api/projects';
import { appStore, uiStore } from '$lib/stores';
import { t } from '$lib/i18n';
import { projectBoardHref, taskViewHref } from '$lib/utils/app-link';
import { showBrowserNotification, dismissBrowserNotification, type BrowserNotification } from '$lib/utils/notifications';
import {
    dismissedTags,
    notableArrivals,
    planReviewNotice,
    reviewArrivals,
    reviewBaseline,
    tasksInReview,
    type ArrivalGroup,
    type AttentionState,
    type ReviewBaseline,
    type ShownNotice,
} from '$lib/utils/review-notify';

/** How long a move this tab made itself counts as its own news. */
const SELF_MOVE_TTL_MS = 60_000;

/** One tag for the batch, so the newest news replaces the previous one instead of stacking under it. */
const BATCH_TAG = 'mike-review-batch';

interface ColumnRead {
    count: number;
    ids: string[];
}

class ReviewNotifier {
    private counts = new Map<string, number>();
    private columns = new Map<string, ColumnRead>();
    /** Per project, task id to the `moved_at` announced: the store restamps it on every column move,
     *  so a task that left Review and came back is a different move and news again. */
    private announced = new Map<string, Map<string, string>>();
    /** What this client put up, by tag, so the user can be caught up with later. */
    private shown = new Map<string, ShownNotice>();
    private openBoard: { projectId: string; tasks: Task[] } | null = null;
    private selfMoved = new Map<string, number>();
    private fetching = new Set<string>();
    /** Arrivals collected but not announced yet, per project. */
    private pending = new Map<string, Task[]>();
    /** Board reads in flight. A frame that raised several projects started several reads, and the burst is
     *  not over until the last one says what its column holds. */
    private reads = 0;

    observeCounts(counts: Record<string, { review?: number } | undefined>): void {
        // Counted even while off, so the first frame after it comes on is a steady count rather than a
        // debt of notifications for changes already made.
        const enabled = uiStore.notifyOnReview;
        const seen = new Set<string>();
        for (const [projectId, entry] of Object.entries(counts || {})) {
            seen.add(projectId);
            const review = entry?.review ?? 0;
            const previous = this.counts.get(projectId);
            this.counts.set(projectId, review);
            if (!enabled || previous === undefined || review <= previous) continue;
            // The board on screen reports its own column exactly, from observeBoard.
            if (this.openBoard?.projectId === projectId) continue;
            void this.readProject(projectId, previous);
        }
        // A project missing from a frame has no column this client can still see.
        for (const projectId of [...this.counts.keys()]) {
            if (!seen.has(projectId)) {
                this.counts.delete(projectId);
                this.columns.delete(projectId);
                this.announced.delete(projectId);
                this.pending.delete(projectId);
                for (const [tag, notice] of this.shown) {
                    if (notice.projectId === projectId) this.shown.delete(tag);
                }
            }
        }
    }

    observeBoard(projectId: string, tasks: Task[]): void {
        // A board that just appeared — the first one, or the way back to a project — says what is there
        // now, so it seeds instead of announcing.
        const firstLook = this.openBoard === null || this.openBoard.projectId !== projectId;
        this.openBoard = { projectId, tasks };
        const snapshot = this.columns.get(projectId);
        this.applyColumn(projectId, tasksInReview(tasks), firstLook || !snapshot ? null : reviewBaseline(snapshot.count, snapshot.ids));
    }

    clearBoard(): void {
        this.openBoard = null;
    }

    /** A card this tab dragged into Review is not news. */
    noteSelfMove(taskId: string): void {
        this.selfMoved.set(taskId, Date.now());
    }

    /**
     * Take back what the user has now seen: the task they opened, or the Review column they are looking
     * at. Focus is not a reactive value, so it is read here rather than watched.
     */
    observeAttention(openTaskId: string | null, reviewProject: string | null): void {
        if (this.shown.size === 0) return;
        if (document.visibilityState !== 'visible' || !document.hasFocus()) return;
        for (const tag of dismissedTags([...this.shown.values()], openTaskId, reviewProject)) {
            this.shown.delete(tag);
            dismissBrowserNotification(tag);
        }
    }

    private async readProject(projectId: string, previousCount: number): Promise<void> {
        if (this.fetching.has(projectId)) return;
        this.fetching.add(projectId);
        this.reads++;
        let board: { tasks?: Task[] } | null = null;
        try {
            board = await getBoard(projectId);
        } catch {
            // Offline, or the project is gone; a later rise is asked about again.
        } finally {
            this.fetching.delete(projectId);
            this.reads--;
        }
        if (!board) {
            this.flushIfQuiet();
            return;
        }
        // A previous read is the pre-rise column only while its count still matches.
        const snapshot = this.columns.get(projectId);
        const ids = snapshot && snapshot.count === previousCount ? snapshot.ids : null;
        this.applyColumn(projectId, tasksInReview(board.tasks || []), reviewBaseline(previousCount, ids));
    }

    /** Land one reading of a project's Review column and announce what is new in it. */
    private applyColumn(projectId: string, review: Task[], baseline: ReviewBaseline | null): void {
        const ids = review.map(t => t.id);
        this.columns.set(projectId, { count: review.length, ids });
        const inReview = new Set(ids);
        const announced = new Map([...(this.announced.get(projectId) || [])].filter(([taskId]) => inReview.has(taskId)));
        this.announced.set(projectId, announced);
        if (baseline && review.length > baseline.count) {
            const arrivals: Task[] = [];
            for (const task of reviewArrivals(baseline, review)) {
                if (announced.get(task.id) === (task.moved_at || '')) continue;
                announced.set(task.id, task.moved_at || '');
                arrivals.push(task);
            }
            const openTaskId = appStore.selectedTask?.id || uiStore.floatingTask?.taskId || null;
            this.record(projectId, notableArrivals(arrivals, openTaskId, this.liveSelfMoves()));
        }
        this.flushIfQuiet();
    }

    /** One burst may read the same project twice; a card counts once towards what arrived. */
    private record(projectId: string, arrivals: Task[]): void {
        if (arrivals.length === 0) return;
        const group = this.pending.get(projectId) ?? [];
        const collected = new Set(group.map(t => t.id));
        for (const task of arrivals) {
            if (!collected.has(task.id)) group.push(task);
        }
        this.pending.set(projectId, group);
    }

    private liveSelfMoves(): string[] {
        const cutoff = Date.now() - SELF_MOVE_TTL_MS;
        const live: string[] = [];
        for (const [taskId, at] of this.selfMoved) {
            if (at < cutoff) this.selfMoved.delete(taskId);
            else live.push(taskId);
        }
        return live;
    }

    /** The burst is over when nothing is being read: what was collected goes out as one notification. */
    private flushIfQuiet(): void {
        if (this.reads > 0 || this.pending.size === 0) return;
        const groups: ArrivalGroup[] = [...this.pending].map(([projectId, tasks]) => ({ projectId, tasks }));
        this.pending.clear();
        this.announce(groups);
    }

    private announce(groups: ArrivalGroup[]): void {
        if (!uiStore.notifyOnReview) return;
        const notice = planReviewNotice(groups, this.reviewCounts(), this.attention());
        if (!notice) return;

        const project = appStore.projects.find(p => p.id === notice.projectId);
        const projectName = project?.name || notice.projectId;
        const task = notice.task;
        if (task) {
            const tag = `mike-task-${task.id}`;
            this.show({
                title: task.title?.trim() || t('notify.review.untitled'),
                body: t('notify.review.body', { project: projectName }),
                tag,
                url: taskViewHref(notice.projectId, task.id),
            }, { tag, projectId: notice.projectId, taskId: task.id });
            return;
        }
        this.show({
            title: t('notify.review.many', { count: notice.count }),
            body: t('notify.review.most', { project: projectName }),
            tag: BATCH_TAG,
            url: projectBoardHref(notice.projectId),
        }, { tag: BATCH_TAG, projectId: notice.projectId, taskId: null });
    }

    private reviewCounts(): Record<string, number> {
        const counts: Record<string, number> = {};
        for (const [projectId, review] of this.counts) counts[projectId] = review;
        return counts;
    }

    /**
     * What the user is looking at, read at the moment of the decision — `document.hasFocus()` is not a
     * reactive value, so nothing here can watch it.
     */
    private attention(): AttentionState {
        return {
            documentVisible: document.visibilityState === 'visible',
            documentFocused: document.hasFocus(),
            reviewProject: uiStore.reviewColumnInViewOf,
        };
    }

    /** Only what reached the screen is worth remembering. */
    private show(spec: BrowserNotification, notice: ShownNotice): void {
        if (!showBrowserNotification(spec)) return;
        this.shown.set(notice.tag, notice);
    }
}

export const reviewNotifier = new ReviewNotifier();
