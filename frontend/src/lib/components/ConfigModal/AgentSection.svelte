<script lang="ts">
    import { marked } from 'marked';
    import DOMPurify from 'dompurify';

    let { description = '', setupInfo = '' } = $props();

    let descriptionHtml = $derived.by(() => {
        if (!description) return '';
        return DOMPurify.sanitize(marked.parse(description, { async: false }));
    });

    let setupHtml = $derived.by(() => {
        if (!setupInfo) return '';
        return DOMPurify.sanitize(marked.parse(setupInfo, { async: false }));
    });
</script>

{#if description || setupInfo}
    <div class="agent-info-section">
        {#if description}
            <div class="agent-info-text">
                {@html descriptionHtml}
            </div>
        {/if}
        {#if setupInfo}
            <div class="agent-info-box">
                <h4 class="agent-info-box-title">Setup</h4>
                {@html setupHtml}
            </div>
        {/if}
    </div>
{/if}

<style>
    .agent-info-section {
        padding: 12px;
        background: var(--color-surface-2);
        border-radius: var(--radius-md);
    }
    .agent-info-text {
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        line-height: 1.5;
        margin: 0 0 12px;
    }
    .agent-info-text :global(p) {
        margin: 0 0 0.5em;
    }
    .agent-info-text :global(p:last-child) {
        margin-bottom: 0;
    }
    .agent-info-box {
        background: var(--color-surface-1);
        border-radius: var(--radius-sm);
        padding: 10px 12px;
        border: 1px solid var(--color-border);
    }
    .agent-info-box-title {
        font-size: 0.75rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: var(--color-text-secondary);
        margin: 0 0 8px;
    }
    .agent-info-box :global(ul) {
        margin: 0;
        padding-left: 16px;
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        line-height: 1.6;
    }
    .agent-info-box :global(li) {
        margin-bottom: 4px;
    }
    .agent-info-box :global(code) {
        background: var(--color-surface-2);
        padding: 1px 4px;
        border-radius: 3px;
        font-size: 0.75rem;
    }
    .agent-info-box :global(strong) {
        font-weight: 600;
    }
</style>
