/**
 * THE ONE SPINNER
 *
 * A ring that speaks and a ring that stays out of the way are two different elements, and a
 * caller picks one: the announced ring carries its translated label inside `role="status"`,
 * and a decorative ring is hidden from the reader whose surrounding text already says the
 * state. A ring that mounts and unmounts carries no `aria-live` — that is what makes the
 * announcement one sentence instead of one per appearance.
 */
import { describe, it, expect, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';
import Spinner from '$lib/components/Spinner.svelte';

afterEach(cleanup);

const ring = (container: HTMLElement) => container.querySelector('span.spinner') as HTMLElement;

describe('Spinner', () => {
    it('says the label it was given', () => {
        const { container } = render(Spinner, { props: { label: 'Loading conversation…' } });
        expect(ring(container).getAttribute('role')).toBe('status');
        expect(ring(container).hasAttribute('aria-hidden')).toBe(false);
        expect(ring(container).querySelector('.sr-only')?.textContent).toBe('Loading conversation…');
    });

    it('says nothing when the text beside it already says the state', () => {
        const { container } = render(Spinner, { props: { decorative: true, label: 'Loading conversation…' } });
        expect(ring(container).getAttribute('aria-hidden')).toBe('true');
        expect(ring(container).getAttribute('role')).toBeNull();
        expect(ring(container).textContent?.trim()).toBe('');
    });

    it('draws the ring at the diameter it was given, and at 14px by default', () => {
        const { container } = render(Spinner, { props: { decorative: true } });
        expect(ring(container).getAttribute('style')).toContain('--spinner-size: 14px');
        const sized = render(Spinner, { props: { size: 20, decorative: true } });
        expect(ring(sized.container).getAttribute('style')).toContain('--spinner-size: 20px');
    });

    it('falls back to the default diameter for a size that is not a number', () => {
        const { container } = render(Spinner, { props: { size: 'large' as unknown as number, decorative: true } });
        expect(ring(container).getAttribute('style')).toContain('--spinner-size: 14px');
    });
});
