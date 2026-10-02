/**
 * The shapes Settings → Providers edits before saving: unsaved values plus per-card UI state, not
 * the `Provider` the server sends, read by both the modal that owns the list and the card that
 * edits one row.
 */

import type { ProviderSampling } from '$lib/types';

/** One model's sampler overrides as Settings edits them: a blank field is null and is never sent. */
interface ModelSamplingDraft {
    temperature: number | null;
    top_p: number | null;
    top_k: number | null;
    min_p: number | null;
    frequency_penalty: number | null;
    presence_penalty: number | null;
    repetition_penalty: number | null;
    seed: number | null;
}

const SAMPLING_FIELDS = [
    'temperature', 'top_p', 'top_k', 'min_p',
    'frequency_penalty', 'presence_penalty', 'repetition_penalty', 'seed',
] as const;

export type SamplingKey = (typeof SAMPLING_FIELDS)[number];

/** A blank sampler draft: every field is the provider's to decide. */
export function emptySampling(): ModelSamplingDraft {
    const draft = {} as ModelSamplingDraft;
    for (const field of SAMPLING_FIELDS) draft[field] = null;
    return draft;
}

/** The stored overrides as a draft, so an expanded row always has something to bind. */
export function samplingFromConfig(sampling: ProviderSampling | undefined): ModelSamplingDraft {
    const draft = emptySampling();
    for (const field of SAMPLING_FIELDS) {
        const v = sampling?.[field];
        if (typeof v === 'number' && Number.isFinite(v)) draft[field] = v;
    }
    return draft;
}

/**
 * The draft as the saved payload: a blank field is left out, and an all-blank row has no `sampling`
 * object at all. A 0 is a value somebody typed, and is sent.
 */
export function samplingPayload(draft: ModelSamplingDraft): ProviderSampling | undefined {
    const out: ProviderSampling = {};
    let any = false;
    for (const field of SAMPLING_FIELDS) {
        const v = draft[field];
        if (typeof v === 'number' && Number.isFinite(v)) {
            out[field] = v;
            any = true;
        }
    }
    return any ? out : undefined;
}

/** A model row of a provider draft. */
export interface ModelEntry {
    name: string;
    readonly: boolean; // true = fetched from API, not editable
    reasoning_effort?: string; // known level or custom value (passed through to the server)
    context_window?: number | null; // nil if not reported by LLM; editable in UI
    // Per-model pricing/power overrides — null = inherit provider default
    input_price?: number | null;
    output_price?: number | null;
    cache_read_price?: number | null;
    cache_write_price?: number | null;
    power_consumption_w?: number | null;
    // Sampler overrides — null = send nothing for that parameter
    sampling: ModelSamplingDraft;
}

/** A provider row of the Settings draft list. */
export interface ProviderDraft {
    uid: string; // stable key for {#each} — never changes after creation
    id: number; // stable identifier for LLM providers; 0 = new/unsaved
    name: string; // agent provider name (manifest-derived); empty for LLM providers
    display_name?: string; // only agent providers carry a manifest-derived display name
    kind: 'llm' | 'agent';
    url: string;
    api_key: string; // full value when editing, masked ("••••••••••") when loaded from config
    agent_config_dir?: string;
    models: ModelEntry[];
    concurrency: number;
    max_requests_per_second: number; // 0 = unlimited; max new LLM request starts per second
    stream_idle_seconds: number; // 0 = no ceiling; seconds a response may stay silent
    // Pricing (USD per 1M tokens)
    input_price: number;
    output_price: number;
    cache_read_price: number;
    cache_write_price: number;
    // Power & electricity
    power_consumption_w: number;
    electricity_cost_per_kwh: number;
    fetchingModels: boolean;
    fetchError: string;
    expanded: boolean; // card expansion state
    testingConnection: boolean;
    testResult: { success: boolean; message: string } | null;
}
