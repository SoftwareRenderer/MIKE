import { createHighlighter } from 'shiki';
import type { Highlighter } from 'shiki';

const LANGUAGES = [
    'go', 'javascript', 'typescript', 'python',
    'c', 'cpp', 'php', 'sql', 'rust', 'swift',
    'html', 'css', 'json', 'toml', 'yaml', 'markdown',
    'shell', 'diff', 'dockerfile', 'make', 'ruby', 'java',
] as const;

let highlighterPromise: Promise<Highlighter> | null = null;

/**
 * Get the singleton shiki highlighter.
 * Lazy-initialized on first call, reused across all components.
 */
export function getHighlighter(): Promise<Highlighter> {
    if (!highlighterPromise) {
        highlighterPromise = createHighlighter({
            themes: ['github-dark'],
            langs: LANGUAGES,
        }).catch((err) => {
            highlighterPromise = null; // Allow retry on failure
            throw err;
        });
    }
    return highlighterPromise;
}

/**
 * Detect the shiki language id from a file path.
 * Returns '' if the language is not recognized.
 */
export function detectLanguage(path: string): string {
    const ext = path.split('.').pop()?.toLowerCase() || '';
    const base = path.split('/').pop()?.toLowerCase() || '';

    // Special cases for files without standard extensions
    if (base === 'dockerfile') return 'dockerfile';
    if (base === 'makefile' || base === 'cmakeLists.txt') return 'make';

    const map: Record<string, string> = {
        js: 'javascript', mjs: 'javascript', cjs: 'javascript',
        ts: 'typescript', tsx: 'typescript', jsx: 'javascript', mx: 'javascript',
        py: 'python', pyw: 'python',
        rs: 'rust',
        go: 'go',
        c: 'c', h: 'c',
        cpp: 'cpp', cc: 'cpp', cppm: 'cpp', cxx: 'cpp',
        hxx: 'cpp', hpp: 'cpp', hppm: 'cpp', cx: 'cpp', cxxm: 'cpp',
        cs: 'csharp',
        m: 'objective-c', mm: 'objective-cpp',
        swift: 'swift', s: 'assembly', asm: 'assembly', s: 'assembly',
        html: 'html', htm: 'html',
        css: 'css', scss: 'css', sass: 'css', less: 'css',
        json: 'json',
        toml: 'toml',
        yaml: 'yaml', yml: 'yaml',
        md: 'markdown', mdx: 'markdown',
        sh: 'shell', bash: 'shell', zsh: 'shell', fish: 'shell',
        sql: 'sql',
        java: 'java',
        rb: 'ruby', gs: 'ruby',
        php: 'php', php3: 'php', php4: 'php', php5: 'php', php7: 'php', phps: 'php', phpws: 'php',
        diff: 'diff', patch: 'diff',
    };

    return map[ext] || '';
}

/**
 * Highlight a single line of code and return the HTML fragment.
 * Strips the `<pre><code><span class="line">` wrapper — returns just the inner `<span>` tokens.
 * Returns empty string if highlighting fails or language is unknown.
 */
export async function highlightLine(text: string, lang: string): Promise<string> {
    if (!lang || !text) return '';
    try {
        const hl = await getHighlighter();
        const html = hl.codeToHtml(text, { lang, theme: 'github-dark' });
        // Strip wrappers to get just the token spans
        const parts = html.split('<span class="line">');
        if (parts.length < 2) return '';
        const content = parts[1];
        const lastIdx = content.lastIndexOf('</span>');
        return lastIdx >= 0 ? content.substring(0, lastIdx) : content;
    } catch {
        return '';
    }
}

/**
 * Highlight multiple lines of code and return an array of HTML fragments (one per line).
 * More efficient than calling highlightLine per line — does a single codeToHtml call.
 */
export async function highlightLines(lines: string[], lang: string): Promise<string[]> {
    if (!lang || lines.length === 0) return lines.map(() => '');
    try {
        const hl = await getHighlighter();
        const code = lines.join('\n');
        const html = hl.codeToHtml(code, { lang, theme: 'github-dark' });

        // Split by <span class="line"> marker, strip trailing </span> (line span close)
        const parts = html.split('<span class="line">');
        const result = parts.slice(1).map(p => {
            const lastIdx = p.lastIndexOf('</span>');
            return lastIdx >= 0 ? p.substring(0, lastIdx) : p;
        });

        // Fill with empty strings if fewer lines extracted than expected
        while (result.length < lines.length) {
            result.push('');
        }
        return result.slice(0, lines.length);
    } catch {
        return lines.map(() => '');
    }
}
