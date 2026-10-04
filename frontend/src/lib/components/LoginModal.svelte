<script lang="ts">
    /**
     * The login gate: a passkey assertion, or the pairing code that buys the first one.
     *
     * All the branching lives in `$lib/utils/login-flow.ts` — this file only paints the body that stage
     * asks for and forwards the clicks. What it does hold is the two things that are not decisions but
     * presentation: a ceremony in flight makes every control refuse a second click (two prompts on one
     * authenticator are one prompt the person cannot attribute), and a prompt the person dismissed is
     * shown as a sentence rather than in red.
     */
    import { onMount } from 'svelte';
    import { isPasskeyCancelled, registerPasskeyWithCode, signInWithPasskey } from '$lib/api';
    import { t } from '$lib/i18n';
    import {
        initialState,
        next,
        normalizePairingCode,
        pairingCodeFromHash,
        pairingCodeReady,
    } from '$lib/utils/login-flow';
    import type { LoginEvent, LoginState, LoginTone } from '$lib/utils/login-flow';
    import { passkeySupport } from '$lib/utils/webauthn';
    import BaseModal from './BaseModal.svelte';

    let { hasPasskey = null, onClose }: { hasPasskey?: boolean | null; onClose: () => void | Promise<void> } = $props();

    // svelte-ignore state_referenced_locally — the body this form opens on is decided once, when it is
    // created. The boot answer that feeds it is settled before the gate renders, and every later change
    // to it is a different screen.
    let form = $state<LoginState>(initialState(hasPasskey, passkeySupport()));
    let codeInput: HTMLInputElement | null = $state(null);

    /** A ceremony runs, so nothing else is clickable. */
    let busy = $derived(form.stage === 'ceremony');
    /** The body on screen. A ceremony keeps the body it started from, so the code field does not vanish
     *  out from under the person while the browser is still talking to the authenticator. */
    let body = $derived(form.stage === 'ceremony' ? form.entry : form.stage);
    /** Back only exists where there is somewhere to go back to: on a fresh install the code field is the
     *  form, and the machine's `back` returns it to itself. */
    let canGoBack = $derived(body === 'code' && form.hasPasskey !== false);
    let canPair = $derived(body === 'signin' && !busy);
    let codeAccepted = $derived(pairingCodeReady(form.code));

    function send(event: LoginEvent): void {
        form = next(form, event);
    }

    /** One place decides how a failure is coloured, because the rule is the same on both paths. */
    function report(e: unknown): void {
        const message = (e as Error)?.message || t('app.login.failed');
        const tone: LoginTone = isPasskeyCancelled(e) ? 'note' : 'error';
        send({ type: 'error', message, tone });
    }

    async function signIn(): Promise<void> {
        send({ type: 'start' });
        try {
            await signInWithPasskey();
            await onClose();
        } catch (e) {
            report(e);
        }
    }

    async function createPasskey(): Promise<void> {
        if (!codeAccepted) return;
        send({ type: 'start' });
        try {
            await registerPasskeyWithCode(normalizePairingCode(form.code));
            await onClose();
        } catch (e) {
            report(e);
        }
    }

    onMount(() => {
        // A pairing link opens the code field with the code already in it. The link is the only way a
        // second device gets a code it can type without someone reading it out, so this runs before
        // anything is asked of the person.
        const fromLink = pairingCodeFromHash(window.location.hash);
        if (fromLink) {
            send({ type: 'pair' });
            send({ type: 'code', value: fromLink });
            // The code is a credential until the ceremony spends it. Out of the address bar goes the
            // copy a bookmark, a screenshot or a later reload would keep showing.
            window.history.replaceState(window.history.state, '', window.location.pathname + window.location.search);
        }
    });

    // The code field takes focus whenever a body shows one: on a fresh install that is the first body,
    // and after a pairing link or the "I have a code" click it is the field that step inserted. The
    // microtask leaves it behind BaseModal's own focus-on-open, which chooses for the modal rather than
    // for the field this stage asked for.
    $effect(() => {
        if (codeInput) queueMicrotask(() => codeInput?.focus());
    });
</script>

{#snippet footer()}
    {#if form.support !== 'available'}
        <span class="login-hint">{t('app.login.unsupportedHint')}</span>
    {:else}
        <div class="login-footer">
            {#if canGoBack}
                <button type="button" class="btn login-secondary" onclick={() => send({ type: 'back' })}
                >{t('app.login.back')}</button>
            {/if}
            {#if body === 'code'}
                <button type="button" class="login-btn btn btn-primary" disabled={busy || !codeAccepted} onclick={createPasskey}>
                    {busy ? t('app.login.waiting') : t('app.login.createPasskey')}
                </button>
            {:else}
                <button type="button" class="login-btn btn btn-primary" disabled={busy} onclick={signIn}>
                    {busy ? t('app.login.waiting') : t('app.login.signInPasskey')}
                </button>
            {/if}
        </div>
    {/if}
{/snippet}

<BaseModal width="380px" modalClass="login-modal" overlayClass="login-modal-overlay" closeOnOverlayClick={false} footer={footer} onClose={() => { }} header={header}>
    {#if form.error}
        <div class="login-message" class:login-message-note={form.tone === 'note'} role={form.tone === 'note' ? 'status' : 'alert'}>
            {form.error}
        </div>
    {/if}

    {#if body === 'unsupported'}
        <p class="login-copy">{form.support === 'insecure-context' ? t('app.login.insecureContext') : t('app.login.notSupported')}</p>
        {#if form.support === 'insecure-context'}
            <p class="login-hint">{t('app.login.insecureHelp')}</p>
        {/if}
    {:else if body === 'code'}
        <p class="login-copy">{t('app.login.codeHelp')}</p>
        <input
            id="login-code"
            bind:this={codeInput}
            type="text"
            class="login-code"
            value={form.code}
            maxlength={11}
            spellcheck="false"
            autocomplete="one-time-code"
            autocapitalize="characters"
            placeholder={t('app.login.codePlaceholder')}
            aria-label={t('app.login.codeAria')}
            disabled={busy}
            onkeydown={(e) => { if (e.key === 'Enter') void createPasskey(); }}
            oninput={(e) => send({ type: 'code', value: e.currentTarget.value })}
        />
    {:else}
        <p class="login-copy">{t('app.login.signInHelp')}</p>
        {#if canPair}
            <button type="button" class="login-link" onclick={() => send({ type: 'pair' })}>{t('app.login.haveCode')}</button>
        {/if}
    {/if}
</BaseModal>

{#snippet header({ title: _title }: { title: string })}
    <div class="login-header">
        <h2>{body === 'code' ? t('app.login.pairTitle') : t('app.login.signIn')}</h2>
    </div>
{/snippet}

<style>
    :global(.overlay.login-modal-overlay) {
        backdrop-filter: blur(4px);
    }
    :global(.modal.login-modal) {
        padding: 4px;
    }
    :global(.login-modal .modal-body) {
        display: flex;
        flex-direction: column;
        gap: 12px;
    }

    .login-header {
        text-align: center;
    }

    .login-header h2 {
        text-align: center;
    }

    .login-copy {
        margin: 0;
        font-size: var(--text-base);
        line-height: 1.5;
        color: var(--color-text-secondary);
    }

    .login-hint {
        font-size: var(--text-xs);
        line-height: 1.5;
        color: var(--color-text-tertiary);
    }

    .login-message {
        background: var(--color-status-red-bg);
        color: var(--color-text-primary);
        padding: 8px 12px;
        border-radius: var(--radius-sm);
        font-size: var(--text-base);
        text-align: center;
    }

    /* A dismissed prompt is not a fault, so it borrows no red. */
    .login-message-note {
        background: var(--color-surface-2);
        color: var(--color-text-secondary);
    }

    input.login-code {
        width: 100%;
        padding: 10px 14px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        background: var(--color-surface-2);
        color: var(--color-text-primary);
        /* The code is two groups of four: proportional glyphs make a 2 look like a 7 and the person
           retypes a code they read off their own screen. */
        font-family: var(--font-mono, 'SF Mono', 'Cascadia Code', 'Fira Code', monospace);
        font-size: var(--text-lg);
        letter-spacing: 0.08em;
        text-align: center;
        text-transform: uppercase;
        outline: none;
        transition: border-color var(--dur-base);
        box-sizing: border-box;
    }

    input.login-code:focus {
        border-color: var(--color-accent);
    }

    input.login-code::placeholder {
        color: var(--color-text-tertiary);
        letter-spacing: 0.08em;
    }

    .login-link {
        align-self: center;
        background: none;
        border: none;
        padding: 0;
        color: var(--color-accent);
        font-family: inherit;
        font-size: var(--text-sm);
        text-decoration: underline;
        cursor: pointer;
    }

    .login-footer {
        display: flex;
        align-items: center;
        justify-content: flex-end;
        gap: 8px;
        width: 100%;
    }

    .login-btn {
        /* The footer asks for a full-width submit; the chip itself is `.btn .btn-primary`. */
        flex: 1;
        padding: 10px;
    }

    .login-secondary {
        padding: 10px;
    }
</style>
