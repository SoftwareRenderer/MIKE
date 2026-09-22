<script lang="ts">
    /**
     * Branch text field with a filtered dropdown; the blur close is delayed because clicking a row
     * blurs the input first.
     */
    let { value = $bindable(''), branches = [], placeholder = '' }: {
        value?: string;
        branches?: string[];
        placeholder?: string;
    } = $props();

    let open = $state(false);
    let activeIndex = $state(0);

    let filtered = $derived.by(() => {
        const query = value.trim().toLowerCase();
        if (!query) return branches;
        return branches.filter(b => b.toLowerCase().includes(query));
    });

    function openDropdown() {
        open = true;
        activeIndex = 0;
    }

    function closeDropdown() {
        open = false;
        activeIndex = 0;
    }

    function handleKeydown(e: KeyboardEvent) {
        if (!open || filtered.length === 0) return;
        if (e.key === 'ArrowDown') {
            e.preventDefault();
            activeIndex = (activeIndex + 1) % filtered.length;
        } else if (e.key === 'ArrowUp') {
            e.preventDefault();
            activeIndex = (activeIndex - 1 + filtered.length) % filtered.length;
        } else if (e.key === 'Enter' && activeIndex >= 0 && activeIndex < filtered.length) {
            e.preventDefault();
            value = filtered[activeIndex];
            closeDropdown();
        } else if (e.key === 'Escape') {
            closeDropdown();
        }
    }

    function select(branch: string) {
        value = branch;
        closeDropdown();
    }
</script>

<div class="branch-input-wrapper">
    <input
        type="text"
        bind:value
        {placeholder}
        class="branch-input"
        oninput={openDropdown}
        onkeydown={handleKeydown}
        onfocus={openDropdown}
        onblur={() => setTimeout(() => closeDropdown(), 150)}
    />
    {#if open && filtered.length > 0}
        <ul class="branch-dropdown" role="listbox">
            {#each filtered as branch, i (branch)}
                <li
                    class="branch-option {i === activeIndex ? 'active' : ''}"
                    role="option"
                    aria-selected={i === activeIndex}
                    onmousedown={(e) => { e.preventDefault(); select(branch); }}
                >{branch}</li>
            {/each}
        </ul>
    {/if}
</div>

<style>
    .branch-input-wrapper {
        position: relative;
    }
    .branch-input {
        width: 160px;
        font-size: 0.8125rem;
        color: var(--color-text-primary);
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 5px 8px;
        font-family: var(--font-mono, monospace);
        outline: none;
        text-align: right;
    }
    .branch-input:focus {
        border-color: var(--color-accent);
    }
    .branch-dropdown {
        position: absolute;
        z-index: 100;
        max-height: 200px;
        overflow-y: auto;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        box-shadow: 0 4px 12px rgba(0, 0, 0, 0.25);
        margin: 0;
        padding: 0;
        list-style: none;
        min-width: 160px;
    }
    .branch-option {
        padding: 6px 10px;
        font-size: 0.8125rem;
        font-family: var(--font-mono, monospace);
        color: var(--color-text-primary);
        cursor: pointer;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .branch-option:hover,
    .branch-option.active {
        background: var(--color-surface-3);
        color: var(--color-accent);
    }
</style>
