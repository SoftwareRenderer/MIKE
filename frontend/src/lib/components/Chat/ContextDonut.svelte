<script lang="ts">
    interface Props {
        used?: number;
        limit?: number;
        size?: number;
    }

    let { used = 0, limit = 0, size = 20 }: Props = $props();

    let percentage = $derived(limit > 0 ? Math.round((used / limit) * 100) : 0);
    let isWarning = $derived(percentage > 70 && percentage <= 80);
    let isOrange = $derived(percentage > 80 && percentage < 90);
    let isDanger = $derived(percentage >= 90);

    // Donut geometry: ring starts full and depletes as context is used
    let RADIUS = $derived(size * 0.35);
    let CIRCUMFERENCE = $derived(2 * Math.PI * RADIUS);
    let CENTER = $derived(size / 2);
    let STROKE = $derived(size * 0.3);
    let remainingPct = $derived(Math.max(0, Math.min(100, 100 - percentage)));
    let donutOffset = $derived(CIRCUMFERENCE * (1 - remainingPct / 100));

    function getBarColor(): string {
        if (isDanger) return 'var(--color-status-red)';
        if (isOrange) return 'var(--color-status-orange-dark)';
        if (isWarning) return 'var(--color-status-amber, #f59e0b)';
        return 'var(--color-status-green, #3fb950)';
    }
</script>

<svg
    class="donut"
    style="--stroke: {STROKE}"
    width="{size}"
    height="{size}"
    viewBox="0 0 {size} {size}"
    role="img"
    aria-hidden="true"
>
    <circle class="donut-track" cx="{CENTER}" cy="{CENTER}" r="{RADIUS}" />
    <circle
        class="donut-fill"
        cx="{CENTER}"
        cy="{CENTER}"
        r="{RADIUS}"
        stroke="{getBarColor()}"
        stroke-dasharray="{CIRCUMFERENCE}"
        stroke-dashoffset="{donutOffset}"
        transform="rotate(-90 {CENTER} {CENTER})"
    />
</svg>

<style>
    .donut {
        display: block;
        flex-shrink: 0;
        /* Flip horizontally so the ring depletes counter-clockwise */
        transform: scaleX(-1);
    }
    .donut-track {
        fill: none;
        stroke: var(--color-surface-4);
        stroke-width: var(--stroke);
    }
    .donut-fill {
        fill: none;
        stroke-width: var(--stroke);
        stroke-linecap: butt;
        transition: stroke 0.3s ease, stroke-dashoffset 0.3s ease;
    }
</style>
