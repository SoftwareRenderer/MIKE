/**
 * GitHub Issue Importer — plain JS extension (no Svelte dependency).
 *
 * Listens for paste events on the task description textarea and imports
 * GitHub issue content when a GitHub issue URL is pasted.
 *
 * Exports an `init` function called by the UI injector with context.
 */

export default function init({ config }) {
    const textarea = document.getElementById('task-description');
    if (!textarea) return;

    const container = document.createElement('div');
    container.className = 'github-import-container';
    container.style.cssText = 'display:flex;flex-direction:column;gap:4px;';
    textarea.parentNode.insertBefore(container, textarea.nextSibling);

    let loadingEl = null;
    let errorEl = null;

    function showLoading() {
        hideLoading();
        hideError();
        loadingEl = document.createElement('div');
        loadingEl.style.cssText =
            'display:flex;align-items:center;gap:6px;font-size:0.75rem;color:var(--color-accent, #00add8);margin-top:4px;padding:4px 8px;background:var(--color-surface-3, #1e1e1e);border-radius:var(--radius-sm, 4px);width:fit-content;';
        loadingEl.innerHTML =
            '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="animation:spin 1s linear infinite"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>' +
            '<span>Importing GitHub issue…</span>';
        container.appendChild(loadingEl);

        // Add spin keyframes once
        if (!document.getElementById('github-import-styles')) {
            const style = document.createElement('style');
            style.id = 'github-import-styles';
            style.textContent = '@keyframes spin{from{transform:rotate(0deg)}to{transform:rotate(360deg)}}';
            document.head.appendChild(style);
        }
    }

    function hideLoading() {
        if (loadingEl) {
            loadingEl.remove();
            loadingEl = null;
        }
    }

    function showError(msg) {
        hideError();
        errorEl = document.createElement('div');
        errorEl.style.cssText =
            'font-size:0.75rem;color:var(--color-status-red, #f85149);margin-top:4px;';
        errorEl.textContent = msg;
        container.appendChild(errorEl);
        setTimeout(hideError, 5000);
    }

    function hideError() {
        if (errorEl) {
            errorEl.remove();
            errorEl = null;
        }
    }

    async function handlePaste(e) {
        const text = e.clipboardData?.getData('text') || '';
        const match = text.trim().match(/^https:\/\/github\.com\/([^\/]+)\/([^\/]+)\/issues\/([0-9]+)$/);
        if (!match) return;

        e.preventDefault();
        const [, owner, repo, number] = match;
        const token = config?.github_token;

        showLoading();

        try {
            const headers = {};
            if (token) headers['Authorization'] = `token ${token}`;

            const res = await fetch(
                `https://api.github.com/repos/${owner}/${repo}/issues/${number}`,
                { headers }
            );
            if (!res.ok) throw new Error(`GitHub API: ${res.statusText}`);

            const data = await res.json();
            const url = text.trim();
            const content = `# ${data.title}\n\n${data.body || ''}\n\nReference: ${url}`;

            // Insert URL + content into textarea, preserving existing text
            const start = textarea.selectionStart;
            const end = textarea.selectionEnd;
            const before = textarea.value.substring(0, start);
            const after = textarea.value.substring(end);
            textarea.value = before + content + after;

            // Dispatch input event so the framework picks up the change
            textarea.dispatchEvent(new Event('input', { bubbles: true }));
        } catch (err) {
            showError(err.message || 'Failed to import GitHub issue');
            // Fallback: paste the original URL
            const start = textarea.selectionStart;
            const end = textarea.selectionEnd;
            const before = textarea.value.substring(0, start);
            const after = textarea.value.substring(end);
            textarea.value = before + text.trim() + after;
            textarea.dispatchEvent(new Event('input', { bubbles: true }));
        } finally {
            hideLoading();
        }
    }

    textarea.addEventListener('paste', handlePaste);

    // Return cleanup function
    return () => {
        textarea.removeEventListener('paste', handlePaste);
        container.remove();
    };
}
