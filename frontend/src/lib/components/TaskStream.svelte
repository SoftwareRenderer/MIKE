
<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import type { StreamEvent, StreamSection } from '$lib/types';
    import { getToolDisplayName } from '$lib/types';
    import { getTaskEventContext } from '$lib/context';
    import { estimateTokens } from '$lib/services/stream-handler';
    import StreamSectionComp from './StreamSection.svelte';
    import StreamActions from './StreamActions.svelte';

    let { projectId, taskId }: { projectId: string; taskId: string } = $props();

    let sections: StreamSection[] = $state([]);
    let connected = $state(false);
    let connecting = $state(true);
    let fullText = $state('');
    let showRaw = $state(false);
    let rawFrames: string[] = $state([]);
    let tokenStats = $state({ prompt: 0, completed: 0, total: 0, duration: 0 });
    let scrollEl: HTMLElement | null = null;
    let atBottom = $state(true);
    let taskEventContext = getTaskEventContext();
    let prevScrollHeight = $state(0);

    let currentTextSection: StreamSection | null = $state(null);
    let currentReasoningSection: StreamSection | null = $state(null);
    let activeToolCalls = $state<Map<string, StreamSection>>(new Map());

    function genId() {
        return Math.random().toString(36).slice(2, 10);
    }

    /** Split raw text into paragraphs: `\n\n` → paragraph break, `\n` → <br> */
    function splitParagraphs(text: string): string[] {
        if (!text) return [];
        return text.split(/\n\n+/).map(p => p.trim()).filter(Boolean);
    }

    /** Escape HTML and convert single newlines to <br> */
    function escapeHtml(str: string): string {
        return str
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/\n/g, '<br>');
    }

    function ensureTextSection() {
        if (!currentTextSection) {
            currentTextSection = { id: genId(), type: 'text', content: '', isStreaming: true };
            sections.push(currentTextSection);
        }
        return currentTextSection;
    }

    function ensureReasoningSection() {
        if (!currentReasoningSection) {
            currentReasoningSection = { id: genId(), type: 'reasoning', content: '', isStreaming: true, tokenCount: 0 };
            sections.push(currentReasoningSection);
        }
        return currentReasoningSection;
    }

    function handleEvent(event: StreamEvent) {
        switch (event.type) {
            case 'token': {
                const data = event.data as { text: string };
                currentReasoningSection = null;
                const sec = ensureTextSection();
                sec.content += data.text;
                sec.isStreaming = true;
                fullText += data.text;
                break;
            }
            case 'reasoning': {
                const data = event.data as { text: string };
                currentTextSection = null;
                const sec = ensureReasoningSection();
                sec.content += data.text;
                sec.tokenCount = (sec.tokenCount || 0) + estimateTokens(data.text);
                sec.isStreaming = true;
                break;
            }
            case 'reasoning_end': {
                const data = event.data as { summary: string };
                if (currentReasoningSection) {
                    currentReasoningSection.isStreaming = false;
                    currentReasoningSection.reasoningSummary = data.summary;
                    currentReasoningSection = null;
                }
                break;
            }
            case 'tool_call': {
                const data = event.data as { tool_id: string; tool_name: string; tool_display_name?: string; args: Record<string, unknown>; sandbox?: boolean };
                currentTextSection = null;
                currentReasoningSection = null;
                // Skip incomplete tool call deltas (empty tool_id from streaming fragments)
                if (!data.tool_id || data.tool_id === '') {
                    break;
                }
                const sec: StreamSection = {
                    id: genId(),
                    type: 'tool_call',
                    content: '',
                    isStreaming: true,
                    toolCall: { toolId: data.tool_id, toolName: data.tool_name, displayName: data.tool_display_name, args: data.args, sandbox: data.sandbox }
                };
                sections.push(sec);
                activeToolCalls.set(data.tool_id, sec);
                break;
            }
            case 'tool_result': {
                const data = event.data as { tool_id: string; success: boolean; output: string; duration: number };
                const sec = activeToolCalls.get(data.tool_id);
                activeToolCalls.delete(data.tool_id);
                if (sec && sec.toolCall) {
                    // Create a completely new section object to force Svelte 5 reactivity
                    const updatedSec: StreamSection = {
                        ...sec,
                        isStreaming: false,
                        toolCall: {
                            ...sec.toolCall,
                            result: data.output,
                            success: data.success,
                            duration: data.duration
                        }
                    };
                    const idx = sections.findIndex(s => s.id === sec.id);
                    if (idx !== -1) {
                        sections = sections.map((s, i) => i === idx ? updatedSec : s);
                    }
                } else {
                    sections = [...sections];
                }
                break;
            }
            case 'complete': {
                const data = event.data as { state: string; summary: string; tokens: { prompt: number; completed: number; total: number }; duration: number };
                tokenStats = { ...data.tokens, duration: data.duration };
                if (data.summary) fullText += '\n\n' + data.summary;
                if (currentTextSection) currentTextSection.isStreaming = false;
                if (currentReasoningSection) {
                    currentReasoningSection.isStreaming = false;
                }
                connected = false;
                break;
            }
            case 'error': {
                const data = event.data as { message: string };
                if (currentTextSection) currentTextSection.isStreaming = false;
                if (currentReasoningSection) currentReasoningSection.isStreaming = false;
                connected = false;
                break;
            }
        }
        scroll();
    }

    function scroll() {
        if (scrollEl && atBottom) {
            scrollEl.scrollTop = scrollEl.scrollHeight;
        }
    }

    function checkScroll() {
        if (scrollEl) {
            const gap = scrollEl.scrollHeight - scrollEl.scrollTop - scrollEl.clientHeight;
            if (scrollEl.scrollHeight < prevScrollHeight) {
                // Content shrank (e.g., section collapsed).
                // Check if user was near the bottom BEFORE the shrink.
                const prevGap = prevScrollHeight - scrollEl.scrollTop - scrollEl.clientHeight;
                atBottom = prevGap < 32;
            } else {
                atBottom = gap < 32;
            }
            prevScrollHeight = scrollEl.scrollHeight;
        }
    }

    function toggleRaw() {
        showRaw = !showRaw;
    }

    function copyOutput() {
        navigator.clipboard.writeText(fullText);
    }

    function collapseAll() {
        // Just a visual hint — handled by StreamSection
    }

    onMount(() => {
        connecting = false;
        connected = true;
        const unsub = taskEventContext.subscribe((event) => {
            if (event && event.task_id === taskId) {
                rawFrames.push(JSON.stringify(event));
                handleEvent({ type: event.type as StreamEvent['type'], data: event.data });
            }
        });
        return () => unsub();
    });

    onDestroy(() => {
        connected = false;
    });
</script>

<div class="task-stream">
    <!-- Content -->
    <div class="content" bind:this={scrollEl} onscroll={checkScroll}>
        {#if showRaw}
            <pre class="raw">{rawFrames.join('\n')}</pre>
        {:else}
            {#each sections as sec (sec.id)}
                {#snippet children()}
                    {#if sec.type === 'text'}
                        <div class="stream-text" class:streaming={sec.isStreaming}>
                            {#each splitParagraphs(sec.content) as para}
                                <p>{@html escapeHtml(para)}</p>
                            {/each}
                            {#if sec.isStreaming}
                                <span class="cursor-blink"></span>
                            {/if}
                        </div>
                    {:else if sec.type === 'reasoning'}
                        <div class="stream-reasoning">{sec.content}</div>
                    {:else if sec.type === 'tool_call' && sec.toolCall}
                        <div class="tool-call-card" style="border-left-color: {sec.isStreaming ? 'var(--color-accent)' : (sec.toolCall.success ? 'var(--color-status-green)' : 'var(--color-status-red)')}">
                            <div class="tool-call-header">
                                <span class="tool-name" title={sec.toolCall.toolName}>{getToolDisplayName(sec.toolCall.displayName, sec.toolCall.toolName)}</span>
                                {#if sec.isStreaming}
                                    <span class="spinner">⏳</span>
                                {:else if sec.toolCall.success}
                                    <span class="success">✓</span>
                                {:else}
                                    <span class="error">✗</span>
                                {/if}
                                {#if sec.toolCall.duration !== undefined}
                                    <span class="duration">{sec.toolCall.duration}ms</span>
                                {/if}
                            </div>
                            <pre class="tool-args">{JSON.stringify(sec.toolCall.args, null, 2)}</pre>
                            {#if sec.toolCall.result && sec.toolCall.result.trim()}
                                <pre class="tool-result" class:success={sec.toolCall.success} class:failure={!sec.toolCall.success}>{sec.toolCall.result}</pre>
                            {/if}
                        </div>
                    {/if}
                {/snippet}
                <StreamSectionComp
                    type={sec.type}
                    collapsible={sec.type === 'reasoning' && !sec.isStreaming}
                    summary={sec.reasoningSummary}
                    tokenCount={sec.tokenCount}
                    {children}
                />
            {/each}
        {/if}
    </div>

    <!-- Actions -->
    {#if fullText}
        <StreamActions
            onCopy={copyOutput}
            onToggleRaw={toggleRaw}
            tokens={tokenStats}
        />
    {/if}
</div>

<style>
    .task-stream {
        display: flex;
        flex-direction: column;
        height: 100%;
        overflow: hidden;
    }
    @keyframes pulse {
        0%, 100% { opacity: 1; }
        50% { opacity: 0.3; }
    }
    .content {
        flex: 1;
        overflow-y: auto;
        padding: 8px 0;
    }
    .stream-text {
        font-size: 0.875rem;
        line-height: 1.7;
        color: var(--color-text-primary);
    }
    .stream-text p {
        margin: 0.75rem 0;
        line-height: 1.7;
    }
    .stream-text p:first-child {
        margin-top: 0;
    }
    .stream-text p:last-child {
        margin-bottom: 0;
    }
    .stream-text.streaming {
        border-left: 2px solid var(--color-accent);
        padding-left: 12px;
    }
    .stream-text .cursor-blink {
        display: inline-block;
        width: 2px;
        height: 1em;
        background: var(--color-accent);
        vertical-align: text-bottom;
        animation: blink 0.8s step-end infinite;
    }
    @keyframes blink {
        0%, 100% { opacity: 1; }
        50% { opacity: 0; }
    }
    .stream-reasoning {
        font-size: 0.875rem;
        line-height: 1.5;
        font-style: italic;
        color: var(--color-text-tertiary);
        white-space: pre-wrap;
    }
    .tool-call-card {
        background: var(--color-surface-2);
        border-radius: var(--radius-md);
        border-left: 3px solid;
        padding: 8px 12px;
    }
    .tool-call-header {
        display: flex;
        align-items: center;
        gap: 8px;
        margin-bottom: 8px;
    }
    .tool-name {
        font-weight: 600;
        font-size: 0.875rem;
        color: var(--color-accent);
        display: flex;
        align-items: center;
    }
    .spinner { color: var(--color-status-yellow); }
    .success { color: var(--color-status-green); }
    .error { color: var(--color-status-red); }
    .duration {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        margin-left: auto;
    }
    .tool-args {
        background: var(--color-surface-0);
        padding: 8px;
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-family: var(--font-mono);
        overflow-x: auto;
        margin: 0;
    }
    .tool-result {
        background: var(--color-surface-0);
        padding: 8px;
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-family: var(--font-mono);
        overflow-x: auto;
        margin: 4px 0 0;
        max-height: 150px;
        overflow-y: auto;
    }
    .raw {
        background: var(--color-surface-0);
        padding: 12px;
        font-size: 0.75rem;
        font-family: var(--font-mono);
        white-space: pre-wrap;
        margin: 0;
    }
</style>