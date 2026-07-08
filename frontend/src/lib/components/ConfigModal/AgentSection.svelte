<script lang="ts">
</script>

<div class="pi-info-section">
    <div class="pi-info-header">
        <svg width="20" height="20" viewBox="0 0 16 16" fill="none">
            <rect x="3" y="5" width="10" height="8" rx="1.5" stroke="currentColor" stroke-width="1.2"/>
            <circle cx="6" cy="9" r="1" fill="currentColor"/>
            <circle cx="10" cy="9" r="1" fill="currentColor"/>
            <path d="M8 2v3M5 4h6" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/>
        </svg>
        <span>Pi Coding Agent</span>
    </div>
    <p class="pi-info-text">
        Pi is a full coding agent that handles its own LLM calls, tool execution, and reasoning.
        When selected, Pi runs inside the task's worktree and manages the entire task lifecycle.
    </p>
    <div class="pi-info-box">
        <h4 class="pi-info-box-title">Setup</h4>
        <ul class="pi-info-list">
            <li><strong>Inherited Config:</strong> Your existing configuration is used when tasks are executed. Therefore all configuration of Pi should be done within Pi itself.</li>
            <li><strong>Sandbox:</strong> Your sandbox image must include <code>pi</code> on <code>$PATH</code>. Pi's config directory (<code>~/.pi/agent/</code>) is bind-mounted into the container at <code>/root/.pi/agent</code> with read/write access. Pi finds its config at the default location via <code>HOME=/root</code>.</li>
            <li><strong>Sessions:</strong> Each task gets its own Pi session (named <code>kanban-&#123;task-id&#125;</code>) for continuity across restarts.</li>
        </ul>
    </div>
</div>

<style>
    .pi-info-section {
        padding: 12px;
        background: var(--color-surface-2);
        border-radius: var(--radius-md);
        border: 1px solid var(--color-border);
    }
    .pi-info-header {
        display: flex;
        align-items: center;
        gap: 8px;
        font-weight: 600;
        font-size: 0.875rem;
        margin-bottom: 8px;
        color: var(--color-text-primary);
    }
    .pi-info-header svg {
        flex-shrink: 0;
        color: var(--color-accent);
    }
    .pi-info-text {
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        line-height: 1.5;
        margin: 0 0 12px;
    }
    .pi-info-box {
        background: var(--color-surface-1);
        border-radius: var(--radius-sm);
        padding: 10px 12px;
        border: 1px solid var(--color-border);
    }
    .pi-info-box-title {
        font-size: 0.75rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: var(--color-text-secondary);
        margin: 0 0 8px;
    }
    .pi-info-list {
        margin: 0;
        padding-left: 16px;
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        line-height: 1.6;
    }
    .pi-info-list li {
        margin-bottom: 4px;
    }
    .pi-info-list code {
        background: var(--color-surface-2);
        padding: 1px 4px;
        border-radius: 3px;
        font-size: 0.75rem;
    }
</style>
