<script lang="ts">
    import { t } from '$lib/i18n';
    import type { ConfigField } from '$lib/utils/extension-config';
    import type { ExtensionUrlProbe } from '$lib/api';

    /**
     * The addresses an extension's tools will send to. Each is one a person typed into the field above,
     * and the probe answers about the value in the field right now — an unsaved address included — so a
     * saved address is never the one that gets tested.
     *
     * The answer belongs to this dialog: nothing here is written to the record, and a failed request fills
     * the same slot as a successful one.
     */
    let { fields, probingKey, answers, ontest }: {
        fields: ConfigField[];
        probingKey: string;
        answers: Record<string, ExtensionUrlProbe>;
        ontest: (field: ConfigField) => void;
    } = $props();
</script>

{#if fields.length > 0}
    <div class="config-fields">
        <h4>{t('settings.extensions.connectivity')}</h4>
        {#each fields as field (field.key)}
            <div class="config-field">
                <div class="connectivity-row">
                    <span class="field-label">{field.title || field.key}</span>
                    <button
                        type="button"
                        class="btn-secondary btn-test"
                        onclick={() => ontest(field)}
                        disabled={probingKey !== ''}
                    >{probingKey === field.key ? t('settings.extensions.testingAddress') : t('settings.extensions.testAddress')}</button>
                </div>
                <p class="field-desc">{t('settings.extensions.connectivityDesc')}</p>
                {#if answers[field.key]}
                    <p class="probe-answer" class:probe-ok={answers[field.key].success} class:probe-fail={!answers[field.key].success} role="status">
                        {answers[field.key].message}
                    </p>
                {/if}
            </div>
        {/each}
    </div>
{/if}

<style>
    .field-label {
        display: block;
        font-size: var(--text-sm);
        color: var(--color-text-tertiary);
        margin-bottom: 0.25rem;
    }

    .field-desc {
        color: var(--color-text-tertiary);
        font-size: var(--text-xs);
        margin: 0;
    }

    .connectivity-row {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 12px;
    }

    .btn-test {
        flex: none;
    }

    .probe-answer {
        padding: 6px 10px;
        border-radius: var(--radius-sm);
        font-size: var(--text-xs);
        line-height: 1.45;
    }

    .probe-ok {
        background: rgba(34, 197, 94, 0.08);
        border: 1px solid rgba(34, 197, 94, 0.3);
        color: var(--color-status-green);
    }

    .probe-fail {
        background: rgba(248, 81, 73, 0.1);
        border: 1px solid rgba(248, 81, 73, 0.3);
        color: var(--color-text-secondary);
    }

    .btn-secondary {
        background: var(--color-btn-secondary-bg);
        border-color: var(--color-border-subtle);
        color: var(--color-text-primary);
    }

    .btn-secondary:hover:not(:disabled) {
        background: var(--color-border-subtle);
    }
</style>
