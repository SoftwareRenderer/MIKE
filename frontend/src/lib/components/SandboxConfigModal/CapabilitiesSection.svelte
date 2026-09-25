<script lang="ts">
    /**
     * Linux capabilities added to the container. Everything is dropped first (`--cap-drop ALL`) and
     * what is granted here is granted on top, so the list is the whole of what a container may do
     * beyond its defaults. The NAME is the machine value and never translates; only the line beside
     * it in the suggestion list does.
     */
    import { t } from '$lib/i18n';
    import type { MessageKey } from '$lib/i18n';
    import ExpandableSection from './ExpandableSection.svelte';
    import type { SandboxDraftOwner } from './draft.svelte';

    let { draft, locked }: { draft: SandboxDraftOwner; locked: boolean } = $props();

    /** The one description per capability name. */
    function capDesc(value: string): string {
        return t(`sandbox.cap.${value}` as MessageKey);
    }

    function handleCapInputFocus() {
        draft.capDropdownOpen = true;
    }

    function handleCapInputBlur() {
        // Delay to allow click on suggestion
        setTimeout(() => { draft.capDropdownOpen = false; }, 150);
    }

    function handleCapInputClick() {
        draft.capDropdownOpen = true;
    }
</script>

<ExpandableSection contentClass="caps-content" bind:expanded={draft.capsExpanded}
                   label={t('sandbox.capabilities')} desc={t('sandbox.capsDesc')} ariaLabel={t('sandbox.capsAria')}
                   icon={capsIcon}>
    <div class="caps-input-wrapper">
        {#if draft.capabilities.length > 0}
            <div class="caps-tags">
                {#each draft.capabilities as cap}
                    <span class="caps-tag" title={cap}>
                        <span class="caps-tag-text">{cap}</span>
                        {#if !locked}
                            <button type="button" class="caps-tag-remove" onclick={() => draft.removeCapability(cap)} title={t('sandbox.removeCapability', { cap })} aria-label={t('sandbox.removeCapability', { cap })}>
                                <svg width="10" height="10" viewBox="0 0 10 10" fill="none" aria-hidden="true">
                                    <path d="M2 2L8 8M8 2L2 8" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
                                </svg>
                            </button>
                        {/if}
                    </span>
                {/each}
            </div>
        {/if}
        <div class="caps-input-field" class:focus={draft.capDropdownOpen}>
            <input
                type="text"
                class="caps-input"
                bind:value={draft.capInputValue}
                oninput={() => draft.handleCapInputChange()}
                onkeydown={(e) => draft.handleCapInputKeydown(e)}
                onfocus={handleCapInputFocus}
                onblur={handleCapInputBlur}
                onclick={handleCapInputClick}
                placeholder={draft.capabilities.length === 0 ? t('sandbox.capSuggestionPlaceholder') : t('sandbox.capAddMorePlaceholder')}
                disabled={locked}
                autocomplete="off"
                spellcheck="false"
                aria-label={t('sandbox.addCapabilityAria')}
                role="combobox"
                aria-expanded={draft.capDropdownOpen}
                aria-controls="caps-dropdown-list"
            />
            {#if draft.capDropdownOpen && draft.capSuggestions.length > 0}
                <div id="caps-dropdown-list" class="caps-dropdown" role="listbox">
                    {#each draft.capSuggestions as suggestion, i}
                        <button type="button" class="caps-dropdown-item" class:selected={i === draft.capSelectedIndex} role="option" aria-selected={i === draft.capSelectedIndex} onclick={() => draft.addCapability(suggestion)}>
                            <span class="caps-dropdown-name">{suggestion}</span>
                            <span class="caps-dropdown-desc">{capDesc(suggestion)}</span>
                        </button>
                    {/each}
                </div>
            {/if}
        </div>
    </div>
</ExpandableSection>

{#snippet capsIcon()}
    <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
        <path d="M10 2l2.2 4.5 5 .7-3.6 3.5.9 5L10 13.3 5.5 15.7l.9-5L2.8 7.2l5-.7L10 2z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"/>
    </svg>
{/snippet}

<style>
    @import '$lib/styles/sandbox-config.css';

    .caps-input-wrapper {
        display: flex;
        flex-direction: column;
        gap: 6px;
    }

    /* Selected tags row */
    .caps-tags {
        display: flex;
        flex-wrap: wrap;
        gap: 4px;
    }

    .caps-tag {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        padding: 2px 4px 2px 8px;
        background: rgba(136, 192, 208, 0.12);
        border: 1px solid rgba(136, 192, 208, 0.25);
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
        color: var(--color-accent-light, #88c0d0);
    }

    .caps-tag-text {
        max-width: 180px;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .caps-tag-remove {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 14px;
        height: 14px;
        padding: 0;
        border: none;
        background: transparent;
        color: inherit;
        cursor: pointer;
        border-radius: 50%;
        opacity: 0.6;
        transition: opacity 0.15s, background 0.15s;
    }

    .caps-tag-remove:hover {
        opacity: 1;
        background: rgba(136, 192, 208, 0.2);
    }

    /* Input field */
    .caps-input-field {
        position: relative;
    }

    .caps-input {
        width: 100%;
        padding: 6px 10px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        font-size: 0.8125rem;
        font-family: var(--font-mono, monospace);
        outline: none;
        transition: border-color 0.15s;
        box-sizing: border-box;
    }

    .caps-input::placeholder {
        color: var(--color-text-secondary);
        font-family: var(--font-sans, system-ui, sans-serif);
    }

    .caps-input:focus,
    .caps-input-field:focus .caps-input {
        border-color: var(--color-accent, #88c0d0);
    }

    .caps-input:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    /* Suggestions dropdown */
    .caps-dropdown {
        position: absolute;
        top: 100%;
        left: 0;
        right: 0;
        margin-top: 2px;
        max-height: 200px;
        overflow-y: auto;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
        z-index: 100;
    }

    .caps-dropdown-item {
        display: flex;
        flex-direction: column;
        align-items: flex-start;
        width: 100%;
        padding: 6px 10px;
        border: none;
        background: transparent;
        color: var(--color-text-primary);
        cursor: pointer;
        text-align: left;
        transition: background 0.1s;
    }

    .caps-dropdown-item:hover,
    .caps-dropdown-item.selected {
        background: var(--color-surface-2);
    }

    .caps-dropdown-name {
        font-size: 0.8125rem;
        font-weight: 500;
        font-family: var(--font-mono, monospace);
    }

    .caps-dropdown-desc {
        font-size: 0.6875rem;
        color: var(--color-text-secondary);
    }
</style>
