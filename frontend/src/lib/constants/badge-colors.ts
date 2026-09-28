// =============================================================================
// Badge colours — the data half of the palette in `src/app.css`
//
// A board card picks a model badge's colour by hashing the model name, so the value is chosen in JS and
// written into a `style` attribute: data, not a declaration a theme reaches by retokening `app.css`.
// Most entries repeat a token's value (`#4C9AFF` is `--color-status-blue`, `#D2A8FF` is `--cm-function`);
// the last four have no token because no chrome colour looks like them. One home for the list.
// =============================================================================

/** Hash bucket → badge colour, in the order `getModelColor` indexes it. */
export const MODEL_BADGE_COLORS: readonly string[] = [
    '#888888', '#4C9AFF', '#3FB950', '#D29922', '#F85149',
    '#A371F7', '#E3B341', '#79C0FF', '#7EE787', '#FF7B72',
    '#D2A8FF', '#F0883E', '#56B6C2', '#F778BA', '#DB6D28',
];

/** An agent provider's badge — the purple `--color-status-purple` carries. */
export const AGENT_BADGE_COLOR = '#A371F7';
