<script lang="ts">
    interface Option {
        value: string;
        label: string;
        meta?: string; // right-aligned text (e.g. hash + Origin)
    }

    let { options, value, onChange, ariaLabel }: { options: Option[]; value: string; onChange: (v: string) => void; ariaLabel: string } = $props();

    let open = $state(false);
    let ref: HTMLDivElement | null = null;
    let selected = $derived(options.find(o => o.value === value) || options[0]);

    function toggle() {
        open = !open;
    }

    function select(opt: Option) {
        onChange(opt.value);
        open = false;
    }

    function handleClickOutside(e: MouseEvent) {
        if (ref && !ref.contains(e.target as Node)) {
            open = false;
        }
    }

    $effect(() => {
        if (open) {
            document.addEventListener('click', handleClickOutside);
        } else {
            document.removeEventListener('click', handleClickOutside);
        }
        return () => document.removeEventListener('click', handleClickOutside);
    });
</script>

<div class="diff-select-wrapper" bind:this={ref} class:open>
    <button class="diff-select-btn" type="button" onclick={toggle} aria-expanded={open} aria-label={ariaLabel}>
        <span class="sel-label" title={selected?.label || ''}>{selected?.label || ''}</span>
        {#if selected?.meta}
            <span class="sel-meta">{selected.meta}</span>
        {/if}
    </button>
    <svg class="sel-chevron" width="10" height="10" viewBox="0 0 16 16" fill="none" aria-hidden="true">
        <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
    </svg>
    {#if open}
        <div class="sel-dropdown" role="listbox">
            {#each options as opt (opt.value)}
                <button
                    class="sel-option"
                    class:selected={opt.value === value}
                    role="option"
                    aria-selected={opt.value === value}
                    onclick={() => select(opt)}
                >
                    <span class="opt-label" title={opt.label}>{opt.label}</span>
                    {#if opt.meta}
                        <span class="opt-meta">{opt.meta}</span>
                    {/if}
                </button>
            {/each}
        </div>
    {/if}
</div>

<style>
    .diff-select-wrapper {
        position: relative;
        flex: 1;
        min-width: 0;
    }
    .diff-select-btn {
        width: 100%;
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 3px 24px 3px 8px;
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-weight: 500;
        background: var(--color-surface-3);
        color: var(--color-text-secondary);
        border: 1px solid var(--color-border);
        cursor: pointer;
        transition: all 0.15s ease;
        font-family: var(--font-mono);
        text-align: left;
        min-height: 1.6rem;
    }
    .diff-select-btn:hover {
        background: var(--color-surface-4);
        color: var(--color-text-primary);
    }
    .diff-select-btn:focus {
        outline: none;
        border-color: var(--color-accent);
    }
    .sel-label {
        flex: 1;
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        text-align: left;
    }
    .sel-meta {
        flex-shrink: 0;
        color: var(--color-text-tertiary);
        font-size: 0.7rem;
        text-align: right;
    }
    .sel-chevron {
        position: absolute;
        right: 6px;
        top: 50%;
        transform: translateY(-50%);
        color: var(--color-text-tertiary);
        pointer-events: none;
    }
    .sel-dropdown {
        position: absolute;
        top: calc(100% + 2px);
        left: 0;
        right: 0;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        max-height: 240px;
        overflow-y: auto;
        z-index: 1000;
        box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
        padding: 2px;
    }
    .sel-option {
        display: flex;
        align-items: center;
        gap: 6px;
        width: 100%;
        padding: 4px 8px;
        border-radius: 3px;
        font-size: 0.75rem;
        background: none;
        border: none;
        color: var(--color-text-secondary);
        cursor: pointer;
        transition: all 0.1s ease;
        text-align: left;
        font-family: var(--font-mono);
    }
    .sel-option:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .sel-option.selected {
        background: color-mix(in srgb, var(--color-accent) 8%, transparent);
        color: var(--color-accent);
    }
    .opt-label {
        flex: 1;
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        text-align: left;
    }
    .opt-meta {
        flex-shrink: 0;
        color: var(--color-text-tertiary);
        font-size: 0.7rem;
        text-align: right;
    }
</style>
