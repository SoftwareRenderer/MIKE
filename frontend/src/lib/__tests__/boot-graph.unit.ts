/**
 * The boot-graph guard against a bundle of four chunks. What matters here is that it refuses: a walk
 * that resolved nothing would pass every real build, and a real build cannot show its own failure.
 */
import { describe, it, expect } from 'vitest';
import type { Plugin } from 'vite';
import { bootGraphPlugin } from '../../../scripts/boot-graph';

type FakeChunk = {
    type: 'chunk';
    name: string;
    isEntry: boolean;
    imports: string[];
    moduleIds: string[];
};

/** The entry and one chunk it statically imports; a test adds chunks and edges of its own. */
function bundle(overrides: Record<string, FakeChunk> = {}): Record<string, FakeChunk> {
    return {
        'assets/index-aaa.js': {
            type: 'chunk', name: 'index', isEntry: true,
            imports: ['assets/api-bbb.js'], moduleIds: ['/src/main.ts'],
        },
        'assets/api-bbb.js': {
            type: 'chunk', name: 'api', isEntry: false,
            imports: [], moduleIds: ['/src/lib/api/index.ts'],
        },
        ...overrides,
    };
}

/** Run the guard and report what it refused, or `''` when it let the build through. */
function refuse(denyNames: readonly RegExp[], denyModules: readonly RegExp[], built: Record<string, FakeChunk>): string {
    const plugin = bootGraphPlugin(denyNames, denyModules) as Plugin;
    const generateBundle = plugin.generateBundle as unknown as (this: unknown, options: unknown, b: unknown) => void;
    try {
        generateBundle.call(
            { error: (message: string): never => { throw new Error(message); } },
            {},
            built,
        );
    } catch (e) {
        return e instanceof Error ? e.message : String(e);
    }
    return '';
}

const EDITOR = /^FileEditor$/;
const RENDERER = /\/node_modules\/d3-array\//;

describe('boot-graph guard', () => {
    it('refuses a first load that statically imports a chunk meant to sit behind a click', () => {
        const built = bundle({
            'assets/editor-ccc.js': {
                type: 'chunk', name: 'FileEditor', isEntry: false,
                imports: [], moduleIds: ['/src/lib/components/FileEditor.svelte'],
            },
        });
        // The entry here is two hops from the editor, which is the shape that hides an edge.
        built['assets/api-bbb.js'].imports = ['assets/editor-ccc.js'];

        expect(refuse([EDITOR], [], built)).toContain('FileEditor');
    });

    it('refuses a vendor module the entry reaches through another chunk', () => {
        const built = bundle({
            'assets/vendor-ddd.js': {
                type: 'chunk', name: 'vendor', isEntry: false,
                imports: [], moduleIds: ['/node_modules/d3-array/src/array.js'],
            },
        });
        built['assets/api-bbb.js'].imports = ['assets/vendor-ddd.js'];

        expect(refuse([], [RENDERER], built)).toContain('/node_modules/d3-array/src/array.js');
    });

    it('lets a chunk the entry only imports dynamically stay in the build', () => {
        const built = bundle({
            'assets/editor-ccc.js': {
                type: 'chunk', name: 'FileEditor', isEntry: false,
                imports: [], moduleIds: ['/src/lib/components/FileEditor.svelte'],
            },
            'assets/vendor-ddd.js': {
                type: 'chunk', name: 'vendor', isEntry: false,
                imports: [], moduleIds: ['/node_modules/d3-array/src/array.js'],
            },
        });

        expect(refuse([EDITOR], [RENDERER], built)).toBe('');
    });

    it('refuses a deny pattern that names nothing, so a renamed chunk cannot silence it', () => {
        expect(refuse([/^NothingWithThatName$/], [], bundle())).toContain('names no chunk');
        expect(refuse([], [/\/node_modules\/nothing-vendored-here\//], bundle())).toContain('names no module');
    });
});
