import { loadAppSetting, saveAppSetting } from '$lib/storage';

const DEFAULT_BOARD_RATIO = 0.22;
const DEFAULT_CHAT_RATIO = 0.55;
const MIN_BOARD_RATIO = 0.12;
const MAX_BOARD_RATIO = 0.40;
const MIN_CHAT_RATIO = 0.15;
const MAX_CHAT_RATIO = 0.80;

export interface ColumnResizeOptions {
    boardKey?: string;
    chatKey?: string;
}

/** Reactive column resize state for a 3-column layout (board | chat | diff).
 * Loads persisted ratios from IndexedDB and saves on drag end. */
export function useColumnResize({
    boardKey = 'kanban:detail:boardRatio',
    chatKey = 'kanban:detail:chatRatio',
}: ColumnResizeOptions = {}) {
    let boardRatio = $state(DEFAULT_BOARD_RATIO);
    let chatRatio = $state(DEFAULT_CHAT_RATIO);

    let boardDragStartX = $state(0);
    let boardDragStartRatio = $state(0);
    let chatDragStartX = $state(0);
    let chatDragStartRatio = $state(0);

    let boardWidth = $derived(`${(boardRatio * 100).toFixed(1)}%`);
    let remainingWidth = $derived(`${((1 - boardRatio) * 100).toFixed(1)}%`);
    let chatWidth = $derived(`${(chatRatio * (1 - boardRatio) * 100).toFixed(1)}%`);
    let diffWidth = $derived(`${(((1 - chatRatio) * (1 - boardRatio)) * 100).toFixed(1)}%`);

    function clampBoardRatio(val: number): number {
        return Math.max(MIN_BOARD_RATIO, Math.min(MAX_BOARD_RATIO, val));
    }
    function clampChatRatio(val: number): number {
        return Math.max(MIN_CHAT_RATIO, Math.min(MAX_CHAT_RATIO, val));
    }

    function onBoardDragStart(_event: MouseEvent, startX: number) {
        boardDragStartX = startX;
        boardDragStartRatio = boardRatio;
    }
    function onBoardDragMove(pointerX: number, containerWidth: number) {
        const delta = (pointerX - boardDragStartX) / Math.max(containerWidth, 1);
        boardRatio = clampBoardRatio(boardDragStartRatio + delta);
    }
    function onBoardDragEnd() {
        saveAppSetting(boardKey, boardRatio).catch(() => {});
    }

    function onChatDragStart(_event: MouseEvent, startX: number) {
        chatDragStartX = startX;
        chatDragStartRatio = chatRatio;
    }
    function onChatDragMove(pointerX: number, containerWidth: number) {
        const remaining = Math.max(containerWidth * (1 - boardRatio), 1);
        const delta = (pointerX - chatDragStartX) / remaining;
        chatRatio = clampChatRatio(chatDragStartRatio + delta);
    }
    function onChatDragEnd() {
        saveAppSetting(chatKey, chatRatio).catch(() => {});
    }

    // Load saved ratios on init
    loadAppSetting(boardKey, DEFAULT_BOARD_RATIO).then((v) => {
        const val = Number.parseFloat(String(v));
        if (!isNaN(val) && val > 0) boardRatio = val;
    }).catch(() => {});
    loadAppSetting(chatKey, DEFAULT_CHAT_RATIO).then((v) => {
        const val = Number.parseFloat(String(v));
        if (!isNaN(val) && val > 0) chatRatio = val;
    }).catch(() => {});

    return {
        boardRatio,
        chatRatio,
        boardWidth,
        remainingWidth,
        chatWidth,
        diffWidth,
        onBoardDragStart,
        onBoardDragMove,
        onBoardDragEnd,
        onChatDragStart,
        onChatDragMove,
        onChatDragEnd,
    };
}
