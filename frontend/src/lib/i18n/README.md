# i18n

`t(key, params?)` from `$lib/i18n` is the whole surface. `en.ts` is the English
catalog of record; a translation pack is installed with `setCatalog(code, messages)`
and everything a pack has not translated falls back to English.

```svelte
<script lang="ts">
    import { t } from '$lib/i18n';
</script>

<h2>{t('app.sidebar.projects')}</h2>
<p>{t('app.reconnect.dataFrom', { time: stale })}</p>
<button>{t('app.title.review', { name, count })}</button>  {# counts are plural tables }
```

## The convention: `aria-label` / `title` / `placeholder` are NOT converted

Text in an `aria-label`, `aria-labelledby`, `aria-describedby`, `title` or
`placeholder` attribute stays a **literal English string in the component**. Visible
text — text nodes, and the script/template expressions that produce them — converts.
This applies to every batch, and it is not an oversight to "finish" later.

**The reason is measured, not stylistic.** The a11y spec
(`src/lib/components/__tests__/accessibility.unit.svelte.ts`) walks the rendered DOM
and compares a control's labelled-by / `aria-label` text set against the *rendered
text* of every button in it. A missing key does not fail loudly: the runtime answers
with the key itself (`common.add`) so a hole in the catalog is visible instead of
blank. That is the right call for visible copy, and it is fatal for that invariant —
the moment a `<button>` gained `t()` text while its `aria-label` stayed English, the
comparison broke for ~27 components, including files whose conversion lived in other
batches. Converting visible text while leaving the labelling attributes literal keeps
the invariant true **by construction**: the two sides of the comparison can never
disagree, in any locale, for any catalog state.

The same argument covers the 48 e2e locators of the shape
`getByRole('button', { name })`: the accessible name is the `aria-label`, so those
locators never move.

Consequences, accepted deliberately:

- A tooltip (`title`) can be a different language from the sentence next to it.
- **The line is drawn at the tag, not at the computation.** A string built in
  `<script>` and *passed* to a `title` converts (`openTerminalTitle()` and
  `fullscreenToggleTitle()` in `utils/keyboard.ts` are the case, and they are visible
  shortcut-hint text too); a string interpolated inside the tag attribute itself does
  not (`title={`Branch: ${branch}`}`, `title={`Create the task${hint}`}` in
  `CreateTaskModal.svelte`). One rule, easy to check in review, and the English value
  is byte-identical either way so no locator moves.
- A script-level `title:` **object property** is different text: `showConfirm({ title })`
  renders an `<h2>`, so it is visible copy and it converts.
- Adding a `t()` call to an attribute is a review error, not a style preference.

## Rules for a converted string

- **Byte-identical English.** A catalog value is the copy that shipped, character for
  character — `…`, `—`, `🟡`, trailing spaces and all. Tests run in English; a changed
  string is a broken test, not an improvement.
- **Named interpolation, never concatenation.** `t('x', { n })` with `{n}` in the
  value. `'in ' + n + 's'` freezes English word order into the code.
- **Plurals are `Intl.PluralRules` tables.** `{ one: '… {count} task', other: '… {count}
  tasks' }` selected on `count`. Never `n === 1 ? 's' : ''` — English is the only
  language where that is correct.
- **Inline elements split a message into `.before` / `.after`** (e.g. the branch badge
  inside "Merge changes into ⟨branch⟩ when complete") rather than into concatenation,
  so a translation can move the element.
- **Never a key for text this app does not own**: extension `display_name`,
  descriptions, changelog entries, server error strings, `console.*` log lines.
- **Login copy needs the catalog to be static.** `LoginModal.svelte` renders before
  config and extensions load; `en.ts` is bundled into the app shell and `t()` never
  fetches, so the pre-auth screen is English by construction with nothing async on its
  path. Do not "fix" this by loading a pack on the login path.
- `document.documentElement.lang` belongs to the runtime. `frontend/index.html`'s
  hardcoded `lang="en"` is not this app's business, and there is no `language` config
  field.

## Measuring a batch: `i18n-count.py`

`cd frontend && python3 i18n-count.py [-v]` counts candidate strings per file —
template text nodes, plus quoted literals in `<script>`, in `.ts` modules, and inside
template mustache expressions. It excludes by **context, never by string identity** (so
a real hole still measures): the labelling attributes above, comments, `<style>`,
`console.*` arguments, property-key position, key-comparison context
(`e.key === 'ArrowDown'`), technical APIs (`addEventListener`, `querySelector<T>(`,
IndexedDB key helpers) and non-prose tokens (identifiers, paths, selectors, dotted
message keys, `clone-pending-${ts}`, `&times;`, and interpolated literals with no prose
of their own — `modal ${cls}`, `/api/projects/${id}/git/branches`).

A batch is done when its own list reaches `0`. The number a batch reports must be its
own file list measured with the same script before and after — e.g. the app-shell batch
measured `85 → 0` (the `85` re-measured on pristine `HEAD` copies of those 23 files, so
rule changes cannot move the baseline). Known limit: a prose word inside an identifier,
a CSS file or a server response is not counted — the `-v` listing is the thing to read,
not just the total.
