<script lang="ts">
    /** Provider draft with pricing fields (mutated in place via Svelte 5 deep reactivity). */
    let { provider, currencySymbol, index }: {
        provider: {
            input_price: number;
            output_price: number;
            cache_read_price: number;
            cache_write_price: number;
            power_consumption_w: number;
            electricity_cost_per_kwh: number;
        };
        currencySymbol: string;
        index: number;
    } = $props();

    function parsePrice(e: Event): number {
        return parseFloat((e.target as HTMLInputElement).value) || 0;
    }
</script>

<!-- Pricing Section -->
<div class="pricing-section">
    <div class="pricing-header">
        <span class="pricing-label">Pricing (USD per 1M tokens)</span>
        <span class="help-icon" title="Set token pricing to track costs in project stats. If Cache Write is set, it replaces Input for non-cached tokens.">
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                <circle cx="8" cy="8" r="6.5" stroke="currentColor" stroke-width="1.2"/>
                <text x="8" y="12" text-anchor="middle" fill="currentColor" font-size="0.75rem" font-weight="600">?</text>
            </svg>
        </span>
    </div>
    <div class="currency-grid" style="grid-template-columns: repeat(4, 1fr)">
        <div class="currency-field">
            <label for={`input-price-${index}`}>Input</label>
            <div class="currency-input-wrap">
                <span class="currency-prefix">{currencySymbol}</span>
                <input id={`input-price-${index}`} type="number" inputmode="decimal" min="0" step="any" value={provider.input_price > 0 ? provider.input_price : ''} oninput={(e) => { provider.input_price = parsePrice(e); }} class="currency-input" />
            </div>
        </div>
        <div class="currency-field">
            <label for={`output-price-${index}`}>Output</label>
            <div class="currency-input-wrap">
                <span class="currency-prefix">{currencySymbol}</span>
                <input id={`output-price-${index}`} type="number" inputmode="decimal" min="0" step="any" value={provider.output_price > 0 ? provider.output_price : ''} oninput={(e) => { provider.output_price = parsePrice(e); }} class="currency-input" />
            </div>
        </div>
        <div class="currency-field">
            <label for={`cache-read-price-${index}`}>Cache Read</label>
            <div class="currency-input-wrap">
                <span class="currency-prefix">{currencySymbol}</span>
                <input id={`cache-read-price-${index}`} type="number" inputmode="decimal" min="0" step="any" value={provider.cache_read_price > 0 ? provider.cache_read_price : ''} oninput={(e) => { provider.cache_read_price = parsePrice(e); }} class="currency-input" />
            </div>
        </div>
        <div class="currency-field">
            <label for={`cache-write-price-${index}`}>Cache Write</label>
            <div class="currency-input-wrap">
                <span class="currency-prefix">{currencySymbol}</span>
                <input id={`cache-write-price-${index}`} type="number" inputmode="decimal" min="0" step="any" value={provider.cache_write_price > 0 ? provider.cache_write_price : ''} oninput={(e) => { provider.cache_write_price = parsePrice(e); }} class="currency-input" />
            </div>
        </div>
    </div>
</div>

<!-- Power & Electricity Section -->
<div class="currency-section">
    <div class="currency-header">
        <span class="currency-label">Power & Electricity</span>
        <span class="help-icon" title="Estimate energy cost from system power draw. Power (W) × generation time gives kWh. Combined with electricity rate, adds to total cost.">
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                <circle cx="8" cy="8" r="6.5" stroke="currentColor" stroke-width="1.2"/>
                <text x="8" y="12" text-anchor="middle" fill="currentColor" font-size="0.75rem" font-weight="600">?</text>
            </svg>
        </span>
    </div>
    <div class="currency-grid">
        <div class="currency-field">
            <label for={`power-w-${index}`}>Power Consumption (W)</label>
            <input id={`power-w-${index}`} type="number" inputmode="decimal" min="0" step="any" value={provider.power_consumption_w > 0 ? provider.power_consumption_w : ''} oninput={(e) => { provider.power_consumption_w = parsePrice(e); }} class="field-input" />
        </div>
        <div class="currency-field">
            <label for={`elec-cost-${index}`}>Electricity ({currencySymbol}/kWh)</label>
            <div class="currency-input-wrap">
                <span class="currency-prefix">{currencySymbol}</span>
                <input id={`elec-cost-${index}`} type="number" inputmode="decimal" min="0" step="any" value={provider.electricity_cost_per_kwh > 0 ? provider.electricity_cost_per_kwh : ''} oninput={(e) => { provider.electricity_cost_per_kwh = parsePrice(e); }} class="currency-input" />
            </div>
        </div>
    </div>
</div>

<style>
    .pricing-section,
    .currency-section {
        margin-top: 12px;
        padding: 10px 12px;
        background: var(--color-surface-2);
        border-radius: var(--radius-md);
        border: 1px solid var(--color-border);
    }
    .pricing-header,
    .currency-header {
        display: flex;
        align-items: center;
        gap: 6px;
        margin-bottom: 8px;
    }
    .pricing-label,
    .currency-label {
        font-size: 0.6875rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: var(--color-text-secondary);
    }
    .help-icon {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        color: var(--color-text-secondary);
        opacity: 0.5;
        cursor: help;
    }
    .help-icon:hover {
        opacity: 0.8;
    }
    .currency-grid {
        display: grid;
        grid-template-columns: repeat(2, 1fr);
        gap: 8px;
    }
    .currency-field {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }
    .currency-field label {
        font-size: 0.75rem;
        color: var(--color-text-secondary);
    }
    .currency-input-wrap {
        display: flex;
        align-items: center;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        overflow: hidden;
    }
    .currency-input-wrap:focus-within {
        border-color: var(--color-accent);
    }
    .currency-prefix {
        padding: 4px 6px;
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        background: var(--color-surface-2);
        border-right: 1px solid var(--color-border);
        white-space: nowrap;
    }
    .currency-input,
    .field-input {
        flex: 1;
        min-width: 0;
        font-size: 0.8125rem;
        padding: 4px 6px;
        background: transparent;
        border: none;
        color: var(--color-text-primary);
        width: 100%;
    }
    .currency-input:focus,
    .field-input:focus {
        outline: none;
    }
    .field-input {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
    }
    .field-input:focus {
        border-color: var(--color-accent);
    }
</style>
