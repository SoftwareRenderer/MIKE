<script lang="ts">
    import { useTaskEvents } from '../use-task-events.svelte';
    import type { TaskEventData } from '$lib/types';

    /**
     * Test-only: a real component instance, because the rule under test is an `$effect` and what that
     * effect ends up depending on is half the rule. `TaskChat` registers this same composable.
     */
    let { taskId }: { taskId: string } = $props();

    let frames = $state<TaskEventData[]>([]);
    // The handler reads this and writes it back, as the chat's does with its own transcript. A handler
    // that ran inside the effect's tracking would make the effect re-run and hand the frame again.
    let turn = $state('idle');

    const taskEvents = useTaskEvents(() => taskId, (event) => {
        frames = [...frames, { ...event, type: `${event.type}/${turn}` }];
        turn = turn === 'idle' ? 'open' : 'idle';
    });
</script>

<button class="arm" onclick={() => taskEvents.arm()}>arm</button>
<span class="count">{frames.length}</span>
<span class="types">{frames.map(f => f.type).join(',')}</span>
