/**
 * Announces tasks that enter Review, in every project and not only the one on screen. Two frames carry
 * the news and either can arrive first, so both feed one ledger and the ledger decides who hears it.
 */
import type { Task } from '$lib/types';
import { getBoard } from '$lib/api/projects';
import { appStore, uiStore } from '$lib/stores';
import { t } from '$lib/i18n';
import { taskViewHref } from '$lib/utils/app-link';
import { showBrowserNotification, dismissBrowserNotification, type BrowserNotification } from '$lib/utils/notifications';
import {
    dismissedTags,
    notableArrivals,
    reviewArrivals,
    reviewBaseline,
    shouldNotifyReview,
    tasksInReview,
    type AttentionState,
    type ReviewBaseline,
    type ShownNotice,
} from '$lib/utils/review-notify';

/** How long a move this tab made itself counts as its own news. */
const SELF_MOVE_TTL_MS = 60_000;

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

    observeCounts(counts: Record<string, { review?: number } | undefined>): void {
        // Counted even while off, so the first frame after it comes on is a steady count rather than a
        // debt of notifications for changes already made.
        const enabled = appStore.config?.notify_on_review === true;
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
        let tasks: Task[];
        try {
            const board = await getBoard(projectId);
            tasks = board.tasks || [];
        } catch {
            // Offline, or the project is gone; a later rise is asked about again.
            this.fetching.delete(projectId);
            return;
        }
        this.fetching.delete(projectId);
        // A previous read is the pre-rise column only while its count still matches.
        const snapshot = this.columns.get(projectId);
        const ids = snapshot && snapshot.count === previousCount ? snapshot.ids : null;
        this.applyColumn(projectId, tasksInReview(tasks), reviewBaseline(previousCount, ids));
    }

    /** Land one reading of a project's Review column and announce what is new in it. */
    private applyColumn(projectId: string, review: Task[], baseline: ReviewBaseline | null): void {
        const ids = review.map(t => t.id);
        this.columns.set(projectId, { count: review.length, ids });
        const inReview = new Set(ids);
        const announced = new Map([...(this.announced.get(projectId) || [])].filter(([taskId]) => inReview.has(taskId)));
        this.announced.set(projectId, announced);
        if (!baseline || review.length <= baseline.count) return;

        const arrivals: Task[] = [];
        for (const task of reviewArrivals(baseline, review)) {
            if (announced.get(task.id) === (task.moved_at || '')) continue;
            announced.set(task.id, task.moved_at || '');
            arrivals.push(task);
        }
        const openTaskId = appStore.selectedTask?.id || uiStore.floatingTask?.taskId || null;
        this.notify(projectId, notableArrivals(arrivals, openTaskId, this.liveSelfMoves()));
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

    private notify(projectId: string, arrivals: Task[]): void {
        if (arrivals.length === 0) return;
        const attention: AttentionState = {
            documentVisible: document.visibilityState === 'visible',
            documentFocused: document.hasFocus(),
            reviewColumnInView: uiStore.reviewColumnInViewOf === projectId,
        };
        if (!shouldNotifyReview(appStore.config?.notify_on_review === true, attention)) return;

        const project = appStore.projects.find(p => p.id === projectId);
        const body = t('notify.review.body', { project: project?.name || projectId });
        const [first] = arrivals;
        if (!first) return;
        if (arrivals.length > 1) {
            const tag = `mike-review-${projectId}`;
            this.show({
                title: t('notify.review.many', { count: arrivals.length }),
                body,
                tag,
                url: taskViewHref(projectId, first.id),
            }, { tag, projectId, taskId: null });
            return;
        }
        const tag = `mike-task-${first.id}`;
        this.show({
            title: first.title?.trim() || t('notify.review.untitled'),
            body,
            tag,
            url: taskViewHref(projectId, first.id),
        }, { tag, projectId, taskId: first.id });
    }

    /** Only what reached the screen is worth remembering. */
    private show(spec: BrowserNotification, notice: ShownNotice): void {
        if (!showBrowserNotification(spec)) return;
        this.shown.set(notice.tag, notice);
    }
}

export const reviewNotifier = new ReviewNotifier();
