import type { LSPClient } from '@codemirror/lsp-client';

import { t } from '$lib/i18n';
/** Symbol format used by the symbols panel and sticky scroll. */
export interface LSPSymbol {
    name: string;
    kind: string;
    line: number;
    endLine: number;
    indent: number;
    children?: LSPSymbol[];
    containerName?: string; // parent symbol name (SymbolInformation)
}

/** Map LSP SymbolKind to our internal kind strings. */
const KIND_MAP: Record<number, string> = {
    1: 'file',       // File
    2: 'module',     // Module
    3: 'namespace',  // Namespace
    4: 'package',    // Package
    5: 'class',      // Class
    6: 'method',     // Method
    7: 'property',   // Property
    8: 'field',      // Field
    9: 'constructor',// Constructor
    10: 'enum',      // Enum
    11: 'interface', // Interface
    12: 'function',  // Function
    13: 'variable',  // Variable (legacy, was Constant)
    14: 'constant',  // Constant
    15: 'string',    // String
    16: 'number',    // Number
    17: 'boolean',   // Boolean
    18: 'array',     // Array
    19: 'object',    // Object
    20: 'key',       // Key
    21: 'null',      // Null
    22: 'enumMember',// EnumMember
    23: 'struct',    // Struct
    24: 'event',     // Event
    25: 'operator',  // Operator
    26: 'typeParameter', // TypeParameter
};

/**
 * Convert a single LSP DocumentSymbol to our LSPSymbol format.
 * Handles both DocumentSymbol (nested, has selectionRange + children) and
 * SymbolInformation (flat, has location instead of range).
 */
function convertSymbol(sym: any, depth: number = 0): LSPSymbol {
    const isDocumentSymbol = sym.selectionRange !== undefined;

    let line: number, endLine: number;
    if (isDocumentSymbol) {
        // DocumentSymbol: has selectionRange and range
        line = sym.selectionRange.start.line + 1;
        endLine = sym.range.end.line + 1;
    } else {
        // SymbolInformation: has location.range
        line = sym.location.range.start.line + 1;
        endLine = sym.location.range.end.line + 1;
    }
    const kind = KIND_MAP[sym.kind] || 'variable';

    return {
        name: sym.name,
        kind,
        line,
        endLine,
        indent: depth,
        containerName: sym.containerName || undefined, // SymbolInformation has containerName
        children: sym.children?.map((child: any) => convertSymbol(child, depth + 1)),
    };
}

/**
 * Flatten a nested symbol tree into a flat list.
 */
export function flattenSymbols(symbols: LSPSymbol[]): LSPSymbol[] {
    const result: LSPSymbol[] = [];
    function walk(syms: LSPSymbol[]) {
        for (const sym of syms) {
            result.push(sym);
            if (sym.children?.length) {
                walk(sym.children);
            }
        }
    }
    walk(symbols);
    return result;
}

/**
 * Build a nested tree from flat symbols using range containment.
 * If symbol A's range fully contains symbol B's range, B is a child of A.
 */
function buildTree(flat: LSPSymbol[]): LSPSymbol[] {
    // Sort by start line ascending
    const sorted = [...flat].sort((a, b) => a.line - b.line);
    const root: LSPSymbol = { name: '__root__', kind: 'file', line: 0, endLine: Infinity, indent: -1, children: [] };
    const stack: LSPSymbol[] = [root];

    for (const sym of sorted) {
        // Pop stack until we find a parent that contains this symbol
        // (parent's endLine >= this symbol's line)
        while (stack.length > 1 && stack[stack.length - 1]!.endLine < sym.line) {
            stack.pop();
        }
        // Create a copy with children array
        const node: LSPSymbol = { ...sym, children: [] };
        stack[stack.length - 1]!.children!.push(node);
        stack.push(node);
    }

    return root.children!;
}

/**
 * Request document symbols from the LSP server.
 * Returns nested symbols (use flattenSymbols for flat list).
 *
 * @param lspClient - The connected LSP client
 * @param filePath - Absolute file path
 * @param timeoutMs - Request timeout (default 2000ms)
 * @returns Nested LSP symbols, or null if LSP unavailable/timeout
 */
export async function requestLSPSymbols(
    lspClient: LSPClient | null,
    filePath: string,
    timeoutMs: number = 4000,
): Promise<LSPSymbol[] | null> {
    if (!lspClient) return null;

    const uri = `file://${filePath}`;

    try {
        // Push pending edits to the server before asking for symbols
        lspClient.sync?.();
        const result = await Promise.race([
            lspClient.request('textDocument/documentSymbol', {
                textDocument: { uri },
            }),
            new Promise<never>((_, reject) =>
                setTimeout(() => reject(new Error(t('lsp.symbolTimeout'))), timeoutMs),
            ),
        ]);

        if (!result) return null;

        // Kinds worth showing in the symbols panel
        const visibleKinds = new Set(['function', 'method', 'class', 'struct', 'enum', 'interface', 'type', 'typeParameter', 'namespace', 'package', 'module', 'file', 'property', 'field', 'constructor', 'constant', 'event']);
        // Skip symbols with keyword names (TS LSP returns these for computed properties)
        const skipNames = new Set(['if', 'for', 'while', 'do', 'let', 'const', 'var', 'this', 'super', 'new', 'delete', 'typeof', 'void', 'instanceof', 'in', 'of', 'function', 'class', 'return', 'break', 'continue', 'switch', 'case', 'default', 'try', 'catch', 'finally', 'throw', 'async', 'await', 'yield', 'import', 'export', 'from', 'as', 'is', '<function>', '<class>', '<unknown>']);

        // Handle both DocumentSymbol[] and SymbolInformation[] responses
        const symbols = Array.isArray(result) ? result : [];
        const converted = symbols.map(sym => convertSymbol(sym));
        const filtered = converted.filter(s => {
            if (!s.name || skipNames.has(s.name)) return false;
            if (!visibleKinds.has(s.kind)) return false;
            
            // HEURISTIC: Filter out symbols that are too short and have 'property' or 'constant' kind
            // This removes local loop variables (e.g., 'ch', 'i', 'x') which LSP misreports
            if (s.name.length <= 2 && (s.kind === 'property' || s.kind === 'constant')) {
                return false;
            }
            
            return true;
        });

        // If symbols are already nested (DocumentSymbol), return as-is
        if (converted.some(s => s.children && s.children.length > 0)) {
            return converted;
        }

        // Flat symbols (SymbolInformation) — build tree from range containment
        return buildTree(filtered);
    } catch {
        return null;
    }
}
