<script lang="ts">
</script>

<div class="pi-info-section">
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
