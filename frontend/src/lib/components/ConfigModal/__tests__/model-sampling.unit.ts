import { describe, it, expect } from 'vitest';
import { emptySampling, samplingFromConfig, samplingPayload } from '$lib/components/ConfigModal/drafts';
import type { ProviderSampling } from '$lib/types';

/** The eight fields, named here so this test pins the set too. */
const SAMPLING_FIELDS = [
    'temperature', 'top_p', 'top_k', 'min_p',
    'frequency_penalty', 'presence_penalty', 'repetition_penalty', 'seed',
] as const;

type Draft = ReturnType<typeof emptySampling>;

function withValues(values: Partial<Draft>): Draft {
    return { ...emptySampling(), ...values };
}

describe('samplingPayload', () => {
    it('sends no sampling object at all when every field is blank', () => {
        expect(samplingPayload(emptySampling())).toBeUndefined();
    });

    it('sends only the field that was filled in', () => {
        expect(samplingPayload(withValues({ top_p: 0.9 }))).toEqual({ top_p: 0.9 });
    });

    it('keeps a 0, which is a value somebody chose and not a blank', () => {
        const payload = samplingPayload(withValues({ temperature: 0, top_k: 0 }));
        expect(payload).toEqual({ temperature: 0, top_k: 0 });
    });

    it('drops a field the input left unparseable', () => {
        const draft = withValues({ seed: NaN, min_p: 0.05 });
        expect(samplingPayload(draft)).toEqual({ min_p: 0.05 });
    });

    it('carries all eight fields when all eight are set', () => {
        const draft = withValues({
            temperature: 0.7, top_p: 0.9, top_k: 40, min_p: 0.05,
            frequency_penalty: -1.5, presence_penalty: 0.25, repetition_penalty: 1.1, seed: 7,
        });
        expect(Object.keys(samplingPayload(draft) ?? {}).sort()).toEqual([...SAMPLING_FIELDS].sort());
    });
});

describe('samplingFromConfig', () => {
    it('hands a row with no stored sampling a blank draft, so the row has something to bind', () => {
        const draft = samplingFromConfig(undefined);
        expect(draft).toEqual(emptySampling());
        for (const field of SAMPLING_FIELDS) expect(draft[field]).toBeNull();
    });

    it('leaves a field that was never stored blank', () => {
        const draft = samplingFromConfig({ temperature: 0.2 });
        expect(draft.temperature).toBe(0.2);
        expect(draft.top_p).toBeNull();
    });

    it('reads back whatever it wrote: a saved row reopens with the same blanks and the same values', () => {
        const stored: ProviderSampling = { temperature: 0, seed: 42 };
        const draft = samplingFromConfig(stored);
        expect(draft.temperature).toBe(0);
        expect(samplingPayload(draft)).toEqual(stored);
    });
});
