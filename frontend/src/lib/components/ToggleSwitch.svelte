<script lang="ts">
    let { checked = $bindable(false), disabled = false, ariaLabel = '', ariaDescribedby = '', onChange = () => {}, class: className = '' }: {
        checked: boolean;
        disabled?: boolean;
        ariaLabel?: string;
        ariaDescribedby?: string;
        onChange?: () => void;
        class?: string;
    } = $props();
</script>

<button
    type="button"
    class="toggle-switch {className}"
    class:active={checked}
    onclick={(e) => { e.stopPropagation(); if (!disabled) { checked = !checked; onChange(); } }}
    role="switch"
    aria-checked={checked}
    aria-label={ariaLabel}
    aria-describedby={ariaDescribedby}
    disabled={disabled}
>
    <span class="toggle-knob"></span>
</button>

<style>
    .toggle-switch {
        position: relative;
        width: 40px;
        height: 22px;
        border-radius: var(--radius-lg);
        background: var(--color-surface-4);
        border: none;
        flex-shrink: 0;
        transition: background var(--dur-md) ease;
    }

    .toggle-switch.active {
        background: var(--color-accent);
    }

    .toggle-knob {
        position: absolute;
        top: 3px;
        left: 3px;
        width: 16px;
        height: 16px;
        border-radius: 50%;
        background: var(--color-toggle-knob);
        transition: transform var(--dur-md) ease;
    }

    .toggle-switch.active .toggle-knob {
        transform: translateX(18px);
    }

    .toggle-switch:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: 2px;
    }
</style>
