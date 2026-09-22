/**
 * The shapes Settings → Providers edits before saving: unsaved values plus per-card UI state, not
 * the `Provider` the server sends, read by both the modal that owns the list and the card that
 * edits one row.
 */

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
