# i18n

`t(key, params?)` from `$lib/i18n` is the whole surface a component uses. `en.ts` is the
English catalog of record; a translation pack is installed with `setCatalog(code, messages)`
and everything a pack has not translated falls back to English **per key, never per catalog**.

```svelte
<script lang="ts">
    import { t } from '$lib/i18n';
</script>

<h2>{t('app.sidebar.projects')}</h2>
<p>{t('app.reconnect.dataFrom', { time: stale })}</p>
<button>{t('app.title.review', { name, count })}</button>  {# counts are plural tables }
```

`runtime.svelte.ts` holds the runes (locale + catalogs in `$state`), `index.ts` is the import
surface components need (`t`, `locale`, `setCatalog`, `resetToEnglish` and the message types —
anything else is imported from the module that owns it), `en.ts` is the catalog. The pack-loading half lives here too now — `catalog.ts` (the
wire shape and its validation), `load-pack.ts` (resolve the winning pack, fetch it, install it),
`cache.ts` (the IndexedDB copy that paints before the extension list arrives) and `boot.ts` (the
pre-paint apply) — but **`t()` itself never fetches anything**: it reads whatever is installed,
and loading is `load-pack.ts`'s job, called from the extension list. There is still no language
picker, because the enabled pack *is* the language (`docs/i18n.md`).

## Adding or changing a key

- **English first, and English only.** The value you add to `en.ts` is the copy that ships,
  byte-identical to the string the component displayed before — `…`, `—`, `🟡`, trailing
  spaces and all. A conversion batch changes no word, because the tests are pinned to that
  exact text. **A translated value never enters core**: `en.ts` holds one language, and the
  only thing that can put another language on screen is a pack installed through
  `setCatalog`.
- **A key is stable — tests and packs both anchor on it.** The copy tests
  (`components/__tests__/*-copy.unit.svelte.ts`) pin the English text a key resolves to, and a
  translation pack is written against the key list. So a rename is a silent double break: the
  pack's translation stops applying and the copy test fails far from its cause. Delete a key
  only with its last use; never repurpose one for different copy.
- **Typo a key, not a string.** `MessageKey` is `keyof typeof en`, so `t('settins.save')` is an
  `npm run check` error rather than text on screen — which is exactly why the rule above
  matters: that guarantee only holds while the key is in the catalog.
- **Named interpolation, never concatenation.** `t('x', { n })` with `{n}` in the value.
  `'in ' + n + 's'` freezes English word order into the code, and so does
  `` `rename ${sym} to ${name}` ``. Interpolation is one pass: a substituted value cannot
  inject another placeholder, and a placeholder with no value stays visible (`{newName}` in
  the output) instead of blanking — a hole you can see is a bug report.
- **Plurals are `Intl.PluralRules` tables.** `{ one: '… {count} task', other: '… {count}
  tasks' }` selected from `count`. Never `n === 1 ? 's' : ''` — English is the only language
  where that is correct; some languages answer `one`/`few`/`many`/`other`, and some only ever
  `other`, so **a pack may ship `other` alone** and a table missing the selected category
  degrades to it.
- **Inline elements split a message into `.before` / `.after`** (e.g. the branch badge inside
  "Merge changes into ⟨branch⟩ when complete") rather than into concatenation, so a
  translation can move the element.
- **Never a key for text this app does not own**: extension `display_name`, descriptions,
  changelog entries, server error strings, `console.*` log lines. That text is the author's,
  and translating it silently puts words in their mouth.
- **Login copy needs the catalog to be static.** `LoginModal.svelte` renders before config and
  extensions load; `en.ts` is bundled into the app shell and `t()` never fetches, so the
  pre-auth screen is English by construction with nothing async on its path. Do not "fix" this
  by loading a pack on the login path.
- `i18n.test.plain` / `i18n.test.files` are the runtime's self-test surface — the two keys
  `__tests__/runtime.unit*.ts` exercise. Nothing renders them; do not delete them or reuse
  them for UI copy.

## Namespaces

Keys are grouped by area, one flat dotted namespace per area, declared in a `═══ namespace
═══` banner in `en.ts`. Every batch has landed, so this is the whole list:

`app.*` · `board.*` / `boardDnd.*` · `task.*` · `chat.*` · `explorer.*` · `settings.*` ·
`sandbox.*` · `terminal.*` · `api.*` · `attachments.*` · `editor.*` / `lsp.*` ·
`diff.*` / `git.*` · `time.*` · `host.*` · `memory.*` · `storage.*` · `swUpdate.*` ·
`tool.*` · `effort.*` · `i18n.offer.*`

`common.*` (shared verbs) stayed reserved: each area kept its own copy of Cancel / Save /
Close, and renaming landed keys to share them costs more than the duplication — see the
stability rule above.

- A new string goes in **its area's** namespace, named for what it says, not for the component
  that renders it (`chat.args.lspRenameTo`, not `chat.message.line12`).
- **A new shared verb goes in `common.*`** rather than being duplicated a third time. The
  duplicates that already landed (`explorer.shared.cancel`, `board.editTask.save`,
  `settings.…`) keep their names — renaming is far more expensive than duplication, see the
  stability rule above.
- Two components with byte-identical copy in one *area* share one key (`explorer.shared.*`);
  across areas they do not, because a shared key is a promise that the copy can never diverge.

## The fallback ladder, and what it warns about

For every call, in order:

1. the active pack's value for the key;
2. the English value, **plus one development `console.warn`** naming the locale and the key;
3. the key itself, **plus a louder warning** — a key no catalog has is a hole in `en.ts`.

Rung 3 prints the key rather than nothing on purpose: a visible `settings.extensions.addMount`
in the UI is a bug report, and an empty string is a mystery. It is also load-bearing for the
a11y test below, which compares a control's accessible name against its rendered text — both
sides must resolve to the same thing whatever the catalog state. A missing key is unreachable
in typed code (it does not compile), so this rung only fires for a cast or a bad pack.

Both warnings are deduped per (locale, key) so a hot render path warns once and not per render,
`resetToEnglish()` re-arms them, and a production build does not pay for them
(`import.meta.env.PROD` is a build-time constant). `__tests__/runtime.unit.ts` **asserts the
warnings** — a silent fallback is the failure mode this ladder exists to avoid.

## `<html lang>`

`runtime.svelte.ts` is the **only** writer of `document.documentElement.lang`: it applies the
active locale at module init and on every `setCatalog` / `resetToEnglish`, so the attribute and
the locale cannot disagree. `frontend/index.html`'s hardcoded `lang="en"` is the default for
the bytes the server sends before any script runs, not the mechanism — nothing may read the
attribute or write it from elsewhere. There is no `language` config field.

## Core names no language — and `i18n-gate.py` measures it

Core contains **zero translated strings and zero language names**: no endonym in a code
comment, no language name in a menu, no branch of any kind keyed on a locale. That used to be a
sentence a reviewer had to remember, and one had already landed (a comment in `ExtensionsTab`
naming two locale codes and an endonym). It is now a command:

```bash
cd frontend && npm run check:i18n       # = python3 i18n-gate.py; exit 0 = core names no language
```

Four rules over `src/` (**`__tests__/` dirs and `*.unit.*` files are out of scope** — a pack
test cannot prove a pack applied without bytes that differ from English):

| # | Rule | Why |
|---|---|---|
| 1 | no locale tag (`zh`/`fr`/`ja`/`ko`, region forms included) | core that knows a tag knows a language |
| 2 | no CJK/hangul/kana codepoint | how "no endonym, no translated sample value" is checked without listing every language |
| 3 | no `Intl.DisplayNames` | a pack's name is the pack's own (below) |
| 4 | no `locale === 'xx'` / `switch (locale)` | the same knowledge as control flow |

Rule 1 is a **tripwire**, not a proof: it lists the tags this product's packs ship, because a
new language arrives through a manifest rather than through core. Its job is to catch core
re-learning a language it had forgotten.

Two things still contain a tag or a translated value, and neither is an exception to the rule:

- **`src/app.css`'s CJK typography block** — `:lang(zh) { --font-sans: … }`. AGENTS.md requires
  that block (and *why* those chains are ordered as they are), and a selector **targets** a
  language without naming one to a reader, so the gate allows a tag there only inside a
  `:lang(…)` selector. Prose in that block says "the Simplified chain", never the tag.
- **`__tests__/` fixtures** carry real tags and real translated values
  (`setCatalog('zh-CN', { 'nav.board': '看板' })`), because the assertion is "a *different
  language* arrived", which a synthetic marker cannot make. Where the assertion is about
  *plurals* instead, the fixture is synthetic (`'[few:{count}]'`) and the locale is named for
  the categories it answers with (`ru`, `ar`, and a single-form locale) — see `runtime.unit.ts`.

Related facts the gate keeps honest:

- **A locale's display name is the pack's own**: `display_name` from the manifest/registry
  entry, verbatim (`entryDisplayName`, `utils/locale-offer.ts`). Core derives none —
  `Intl.DisplayNames` was tried here and deleted, because the endonym it produced was core's
  opinion about a language rather than the author's. Settings shows the **tag** (`.ext-locale`
  in `ExtensionsTab`), the first-run offer shows the pack's own name.
- The runtime hands a locale code to `Intl` and believes the answer; it never branches on one.
- Nothing in core spells the fallback locale: the runtime compares against `DEFAULT_LOCALE`
  (imported from `en.ts`), so rule 4 flags even `=== 'en'`.

## `aria-label` / `title` / `placeholder`: attribute copy converts

Every literal in an `aria-label`, `title`, `placeholder` or `alt` attribute resolves through `t()`. The
accessible name is the only copy a person using a screen reader gets, so an untranslated attribute is a
half-translated app whatever the label beside it says. (`alt=""` stays — it is how markup says *decorative*;
`aria-labelledby` and `aria-describedby` hold an element id, and no copy is due in one.)

Three kinds of string stay out of the catalog, each for its own reason:

- **Text this app did not write.** A filename, branch, host path or mount mode in a `title` is
  data, as are git's status letters and `rw` / `ro`. So is a `placeholder` that is a format example
  (`/host/path`, `docker.io/ubuntu:latest`, `http://localhost:8080/v1`) — the example *is* the
  answer, and translating it teaches the wrong shape. These are the only literals still allowed at
  these attributes.
- **A `title` repeating visible text.** Drop it. Two names for one control is one too many, and the
  visible text is already the accessible name. On an icon-only control the tooltip is the only hover
  affordance, so there `title` and `aria-label` both stay, fed by **one** key.
- **A name already spelled elsewhere.** Where an attribute prints a word that is a visible label in
  another component — a card's `Move to {status}` naming a column header (`Card.svelte`) — it uses
  that key. A second table of the same words is how one state ends up with two translations.

Reactivity is the same rule as visible text: `t()` inline at the attribute, so a language switch
re-renders it. Never a `$state` snapshot of a `t()` result. A name that leaves the render path — a
prop default (`ResizeHandle`'s fallback is a `$derived`), an imperative `el.title`, a CodeMirror
gutter row — becomes a getter read at build time, recomputed through `onLocaleChange`
(`runtime.svelte.ts`) when nothing else redraws it. `utils/git-gutter.ts`'s title getter is that
case, and `utils/keyboard.ts`'s `openTerminalTitle()` / `fullscreenToggleTitle()` are the same shape
arrived at from the other side.

Two invariants keep the batch honest:

- **English renders byte-identical** to the string the attribute held before its conversion, so the
  ~48 e2e `getByRole(name)` locators and the `[aria-label=…]` selectors `docs/testing.md` names do
  not move. A deliberate rename — a slug like `refresh-tokens` becoming a real sentence, or an
  icon-only control that used to announce nothing — moves its locator in the same change.
- `accessibility.unit.svelte.ts` compares a control's labelled-by / `aria-label` text against the
  rendered text of the buttons it names. Both sides read the same catalog now, so a key that has not
  landed shows its key text on both sides instead of breaking the comparison.

`i18n-count.py` measures these attributes, so the batch is one command rather than a grep:

```bash
cd frontend && python3 i18n-count.py    # 0 findings; a literal names its file:line and its attribute
```

A literal in an `aria-label`, `title`, `placeholder` or `alt` fails it. The format examples above are the
only sites the script permits, each listed in its `TEXT_ATTR_ALLOW` with the reason beside it. An
IDREF (`aria-labelledby`, `aria-describedby`), a flag (`aria-hidden`, `aria-expanded`) and a key inside
`t(…)` are not text, so the script asks nothing of them. `docs/i18n.md → Attribute text is a catalog key`
holds the rule, the blind spots and the ratchet.

## Measuring the tree: `i18n-count.py`

`cd frontend && python3 i18n-count.py [-v]` counts candidate strings per file over **all of
`src/`** — template text nodes, plus quoted literals in `<script>`, in `.ts` modules, and
inside template mustache expressions. It excludes by **context, never by string identity**
(so a real hole still measures): the labelling attributes the section above measures by grep
instead, comments, `<style>`,
`console.*` arguments, property-key position, key-comparison context
(`e.key === 'ArrowDown'`), technical APIs (`addEventListener`, `querySelector<T>(`,
IndexedDB key helpers), non-prose tokens (identifiers, paths, selectors, dotted message
keys, `clone-pending-${ts}`, `&times;`, interpolated literals with no prose of their own —
`modal ${cls}`, `/api/projects/${id}/git/branches`), embedded source code, CSS declaration
values, terminal keycaps and syntax keyword tables.

**It is a gate, not a report: any candidate exits non-zero.** `npm run check:i18n-surface`
is it, and `make test-frontend` runs it beside `npm test`, `i18n-gate.py` and the counter's
own self-test — so a new literal in a component cannot land on a green suite. The tree is at
**0 across 190 files**; keep it there.

Passing file or directory arguments measures a subset, which is how a change to one area
checks itself before running the whole gate.

`i18n-count_test.py` is what makes the exclusions trustworthy: every rule is pinned **in both
directions** — real copy in a lookalike position still measures, the lookalike does not.
That is the only defence against the gate lying by going blind, which has already happened
once (a `x || 'Loading…'` was swallowed by the union-type rule). Run it after touching any
exclusion.

Known limit: a prose word inside an identifier, a CSS file or a server response is not
counted — the `-v` listing is the thing to read, not just the total.
