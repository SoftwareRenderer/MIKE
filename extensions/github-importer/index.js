export default async function GitHubImporter({ context, config }) {
    const { taskId } = context;
    const { github_token } = config;

    if (!github_token) {
        console.warn('[github-importer] Missing github_token in configuration');
        return null;
    }

    try {
        // Fetch the task to check its description for GitHub URLs
        const response = await fetch(`/api/tasks/${taskId}`);
        if (!response.ok) throw new Error(`Failed to fetch task: ${response.statusText}`);
        const task = await response.json();
        
        const description = task.prompt || '';
        const githubUrlRegex = /https:\/\/github\.com\/([^/]+)\/([^/]+)\/issues\/(\d+)/;
        const match = description.match(githubUrlRegex);

        if (!match) return null;

        const [url, owner, repo, issueNumber] = match;

        // Create a simple button to trigger the import
        const button = document.createElement('button');
        button.innerText = `Import GitHub Issue #${issueNumber}`;
        button.style.cssText = `
            background: var(--color-accent, #4C9AFF);
            color: white;
            border: none;
            border-radius: 4px;
            padding: 4px 8px;
            font-size: 12px;
            cursor: pointer;
            margin: 4px 0;
            display: flex;
            align-items: center;
            gap: 4px;
        `;

        button.onclick = async () => {
            button.disabled = true;
            button.innerText = 'Importing...';
            try {
                const importResponse = await fetch('/api/github/import-issue', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ taskId, url, github_token })
                });
                if (!importResponse.ok) throw new Error(`Import failed: ${importResponse.statusText}`);
                
                button.innerText = 'Imported!';
                button.style.background = 'var(--color-status-green, #3FB950)';
                setTimeout(() => {
                    button.remove();
                }, 3000);
            } catch (e) {
                console.error('GitHub import error:', e);
                button.innerText = 'Error importing';
                button.style.background = 'var(--color-status-red, #F85149)';
                button.disabled = false;
            }
        };

        return button;
    } catch (e) {
        console.error('[github-importer] Error:', e);
        return null;
    }
}
