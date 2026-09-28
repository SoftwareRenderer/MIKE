<!--
    The one "working…" ring. It draws the shared `spin` keyframe from app.css — the only
    definition of it in the tree — as a ring in `currentColor`, so it matches the text
    it sits in and carries no colour of its own.

    `size` is the ring's diameter and comes from the 12/14/16/20 ladder the chrome uses;
    a number off that ladder is a second opinion rather than a measurement.

    `label` is already-translated text — the caller owns the phrase, this component never
    calls `t`. It is announced through `role="status"`, never `aria-live`: a ring that
    mounts and unmounts would make a screen reader re-read the state on every appearance.
    Pass `decorative` when adjacent text, or the containing control's own name, already
    says what is happening — then the ring announces nothing and that text owns it.
-->
<script lang="ts">
    interface Props {
        /** Ring diameter in px; the ladder is 12 / 14 / 16 / 20. */
        size?: number;
        /** Already-translated text, announced unless `decorative`. */
        label?: string;
        /** True when surrounding text or the control's own name already says what is happening. */
        decorative?: boolean;
    }

    let { size = 14, label, decorative = false }: Props = $props();

    // The unit is written after the `}`, so the attribute completes a number and a caller
    // cannot reach CSS as text.
    let diameter = $derived(Math.round(Number(size)) || 14);
</script>

{#if decorative}
    <span class="spinner" aria-hidden="true" style="--spinner-size: {diameter}px"></span>
{:else}
    <span class="spinner" role="status" style="--spinner-size: {diameter}px">
        <span class="sr-only">{label}</span>
    </span>
{/if}

<style>
    .spinner {
        display: inline-block;
        width: var(--spinner-size);
        height: var(--spinner-size);
        border: 2px solid currentColor;
        border-top-color: transparent;
        border-radius: 50%;
        animation: spin 0.6s linear infinite;
        /* A ring in a flex row must never be squashed into an ellipse. */
        flex-shrink: 0;
    }
</style>
