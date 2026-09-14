import { describe, it, expect } from 'vitest';
import { isDisplayableImage, imageSides } from '$lib/utils/diff-image';
import { rawFileURL } from '$lib/api/fs';

// Which files get a "View image" button, and what clicking one asks for.

describe('which files are pictures', () => {
    it('claims the formats the server is willing to serve', () => {
        for (const path of ['logo.png', 'a/b/photo.JPG', 'shot.jpeg', 'anim.gif', 'hero.webp', 'pixel.bmp', 'favicon.ico', 'next.avif']) {
            expect(isDisplayableImage(path)).toBe(true);
        }
    });

    it('refuses what an <img> cannot paint or must not run', () => {
        // svg is the row that matters: markup, and a common image extension.
        for (const path of ['icon.svg', 'archive.zip', 'notes.txt', 'Makefile', 'data.png.gz', '.pngrc']) {
            expect(isDisplayableImage(path)).toBe(false);
        }
    });
});

describe('how many sides a changed image has', () => {
    it('shows one for a file the change created', () => {
        expect(imageSides({ isNewFile: true })).toBe('single');
    });

    it('shows two for a file the change altered', () => {
        expect(imageSides({})).toBe('compare');
        expect(imageSides({ isNewFile: false, isDeleted: false })).toBe('compare');
    });

    it('shows none for a file the change removed — neither tree holds it', () => {
        expect(imageSides({ isDeleted: true })).toBeNull();
    });
});

describe('the URL a pane loads', () => {
    it('names the project, the path and nothing else by default', () => {
        expect(rawFileURL({ project: 'p1', path: 'docs/logo.png' }))
            .toBe('/api/fs/raw?project=p1&path=docs%2Flogo.png');
    });

    it('carries the task whose worktree holds the file, and the commit to read it from', () => {
        const url = new URL(rawFileURL({ project: 'p1', task: 't 1', path: 'a b.png', ref: 'deadbeef' }), 'http://x');
        expect(url.searchParams.get('task')).toBe('t 1');
        expect(url.searchParams.get('path')).toBe('a b.png');
        expect(url.searchParams.get('ref')).toBe('deadbeef');
    });

    it('omits an empty task or ref rather than asking for the root of the tree', () => {
        const url = new URL(rawFileURL({ project: 'p1', task: '', path: 'x.png', ref: '' }), 'http://x');
        expect(url.searchParams.has('task')).toBe(false);
        expect(url.searchParams.has('ref')).toBe(false);
    });
});
