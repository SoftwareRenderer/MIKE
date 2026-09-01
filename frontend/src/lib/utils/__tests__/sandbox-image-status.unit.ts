import { describe, it, expect } from 'vitest';
import {
    applySandboxImageStatus,
    idleSandboxImageState,
    sandboxImageLabel,
    sandboxImageVisible,
    MAX_SANDBOX_IMAGE_LINES,
    SANDBOX_IMAGE_PHASES,
    type SandboxImageState,
} from '$lib/utils/sandbox-image-status';

const IMAGE = 'localhost/sandbox-getmike.dev';

describe('sandbox image status', () => {
    it('starts hidden with nothing to say', () => {
        const state = idleSandboxImageState();
        expect(state.phase).toBe('idle');
        expect(sandboxImageVisible(state)).toBe(false);
        expect(sandboxImageLabel(state)).toBe('');
    });

    it('a build announcement shows what is being built', () => {
        const state = applySandboxImageStatus(idleSandboxImageState(), {
            phase: SANDBOX_IMAGE_PHASES.building,
            image: IMAGE,
        });
        expect(state.phase).toBe('working');
        expect(state.building).toBe(true);
        expect(sandboxImageVisible(state)).toBe(true);
        expect(sandboxImageLabel(state)).toContain('Building sandbox image');
        expect(sandboxImageLabel(state)).toContain(IMAGE);
    });

    it('a pull is labelled as a pull, not a build', () => {
        const state = applySandboxImageStatus(idleSandboxImageState(), {
            phase: SANDBOX_IMAGE_PHASES.pulling,
            image: 'docker.io/library/ubuntu:24.04',
        });
        expect(state.building).toBe(false);
        expect(sandboxImageLabel(state)).toContain('Pulling');
    });

    it('appends output lines in order', () => {
        let state = applySandboxImageStatus(idleSandboxImageState(), {
            phase: SANDBOX_IMAGE_PHASES.building,
            image: IMAGE,
        });
        state = applySandboxImageStatus(state, { phase: SANDBOX_IMAGE_PHASES.building, image: IMAGE, line: 'STEP 1/3: FROM alpine' });
        state = applySandboxImageStatus(state, { phase: SANDBOX_IMAGE_PHASES.building, image: IMAGE, line: 'STEP 2/3: RUN echo hi' });
        expect(state.lines).toEqual(['STEP 1/3: FROM alpine', 'STEP 2/3: RUN echo hi']);
    });

    it('keeps only the tail of a long build log', () => {
        let state = applySandboxImageStatus(idleSandboxImageState(), {
            phase: SANDBOX_IMAGE_PHASES.building,
            image: IMAGE,
        });
        for (let i = 0; i < MAX_SANDBOX_IMAGE_LINES + 10; i++) {
            state = applySandboxImageStatus(state, { phase: SANDBOX_IMAGE_PHASES.building, image: IMAGE, line: `line ${i}` });
        }
        expect(state.lines.length).toBe(MAX_SANDBOX_IMAGE_LINES);
        expect(state.lines[state.lines.length - 1]).toBe(`line ${MAX_SANDBOX_IMAGE_LINES + 9}`);
        expect(state.lines[0]).toBe('line 10');
    });

    it('clears on success — a banner left standing after a good build reads like an error', () => {
        const working: SandboxImageState = applySandboxImageStatus(idleSandboxImageState(), {
            phase: SANDBOX_IMAGE_PHASES.building,
            image: IMAGE,
            line: 'Successfully tagged ' + IMAGE,
        });
        const done = applySandboxImageStatus(working, { phase: SANDBOX_IMAGE_PHASES.done, image: IMAGE });
        expect(done.phase).toBe('idle');
        expect(done.lines).toEqual([]);
        expect(sandboxImageVisible(done)).toBe(false);
    });

    it('keeps the output that explains a failure', () => {
        let state = applySandboxImageStatus(idleSandboxImageState(), {
            phase: SANDBOX_IMAGE_PHASES.building,
            image: IMAGE,
        });
        state = applySandboxImageStatus(state, { phase: SANDBOX_IMAGE_PHASES.building, image: IMAGE, line: 'error: FROM takes one argument' });
        state = applySandboxImageStatus(state, {
            phase: SANDBOX_IMAGE_PHASES.failed,
            image: IMAGE,
            error: 'build sandbox image ' + IMAGE + ': exit status 1',
        });
        expect(state.phase).toBe('failed');
        expect(sandboxImageVisible(state)).toBe(true);
        expect(state.error).toContain('exit status 1');
        expect(state.lines).toEqual(['error: FROM takes one argument']);
        // Still described as a build, which is what was running when it failed.
        expect(state.building).toBe(true);
    });

    it('gives a failure an error even when the server sent none', () => {
        const state = applySandboxImageStatus(idleSandboxImageState(), { phase: SANDBOX_IMAGE_PHASES.failed, image: IMAGE });
        expect(state.error).not.toBe('');
    });

    it('ignores a phase it does not know, without touching the state', () => {
        const working = applySandboxImageStatus(idleSandboxImageState(), {
            phase: SANDBOX_IMAGE_PHASES.building,
            image: IMAGE,
            line: 'STEP 1/2',
        });
        const ignored = applySandboxImageStatus(working, { phase: 'who_knows', image: IMAGE });
        expect(ignored).toBe(working);
        expect(applySandboxImageStatus(working, {})).toBe(working);
    });

    it('keeps the image name a frame omitted', () => {
        const working = applySandboxImageStatus(idleSandboxImageState(), {
            phase: SANDBOX_IMAGE_PHASES.building,
            image: IMAGE,
        });
        const withLine = applySandboxImageStatus(working, { phase: SANDBOX_IMAGE_PHASES.building, line: 'x' });
        expect(withLine.image).toBe(IMAGE);
    });
});
