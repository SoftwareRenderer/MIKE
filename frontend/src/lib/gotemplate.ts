import { LanguageSupport, Language, StreamLanguage } from '@codemirror/language';
import type { StreamParser } from '@codemirror/language';

/**
 * Go template syntax highlighting via CodeMirror StreamLanguage.
 *
 * Recognizes HTML base content and Go template actions
 * (double-brace delimiters with keywords, vars, pipes).
 */

// Go template action keywords
const actionKeywords = new Set([
    'if', 'else', 'end', 'range', 'with',
    'template', 'define', 'block',
    'nil', 'true', 'false',
    'and', 'or', 'not',
    'eq', 'ne', 'lt', 'le', 'gt', 'ge',
    'printf', 'print', 'println',
    'len', 'index', 'html', 'js', 'urlquery',
    'slice', 'substr', 'default', 'timeout', 'recover',
]);

interface GoTemplateState {
    inAction: boolean;
    inString: string | null; // quote char
    inComment: boolean;
    inTag: boolean;
    tagDepth: number;
}

const startPos: GoTemplateState = {
    inAction: false,
    inString: null,
    inComment: false,
    inTag: false,
    tagDepth: 0,
};

function copyState(state: GoTemplateState): GoTemplateState {
    return { ...state };
}

const goTemplateParser: StreamParser<GoTemplateState> = {
    name: 'gotemplate',

    startState() {
        return { ...startPos };
    },

    copyState,

    token(stream, state) {
        // --- Inside a string (any mode) ---
        if (state.inString) {
            while (!stream.eol()) {
                const ch = stream.next();
                if (ch === state.inString) {
                    state.inString = null;
                    break;
                }
                if (ch === '\\') stream.next();
            }
            return 'string';
        }

        // --- Inside a comment action {{/* ... */}} ---
        if (state.inComment) {
            while (!stream.eol()) {
                if (stream.peek() === '*' && stream.string[stream.start + 1] === '/') {
                    stream.match('*/');
                    // Check for closing }}
                    stream.eatSpace();
                    stream.eat('-');
                    if (stream.match('}}')) {
                        state.inComment = false;
                        state.inAction = false;
                    }
                    return 'comment';
                }
                stream.next();
            }
            return 'comment';
        }

        // --- Inside a Go template action {{ ... }} ---
        if (state.inAction) {
            // Check for action close: -}} or }}
            if (stream.eat('-') && stream.match('}}', false)) {
                stream.match('}}');
                state.inAction = false;
                return 'meta';
            }
            if (stream.match('}}')) {
                state.inAction = false;
                return 'meta';
            }

            // Strings inside actions
            if (stream.peek() === '"' || stream.peek() === "'" || stream.peek() === '`') {
                const quote = stream.next();
                state.inString = quote;
                // Continue reading the string
                while (!stream.eol()) {
                    const ch = stream.next();
                    if (ch === quote) {
                        state.inString = null;
                        break;
                    }
                    if (ch === '\\') stream.next();
                }
                return 'string';
            }

            // Dot variables: .Field or standalone .
            if (stream.peek() === '.') {
                stream.next();
                if (stream.match(/[a-zA-Z_][a-zA-Z0-9_]*/)) {
                    return 'variableName';
                }
                // Standalone dot
                return 'variableName';
            }

            // Named variables: $var
            if (stream.peek() === '$') {
                stream.next();
                if (stream.match(/[a-zA-Z_][a-zA-Z0-9_]*/)) {
                    return 'variableName';
                }
                return 'variableName';
            }

            // Pipe operator
            if (stream.match('|')) {
                return 'operator';
            }

            // Comparison operators
            if (stream.match('==') || stream.match('!=') || stream.match('<=') || stream.match('>=')) {
                return 'operator';
            }
            if (stream.match('<') || stream.match('>') || stream.match('=')) {
                return 'operator';
            }

            // Keywords and identifiers
            if (stream.match(/[a-zA-Z_][a-zA-Z0-9_]*/)) {
                const word = stream.current();
                if (actionKeywords.has(word)) {
                    return 'keyword';
                }
                return 'function variableName';
            }

            // Numbers
            if (stream.match(/\d+(\.\d+)?/)) {
                return 'number';
            }

            // Punctuation
            if (stream.match(/[(){}\[\],:]/)) {
                return 'punctuation';
            }

            // Whitespace and other
            stream.next();
            return null;
        }

        // --- HTML / default mode ---

        // Check for action open: {{ or {{-
        if (stream.peek() === '{' && stream.string[stream.start + 1] === '{') {
            stream.match('{{');
            state.inAction = true;
            // Check for comment action
            if (stream.match('/*')) {
                state.inComment = true;
                state.inAction = false; // comment handles its own close
                // Skip whitespace after /*
                stream.eatSpace();
                return 'comment';
            }
            stream.eat('-');
            stream.eatSpace();
            return 'meta';
        }

        // HTML tag opening
        if (stream.match('</') || stream.match('<')) {
            state.inTag = true;
            state.tagDepth = 1;
            if (stream.match(/[a-zA-Z][a-zA-Z0-9]*/)) {
                return 'tagName';
            }
            return 'punctuation';
        }

        // Inside HTML tag
        if (state.inTag) {
            // Tag close
            if (stream.peek() === '>') {
                stream.next();
                state.inTag = false;
                state.tagDepth = 0;
                return 'punctuation';
            }
            // Nested tag
            if (stream.peek() === '<') {
                state.tagDepth++;
                stream.next();
                return 'punctuation';
            }

            // Attribute strings
            if (stream.peek() === '"' || stream.peek() === "'") {
                const quote = stream.next();
                while (!stream.eol()) {
                    const ch = stream.next();
                    if (ch === quote) break;
                    if (ch === '\\') stream.next();
                }
                return 'string';
            }

            // Attribute names
            if (stream.match(/[a-zA-Z_][a-zA-Z0-9_-]*/)) {
                return 'propertyName';
            }

            // Attribute value separator
            if (stream.match('=')) {
                return 'operator';
            }

            // Whitespace and other
            stream.next();
            return null;
        }

        // HTML comment
        if (stream.match('<!--')) {
            while (!stream.eol()) {
                if (stream.peek() === '-' && stream.string[stream.start + 1] === '-') {
                    stream.match('-->');
                    break;
                }
                stream.next();
            }
            return 'comment';
        }

        // Plain text — advance one character
        stream.next();
        return null;
    },

    blankLine(state, _indentUnit) {
        // Reset tag state on blank lines
        if (state.inTag && !state.inString) {
            state.inTag = false;
            state.tagDepth = 0;
        }
        return null;
    },
};

/**
 * Language definition for Go templates (.tmpl, .gohtml).
 */
export const goTemplateLanguage: Language = StreamLanguage.define(goTemplateParser);

/**
 * LanguageSupport for Go templates.
 */
export function goTemplate(): LanguageSupport {
    return new LanguageSupport(goTemplateLanguage);
}
