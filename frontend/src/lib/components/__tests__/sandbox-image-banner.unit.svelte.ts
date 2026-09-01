import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, cleanup, fireEvent } from '@testing-library/svelte';

// The banner uses svelte/transition, which needs the DOM the jsdom run does not provide.
vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));

import SandboxImageStatusBanner from '$lib/components/SandboxImageStatusBanner.svelte';
import { sandboxImageStatus } from '$lib/services/sandbox-image-status.svelte';
import { SANDBOX_IMAGE_PHASES } from '$lib/utils/sandbox-image-status';

const IMAGE = 'localhost/sandbox-getmike.dev';

afterEach(() => {
    cleanup();
    sandboxImageStatus.clear();
});

describe('SandboxImageStatusBanner', () => {
    it('is absent while nothing is being prepared', () => {
        render(SandboxImageStatusBanner);
        expect(document.querySelector('.sandbox-image-status')).toBeNull();
    });

    it('shows the phase and the build output', async () => {
        render(SandboxImageStatusBanner);
        sandboxImageStatus.update({ phase: SANDBOX_IMAGE_PHASES.building, image: IMAGE });
        await Promise.resolve();

        const bar = document.querySelector('.sandbox-image-status');
        expect(bar).not.toBeNull();
        expect(document.querySelector('.sandbox-image-status-label')?.textContent).toContain(IMAGE);
        // Announced as progress, not as an error.
        expect(bar?.getAttribute('role')).toBe('status');
        expect(document.querySelector('.sandbox-image-status-log')).toBeNull();

        sandboxImageStatus.update({ phase: SANDBOX_IMAGE_PHASES.building, image: IMAGE, line: 'STEP 1/3: FROM alpine' });
        await Promise.resolve();
        expect(document.querySelector('.sandbox-image-status-log')?.textContent).toContain('STEP 1/3: FROM alpine');
    });

    it('shows a failure with its reason and dismisses', async () => {
        render(SandboxImageStatusBanner);
        sandboxImageStatus.update({ phase: SANDBOX_IMAGE_PHASES.pulling, image: IMAGE });
        sandboxImageStatus.update({
            phase: SANDBOX_IMAGE_PHASES.failed,
            image: IMAGE,
            error: 'sandbox image ' + IMAGE + ' is not available locally',
        });
        await Promise.resolve();

        const bar = document.querySelector('.sandbox-image-status');
        expect(bar?.getAttribute('role')).toBe('alert');
        expect(document.querySelector('.sandbox-image-status-error')?.textContent).toContain('is not available locally');

        const dismiss = document.querySelector('.sandbox-image-status-dismiss');
        expect(dismiss).not.toBeNull();
        await fireEvent.click(dismiss!);
        expect(document.querySelector('.sandbox-image-status')).toBeNull();
    });

    it('disappears when the build succeeds', async () => {
        render(SandboxImageStatusBanner);
        sandboxImageStatus.update({ phase: SANDBOX_IMAGE_PHASES.building, image: IMAGE, line: 'writing manifest' });
        await Promise.resolve();
        expect(document.querySelector('.sandbox-image-status')).not.toBeNull();

        sandboxImageStatus.update({ phase: SANDBOX_IMAGE_PHASES.done, image: IMAGE });
        await Promise.resolve();
        expect(document.querySelector('.sandbox-image-status')).toBeNull();
    });

    it('does not resurrect a finished build for the next project', async () => {
        render(SandboxImageStatusBanner);
        sandboxImageStatus.update({ phase: SANDBOX_IMAGE_PHASES.building, image: IMAGE });
        await Promise.resolve();
        sandboxImageStatus.clear(); // what App.svelte does on project switch
        await Promise.resolve();
        expect(document.querySelector('.sandbox-image-status')).toBeNull();
        expect(screen.queryByText(IMAGE)).toBeNull();
    });
});
