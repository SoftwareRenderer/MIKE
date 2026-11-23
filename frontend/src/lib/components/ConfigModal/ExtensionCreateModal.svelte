<script lang="ts">
    import type { Project, Task } from '$lib/types';
    import { t } from '$lib/i18n';
    import BaseModal from '../BaseModal.svelte';
    import Spinner from '../Spinner.svelte';
    import { scaffoldExtension } from '$lib/api';
    import ExtensionCreateSandboxStep from './ExtensionCreateSandboxStep.svelte';
    import {
        extensionCreateSandboxOptions,
        validateExtensionCreateDraft,
        type ExtensionCreateErrors,
    } from '$lib/utils/extension-create-draft';

    /**
     * The one form that makes a custom extension: a name and what it should do. The server makes the
     * repository, the project and the first task, and the agent writes the files. A refusal is shown in
     * the server's own words, because it names the path it could not write.
     */
    let { oncreated, onclose }: {
        /** The rows the create made; only the create knows the task's id before the board holds it. */
        oncreated?: (project: Project, task: Task) => void;
        onclose: () => void;
    } = $props();

    let name = $state('');
    let description = $state('');
    let touched = $state(false);
    let busy = $state(false);
    let failure = $state('');
    /** Only the sandbox step's Create makes anything. */
    let step = $state<'form' | 'sandbox'>('form');
    let allowNetwork = $state(false);
    let defaultCapabilities = $state(false);

    const errors = $derived<ExtensionCreateErrors>(
        validateExtensionCreateDraft({ name, description }));
    const submittable = $derived(!errors.name && !errors.description);

    function toSandboxStep(e: SubmitEvent) {
        e.preventDefault();
        touched = true;
        if (!submittable) return;
        failure = '';
        step = 'sandbox';
    }

    async function create() {
        busy = true;
        failure = '';
        try {
            const made = await scaffoldExtension(
                name.trim(),
                description.trim(),
                extensionCreateSandboxOptions({ allowNetwork, defaultCapabilities }),
            );
            oncreated?.(made.project, made.task);
        } catch (err) {
            // The step stays up with the server's sentence, so the person can press again.
            failure = err instanceof Error ? err.message : String(err);
            busy = false;
        }
    }
</script>

{#snippet footer()}
    {#if step === 'form'}
        <button type="button" class="btn" onclick={onclose}>{t('app.cancel')}</button>
        <button type="submit" form="extension-create-form" class="btn btn-primary" disabled={!submittable}>
            {t('settings.extensions.createSubmit')}
        </button>
    {:else}
        <button type="button" class="btn" onclick={() => { step = 'form'; }} disabled={busy}>
            {t('settings.extensions.createBack')}
        </button>
        <button type="button" class="btn btn-primary" onclick={create} disabled={busy}>
            {#if busy}
                <Spinner decorative /> {t('settings.extensions.creating')}
            {:else}
                {t('settings.extensions.createSubmit')}
            {/if}
        </button>
    {/if}
{/snippet}

<BaseModal
    title={step === 'form'
        ? t('settings.extensions.createTitle')
        : t('settings.extensions.createSandboxTitle')}
    width="520px"
    modalClass="extension-create-modal"
    footer={footer}
    onClose={onclose}
>
{#if step === 'form'}
    <form id="extension-create-form" class="create-body" onsubmit={toSandboxStep}>
        <p class="create-intro">{t('settings.extensions.createIntro')}</p>
        <label class="create-field">
            <span class="create-label">{t('settings.extensions.createNameLabel')}</span>
            <input
                type="text"
                name="name"
                class="create-input"
                bind:value={name}
                autocomplete="off"
                aria-invalid={touched && !!errors.name}
                placeholder={t('settings.extensions.createNamePlaceholder')}
            />
        </label>
        {#if touched && errors.name}
            <span class="create-error">{t(errors.name)}</span>
        {/if}

        <label class="create-field">
            <span class="create-label">{t('settings.extensions.createDescriptionLabel')}</span>
            <textarea
                name="description"
                class="create-input create-textarea"
                rows="5"
                bind:value={description}
                aria-invalid={touched && !!errors.description}
                placeholder={t('settings.extensions.createDescriptionPlaceholder')}
            ></textarea>
        </label>
        {#if touched && errors.description}
            <span class="create-error">{t(errors.description)}</span>
        {/if}

        <p class="create-note">{t('settings.extensions.createNote')}</p>
    </form>
{:else}
    <ExtensionCreateSandboxStep bind:allowNetwork bind:defaultCapabilities disabled={busy} />
    {#if failure}
        <p class="create-error" role="alert">{failure}</p>
    {/if}
{/if}
</BaseModal>

<style>
    .create-body {
        display: flex;
        flex-direction: column;
        gap: 0.5rem;
    }

    .create-intro,
    .create-note {
        font-size: var(--text-sm);
        color: var(--color-text-secondary);
        margin: 0;
    }

    .create-field {
        display: flex;
        flex-direction: column;
        gap: 0.25rem;
    }

    .create-label {
        font-size: var(--text-sm);
        font-weight: 600;
        color: var(--color-text-primary);
    }

    .create-input {
        background: var(--color-bg-input);
        border: 1px solid var(--color-border-subtle);
        border-radius: var(--radius-md);
        padding: 0.5rem 0.75rem;
        color: var(--color-text-primary);
        font-size: var(--text-base);
        font-family: inherit;
    }

    .create-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }

    .create-input[aria-invalid='true'] {
        border-color: var(--color-status-red);
    }

    .create-textarea {
        resize: vertical;
        min-height: 5rem;
    }

    .create-error {
        font-size: var(--text-xs);
        color: var(--color-status-red);
        margin: 0;
        overflow-wrap: anywhere;
    }</style>
