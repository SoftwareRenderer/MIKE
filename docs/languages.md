# Language Knowledge in Extensions — Design Detail

Read this before modifying extension manifests, `backend/extensions/manifest_validate.go`, `frontend/src/lib/syntax/`, `frontend/src/lib/utils/editor-language.ts`, or `highlighter.ts`. The hard rules are in `AGENTS.md → Extensions`.

This document records the refactor that moved language knowledge out of the core (grammar maps, templates, and label maps) and into LSP extension manifests. The core now provides a generic engine with documented fallbacks.

## `language` vs `language_id`

The browser uses `resolveFile(path).language` for both LSP routing and grammar selection, ensuring a file is always opened with the correct server and grammar.

The `file_types[]` array (`{extension, language_id, label, syntax}`) is used because one extension may serve multiple file types (e.g., `ts`, `tsx`, `js`, `jsx`) under a single `language` (e.g., `"typescript"`), while the LSP server (like `tsserver`) requires distinct `language_id`s (ScriptKinds) for each.

Collapsing these would either break routing (e.g., `lookupExtension("javascript")` finding nothing) or cause parsing errors (e.g., treating `.tsx` as `typescript`).

## `syntax` as Data

To avoid running arbitrary JavaScript from extensions in the app's realm, syntax profiles are pure data. They define:
- `line_comment` and `line_comments[]`
- `block_comment`
- `strings[]` (single character or repeated, e.g., Python's `"""`)
- `pairs[]`
- `keywords`, `types`, `constants`, `builtins`, `numbers`, and `region`.

`frontend/src/lib/syntax/spec-language.ts` converts a profile into a `StreamLanguage` and provides:
- **Folding**: A `foldService` (ignoring brackets in comments/strings) and region folding (e.g., `//#region`).
- **Indentation**: An `indentService`.
- **Editing**: `commentTokens` and `closeBrackets` data for auto-close and Ctrl-/.
- **Styling**: Bracket pair colorization based on `pairs`.

## Token Coloring and Brackets

**Vocabulary and Shape**:
Word lists define a language's vocabulary. If a name is not in a list, its color is determined by its shape in this order:
1. Declared `keywords`, `types`, `constants`, or `builtins`.
2. Function calls (a name followed by `(`).
3. Capitalized names (treated as types).

These rules are configurable per profile via `call_as_function` and `capitalized_types`. For example, Go's exported constants start with upper-case and are colored as types; this is an accepted trade-off.

**Bracket Pair Colorization**:
Follows VS Code's rule: a pair's color depends on its nesting level. The engine cycles through three colors, skipping comments and strings. Colors are defined by the theme (`.cm-bracket-l0|1|2`).

`buildLanguageSupport(spec, name)` manages all declared language features. The editor, outline, and LSP tooltip renderer consume it. The diff view uses the same specs but parses a `Tree` rather than using editor state.

## Code Fences in LSP Documentation

Hover cards often include code fences (e.g., ` ```go `). The client highlights these only if the tag resolves to a `Language`.

`lsp-client-config.ts` uses `getGrammarForFence(tag, filePath)`, which resolves the tag by checking:
1. The declaration's `language_id`, `language`, or `extension`.
2. The core fallback table (matching tag to file suffix).
3. The grammar of the file being documented.
4. Prose (markdown/text) as a final fallback.

The hook lives on the client, so hover, completion documentation, and signature help all share the same logic.

## Quality Trade-offs

Spec-built stream grammars do not produce a parse tree. Consequently:
- **Folding** is bracket-based.
- **Outlines** rely on LSP document symbols or three regex extractors for Go templates, Markdown headings, and Svelte `<script>` blocks.

Because of this, `@codemirror/lang-*` packages for languages covered by extensions (JS, TS, Go, Python, Rust, C++, PHP, Svelte) are excluded from dependencies to avoid redundancy.

## Implementation Details

- **Manifests**: `file_types[]` is the source for routing, `didOpen` language IDs, and status-bar labels. Go uses `extensions.LSPConfig.fileTypeFor`; the browser uses `editor-language.ts::resolveFile`.
- **Shared Scan**: Folding, indentation, and bracket matching all share a single "is this range code?" reader (`syntax/stream-tokens.ts`) to ensure consistency.
- **Validation**: `backend/extensions/manifest_validate.go` ensures manifests are valid before they are stored, removing the need for runtime migration.
- **LSP Switch**: `editor_enabled` (Settings) controls whether an LSP channel opens, but `resolveFile()` still provides the language and syntax data from the manifest.
- **Core Fallbacks**: `frontend/src/lib/utils/fallback-languages.ts` provides support for common types (JSON, Markdown, HTML, CSS, YAML, SQL) when no extension is available.
- **Styling**: `frontend/src/lib/syntax/token-style.ts` is the single source of truth for token colors, used by both the editor and the diff highlighter.

## Tests

- `backend/extensions/manifest_validate_test.go`: Validator refusals.
- `backend/lsp/docsync_test.go`: Wire IDs per file.
- `backend/lsp/tsserver_integration_test.go`: Verifies `tsserver` refuses `.jsx` if the ID is collapsed to `typescript`.
- `frontend/src/lib/syntax/__tests__/spec-language.unit.ts`: Tokens, folds, and indentation.
- `frontend/src/lib/syntax/__tests__/bracket-pairs.unit.ts`: Nesting levels and CSS classes.
- `frontend/src/lib/syntax/__tests__/bundled-profiles.unit.ts`: Shipped manifests validation.
- `frontend/src/lib/__tests__/highlighter.unit.ts`: Diff view consistency.
- `frontend/src/lib/utils/__tests__/editor-outline.unit.ts`: Outline fallback extractors.
- `frontend/src/lib/syntax/__tests__/token-style.unit.ts`: Token colors and CSS mapping.
- `frontend/src/lib/utils/__tests__/editor-language.unit.svelte.ts`: Resolution order and fence tags.
- `frontend/src/lib/utils/__tests__/lsp-doc-highlight.unit.svelte.ts`: LSP hover highlighting.
- `frontend/tests/declared-languages-check.mjs`: Live probe of wire IDs, folding, and indentation.
- `frontend/tests/lsp-hover-highlight-check.mjs`: Verifies grammar follows the open file.

## Verification

- **Stored shape**: `go test ./backend/config/`
- **Registry**: `python3 scripts/gen-registry.py --check`
