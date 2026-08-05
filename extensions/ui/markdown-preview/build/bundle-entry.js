// Entry point for building the single-file Wordgard browser bundle used by this
// extension. Exposes the pieces Preview.js needs on `window` so they can be
// loaded via a plain <script> tag (matching how host-client.js and Preview.js
// are loaded in the sandboxed extension iframe).
//
// Sources are the dist files of the published `wordgard` npm package, copied
// into the build dir by build/build-wordgard.mjs.
import { Wordgard, menuBar } from './editor.js';
import { fullSchema } from './schema.js';
import { history } from './history.js';
import { serialize, parse } from './doc.js';
import { GardState } from './state.js';
import { tables } from './table.js';
import {
    Paragraph,
    Heading,
    CodeBlock,
    CodeBlockLanguage,
    Blockquote,
    BulletList,
    OrderedList,
    ListItem,
    InlineListItem,
    HorizontalRule,
    Figure,
    Image,
    LineBreak,
    Strong,
    Emphasis,
    Code,
    Link,
    Strikethrough,
    Underline,
    Superscript,
    Subscript,
    ImageAlt,
    Table,
    TableRow,
    Cell,
    HeaderCell,
    ColSpan,
    RowSpan,
} from './types.js';

window.Wordgard = Wordgard;
window.WordgardEditor = { Wordgard, menuBar };
window.WordgardSchema = { fullSchema };
window.WordgardHistory = { history };
window.WordgardDoc = { serialize, parse };
window.WordgardState = { GardState };
window.WordgardTable = { tables };
window.WordgardTypes = {
    Paragraph, Heading, CodeBlock, CodeBlockLanguage, Blockquote,
    BulletList, OrderedList, ListItem, InlineListItem, HorizontalRule,
    Figure, Image, LineBreak, Strong, Emphasis, Code, Link, Strikethrough,
    Underline, Superscript, Subscript, ImageAlt,
    Table, TableRow, Cell, HeaderCell, ColSpan, RowSpan,
};
