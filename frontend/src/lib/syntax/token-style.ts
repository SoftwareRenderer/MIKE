/**
 * Every token the syntax layer can emit, painted in one place.
 *
 * Four renderers color a token: the editor's dark `HighlightStyle`, the editor's light one, and two
 * diff views — which cannot use a `HighlightStyle` at all, because they render HTML strings and style
 * them through `.hl-*` classes driven by the `--cm-*` variables in `app.css` (so a diff follows the
 * theme without a second color table). Five hand-written lists used to describe those tokens (one per
 * renderer plus the diff class table), and they drifted: a declared profile tags `true` / `None` / `nil`
 * as `tags.atom` (see `TOK.constant`) and `atom` has no parent tag, so the `constant(keyword)` entry all
 * five carried matched nothing — constants came out in the default text color, in the editor and in
 * both diffs alike. The commit overlay's list was also the copy that lagged: it gained no new token
 * when the others did.
 *
 * One row per token; the editor reads the colors, `styles/token-colors.css` reads the classes. Adding
 * a token means adding a row **and** its rule there — `__tests__/token-style.unit.ts` fails either half
 * on its own, and fails any second stylesheet that colors a token class again.
 */
import { HighlightStyle } from '@codemirror/language';
import { tagHighlighter, tags } from '@lezer/highlight';
import type { Tag } from '@lezer/highlight';

export interface TokenStyle {
  /** The tag a grammar produces — the names `syntax/stream-tokens.ts` hands the stream tokenizer. */
  tag: Tag;
  /** The diff views' class for it, painted by `styles/token-colors.css`. Several tags may share one
   *  class: the diff palette is coarser than the editor's (`hl-atom` is three tags). */
  diffClass: string;
  /** The editor's color. Both are here because the editor themes hardcode theirs (they are mounted
   *  per view, where a CSS variable would not follow a theme switch inside an open document). */
  dark: string;
  light: string;
  /** Comments are italic, invalid ranges get a wavy underline — in the editor and in a diff. */
  italic?: boolean;
  wavy?: boolean;
}

export const TOKEN_STYLES: readonly TokenStyle[] = [
  { tag: tags.keyword, diffClass: 'hl-keyword', dark: '#F97583', light: '#CF222E' },
  // Constants, all three tags that mean one: a stream token named "atom" (what a declared profile
  // emits for True/False/None, true/false/null/undefined, true/false/nil/iota), and the two tags
  // JSON's own grammar uses for its literals — `bool` has no parent at all, so it painted nothing,
  // and `null` resolves through `keyword`, which read it as control flow.
  { tag: tags.atom, diffClass: 'hl-atom', dark: '#79C0FF', light: '#0550AE' },
  { tag: tags.bool, diffClass: 'hl-atom', dark: '#79C0FF', light: '#0550AE' },
  { tag: tags.null, diffClass: 'hl-atom', dark: '#79C0FF', light: '#0550AE' },
  { tag: tags.function(tags.variableName), diffClass: 'hl-function', dark: '#D2A8FF', light: '#8250DF' },
  { tag: tags.function(tags.definition(tags.variableName)), diffClass: 'hl-function', dark: '#D2A8FF', light: '#8250DF' },
  { tag: tags.definition(tags.variableName), diffClass: 'hl-definitionvariable', dark: '#C9D1D9', light: '#24292F' },
  { tag: tags.variableName, diffClass: 'hl-variable', dark: '#C9D1D9', light: '#24292F' },
  { tag: tags.propertyName, diffClass: 'hl-property', dark: '#D2A8FF', light: '#8250DF' },
  { tag: tags.string, diffClass: 'hl-string', dark: '#A5D6FF', light: '#0A3069' },
  { tag: tags.number, diffClass: 'hl-number', dark: '#79C0FF', light: '#0550AE' },
  { tag: tags.comment, diffClass: 'hl-comment', dark: '#8B949E', light: '#6E7781', italic: true },
  { tag: tags.operator, diffClass: 'hl-operator', dark: '#FF7B72', light: '#CF222E' },
  { tag: tags.punctuation, diffClass: 'hl-punctuation', dark: '#C9D1D9', light: '#24292F' },
  // `TOK.builtin`: a name the profile lists as built-in (`len`, `console`, `append`).
  { tag: tags.className, diffClass: 'hl-classname', dark: '#FFA657', light: '#953800' },
  { tag: tags.typeName, diffClass: 'hl-typename', dark: '#39D2C0', light: '#0550AE' },
  { tag: tags.tagName, diffClass: 'hl-tagname', dark: '#7EE787', light: '#116329' },
  { tag: tags.attributeName, diffClass: 'hl-attribute', dark: '#79C0FF', light: '#0550AE' },
  { tag: tags.attributeValue, diffClass: 'hl-attributevalue', dark: '#A5D6FF', light: '#0A3069' },
  { tag: tags.regexp, diffClass: 'hl-regexp', dark: '#A5D6FF', light: '#0A3069' },
  { tag: tags.meta, diffClass: 'hl-meta', dark: '#C9D1D9', light: '#24292F' },
  { tag: tags.invalid, diffClass: 'hl-invalid', dark: '#F97583', light: '#CF222E', wavy: true },
];

/** The editor's `HighlightStyle` for one theme, straight off the table. */
export function buildHighlightStyle(mode: 'dark' | 'light'): HighlightStyle {
  return HighlightStyle.define(
    TOKEN_STYLES.map((style) => {
      const color = mode === 'dark' ? style.dark : style.light;
      return {
        tag: style.tag,
        color,
        ...(style.italic ? { fontStyle: 'italic' } : {}),
        // The decoration takes the token's own color — `currentColor` is what CSS defaults a
        // text-decoration to, so the underline and the glyph cannot drift apart.
        ...(style.wavy ? { textDecoration: 'underline wavy' } : {}),
      };
    }),
  );
}

/**
 * The diff views' highlighter: the same table, class names instead of colors.
 *
 * Named for what it is rather than for one palette — the color is whatever `--cm-*` says, so a diff
 * in the light theme is the same classes in different colors.
 */
export const diffHighlighter = tagHighlighter(
  TOKEN_STYLES.map((style) => ({ tag: style.tag, class: style.diffClass })),
);
