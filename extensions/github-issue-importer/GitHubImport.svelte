<script lang="ts">
    import { onMount } from 'svelte';

    let { prompt = $bindable(), setPrompt, config } = $props();
    let loading = $state(false);
    let error = $state('');

    async function handlePaste(e: ClipboardEvent) {
        const text = e.clipboardData?.getData('text') || '';
        const githubIssueRegex = /^https:\/\/github\.com\/[^\/]+\/[^\/]+\/issues\/[0-9]+$/;
        if (githubIssueRegex.test(text.trim())) {
            e.preventDefault();
            loading = true;
            error = '';
            try {
                const url = text.trim();
                const match = url.match(/^https:\/\/github\.com\/([^\/]+)\/([^\/]+)\/issues\/([0-9]+)$/);
                if (!match) throw new Error('Invalid GitHub issue URL');
                
                const [_, owner, repo, number] = match;
                const token = config?.github_token;
                const headers: Record<string, string> = {};
                if (token) {
                    headers['Authorization'] = `token ${token}`;
                }
                
                const res = await fetch(`https://api.github.com/repos/${owner}/${repo}/issues/${number}`, { headers });
                if (!res.ok) throw new Error(`GitHub API error: ${res.statusText}`);
                
                const data = await res.json();
                const content = `# ${data.title}\n\n${data.body || ''}`;
                setPrompt(content);
            } catch (err: any) {
                error = err.message || 'Failed to import GitHub issue';
                // Fallback: just paste the text
                setPrompt(prompt + text);
            } finally {
                loading = false;
            }
        }
    }

    onMount(() => {
        const textarea = document.getElementById('task-description');
        if (textarea) {
            textarea.addEventListener('paste', handlePaste);
        }
        return () => {
            const textarea = document.getElementById('task-description');
            if (textarea) {
                textarea.removeEventListener('paste', handlePaste);
            }
        };
    });
</script>

{#if loading}
    <div class="github-import-loading">
        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="spin">
            <path d="M21 12a9 9 0 1 1-6.219-8.56"/>
        </svg>
        <span>Importing GitHub issue...</span>
    </div>
{/if}

{#if error}
    <div class="github-import-error">{error}</div>
{/if}

<style>
    .github-import-loading {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 0.75rem;
        color: var(--color-accent);
        margin-top: 4px;
        padding: 4px 8px;
        background: var(--color-surface-3);
        border-radius: var(--radius-sm);
        width: fit-content;
    }
    .github-import-error {
        font-size: 0.75rem;
        color: var(--color-status-red);
        margin-top: 4px;
    }
    .spin {
        animation: spin 1s linear infinite;
    }
    @keyframes spin {
        from { transform: rotate(0deg); }
        to { transform: rotate(360deg); }
    }
</style>
