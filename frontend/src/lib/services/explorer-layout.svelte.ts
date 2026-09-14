import { loadExplorerLayout, saveExplorerLayout } from '$lib/storage';

const DEFAULTS = { leftWidth: 250, rightWidth: 180, splitRatio: 0.5 };
const MIN_LEFT = 180;
const MAX_LEFT = 500;
const MIN_RIGHT = 120;
const MAX_RIGHT = 400;

/** Column widths and the file-browser / git-graph split, measured against the rendered panels. */
export class ExplorerLayout {
    leftWidth = $state(DEFAULTS.leftWidth);
    rightWidth = $state(DEFAULTS.rightWidth);
    splitRatio = $state(DEFAULTS.splitRatio);

    constructor(isMobile: { readonly isMobile: boolean }) {
        loadExplorerLayout().then(layout => {
            this.leftWidth = layout.leftWidth;
            this.rightWidth = layout.rightWidth;
            this.splitRatio = layout.splitRatio;
        }).catch(() => {});

        $effect(() => {
            if (isMobile.isMobile) {
                this.splitRatio = 0.75;
            }
        });
    }

    /** Drag handlers measure the live panels: the columns are siblings, so only the DOM knows. */
    private body(): Element | null {
        return document.querySelector('.explorer-body');
    }

    private persist(): void {
        saveExplorerLayout({ leftWidth: this.leftWidth, rightWidth: this.rightWidth, splitRatio: this.splitRatio }).catch(() => {});
    }

    handleLeftDragStart = (_e: MouseEvent | TouchEvent, _startX: number) => {};
    handleLeftDragMove = (pointerX: number) => {
        const body = this.body();
        if (!body) return;
        const posX = pointerX - body.getBoundingClientRect().left;
        this.leftWidth = Math.max(MIN_LEFT, Math.min(MAX_LEFT, posX));
    };
    handleLeftDragEnd = () => { this.persist(); };

    handleRightDragStart = (_e: MouseEvent | TouchEvent, _startX: number) => {};
    handleRightDragMove = (pointerX: number) => {
        const body = this.body();
        if (!body) return;
        const posX = body.getBoundingClientRect().right - pointerX;
        this.rightWidth = Math.max(MIN_RIGHT, Math.min(MAX_RIGHT, posX));
    };
    handleRightDragEnd = () => { this.persist(); };

    handleSplitDragStart = (_e: MouseEvent | TouchEvent, _startY: number) => {};
    handleSplitDragMove = (pointerY: number) => {
        const container = document.querySelector('.explorer-split-container');
        if (!container) return;
        const rect = container.getBoundingClientRect();
        this.splitRatio = Math.max(0.2, Math.min(0.8, (pointerY - rect.top) / rect.height));
    };
    handleSplitDragEnd = () => { this.persist(); };
}
