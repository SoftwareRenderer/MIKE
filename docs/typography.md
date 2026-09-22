# CJK typography

`--font-sans` has no CJK coverage in its Latin base, so Chinese/Japanese/Korean gets its chain from a
`:lang()` block at the end of `frontend/src/app.css` (`zh-Hans`/`zh-Hant`/`ja`/`ko`).

- Keep it scoped to `:lang()`: a global rule restyles English chrome. `--font-mono` must stay CJK-free
  (code must not pick up a CJK face).
- **A generic family ends a font list.** `sans-serif` matches every script, so every family written after
  it is unreachable; `--font-sans-latin` stops before it and each chain carries its own copy last.
- **CJK faces come FIRST, before the Latin base.** A Latin alias (`Arial`, `Helvetica`) resolves through
  fontconfig to something that already covers the CJK run, so the platform answers the missing script
  from its own language fallback and never reads further down the list — a CJK face written after the
  Latin base is decorative. Accepted cost: Latin in a CJK locale renders with the CJK face's own Latin
  glyphs, which is what a Chinese or Japanese site does anyway.
- **`html:lang(…)` selects, and it overrides the `--font-sans` VARIABLE** (not `font-family`), because
  components read the variable directly (StatsModal, SandboxConfigModal) as well as inheriting from
  `body`; the higher specificity also stops an installed theme's `:root { --font-sans }` from dropping the
  chain.
- The CJK block adds no `white-space` override: measured on a CJK board, the 36 `nowrap` rules with no
  overflow contract produced zero overflowing boxes and no row-height change. If a translation does push
  a label past its box, give that rule the `overflow: hidden; text-overflow: ellipsis` pair rather than a
  scoped `white-space: normal`, which trades a clip for a ragged row.

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
