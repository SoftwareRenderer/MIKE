// =============================================================================
// Stacking tiers — the numeric half of the `--z-*` scale declared in `src/app.css`
//
// The two halves must agree key for key: `--z-modal-stacked` is `Z.modalStacked`, and a value that
// drifts between them puts a component in a rank nobody documented. `__tests__/z-layers.unit.ts`
// reads `app.css` and fails on a mismatch, so the comment is a pointer, not the only guard.
//
// A tier is a rank, not a distance, and it only means something inside its own stacking context: an
// ancestor with `transform`, `filter`, `opacity < 1`, or `position` + `z-index` restarts the game, so
// a tier asked for under a `BaseModal` overlay orders that dialog's siblings and nothing else. Take
// the lowest rank that clears the siblings in your own context.
// =============================================================================

export const Z = {
    /** Under the surface it belongs to. */
    below: -1,
    /** One layer above the unpositioned content around it. */
    raised: 1,
    /** Above `raised` siblings — a tint, link overlay or sticky strip inside one component. */
    content: 2,
    /** Sticky headers and bars that scroll over their own pane. */
    sticky: 10,
    /** A menu anchored inside the pane that clips it. */
    dropdown: 100,
    /** Full-screen panels, and the fixed menus and tooltips that escape a pane and must clear them. */
    overlay: 900,
    /** `BaseModal`'s overlay and the full-screen views that behave like one. */
    modal: 1000,
    /** A dialog that opens over a dialog: `PathPicker`, the extension dialogs, stacked pickers. */
    modalStacked: 1100,
    /** `Toast.svelte` and its twins — above every dialog, because a dialog must not hide a fault. */
    toast: 10000,
    /** The reconnect and cache banners in `App.svelte`: above everything the page itself can put up. */
    banner: 10001,
    /** Boot screens, and the transient drag handles attached to `document.body`. */
    max: 99999,
} as const;
