# CJK typography

`--font-sans` has no CJK coverage in its Latin base, so Chinese/Japanese/Korean gets its chain and its
line-breaking rules from the `CJK typography` block near the end of `frontend/src/app.css`, keyed on
`html:lang(…)`. The bare `zh` subtag and `zh-Hans` share the Simplified chain; `zh-Hant`, `ja` and `ko`
have their own.

- Keep it scoped to `:lang()`: a global rule restyles English chrome. `--font-mono` must stay CJK-free
  (code must not pick up a CJK face).
- **A generic family ends a font list.** `sans-serif` matches every script, so any family listed after
  it is unreachable. `--font-sans-latin` stops before the generic family, and each CJK chain ends with
  its own generic fallback.
- **CJK faces come FIRST, before the Latin base.** A Latin alias (`Arial`, `Helvetica`) resolves through
  fontconfig to something that already covers the CJK run, so the platform answers the missing script
  from its own language fallback and never reads further down the list — a CJK face written after the
  Latin base is decorative. Accepted cost: Latin in a CJK locale renders with the CJK face's own Latin
  glyphs, which is what a Chinese or Japanese site does anyway.
- **`html:lang(…)` selects the font and overrides the `--font-sans` variable** (not `font-family`). This
  ensures that components reading the variable directly (StatsModal, SandboxConfigModal) still use the
  CJK chain, and the higher specificity prevents themes from accidentally dropping the CJK fallback.
- The CJK block does not override `white-space`. Measured on a CJK board, the existing `nowrap` rules
  produced no overflowing boxes and no row-height changes. If a translation pushes a label past its box,
  give that rule `overflow: hidden; text-overflow: ellipsis` rather than a scoped `white-space: normal`,
  which trades a clip for a ragged row.

## Verifying typography needs pixels

Glyph advance cannot tell faces apart (CJK glyphs are em-width, so every face measures the same) and
`document.fonts.check()` answers `true` for a family that is not installed, because *a* fallback can
render the text. Rasterize the same string into a canvas under both stacks and compare the bytes.

Nothing in `make check` does this. `make run`, open a CJK locale, and compare the two canvases in the
browser: a wrong face is a rendering fact, and no unit test in either language can see it.

## Related

`document.documentElement.lang` has exactly one writer — the i18n runtime (`docs/i18n.md`);
`index.html`'s `lang="en"` is the initial value only. It describes the language of the *rendered strings*
(the active pack, else `en`), not the browser's language, and the `:lang(…)` rules here key off it.
