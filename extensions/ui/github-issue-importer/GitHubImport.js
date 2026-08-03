/**
 * GitHub Issue Importer
 */
(() => {
    'use strict';

    const GITHUB_URL = /^https:\/\/github\.com\/([^/]+)\/([^/]+)\/issues\/([0-9]+)$/;

    async function handlePaste(data) {
        const text = (data && data.text) || '';
        const match = text.trim().match(GITHUB_URL);
        if (!match) {
            // Host prevented the default paste; re-insert the raw text.
            try { await extHost.createTask.description.insertText(text); } catch {}
            return;
        }

        const owner = match[1];
        const repo = match[2];
        const number = match[3];

        extHost.loading('Importing GitHub issue...');

        try {
            const issue = await extHost.proxy(
                `https://api.github.com/repos/${owner}/${repo}/issues/${number}`,
                'GET',
                ''
            );
            const content = `# ${issue.title || ''}\n\n${issue.body || ''}\n\nReference: ${text.trim()}`;
            await extHost.createTask.description.insertText(content);
            await extHost.hide();
        } catch (err) {
            extHost.error('Failed to import: ' + (err && err.message ? err.message : String(err)));
            // Fallback: insert the original URL.
            try { await extHost.createTask.description.insertText(text.trim()); } catch {}
        }
    }

    extHost.ready().then(() => {
        // Subscribe to paste events on the task description (granted capability).
        extHost.on('createTask.events.paste', handlePaste);
    });
})();
