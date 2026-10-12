<script lang="ts">
    /**
     * The three sandbox settings that are lists of saved values: bind mounts, environment
     * variables, capabilities. SandboxConfigModal shows them read-only while tasks are in
     * progress and SandboxPanel shows them for one task, from different sources but in the same
     * shape, so the shape lives here. Its styles are the global `sandbox-*` rules in
     * $lib/styles/sandbox-settings.css — see that file for why they cannot be scoped.
     */
    import type { SandboxEnv, SandboxMount } from '$lib/types';
    import { t } from '$lib/i18n';

    let { mounts = [], envs = [], capabilities = [], bounded = false }: {
        mounts?: SandboxMount[];
        envs?: SandboxEnv[];
        capabilities?: string[];
        /** Rule between each section, for a view that stacks them directly under the settings rows. */
        bounded?: boolean;
    } = $props();
</script>

{#if mounts.length > 0}
    <div class="sandbox-section" class:sandbox-section-bounded={bounded}>
        <span class="sandbox-section-label">{t('sandbox.customMounts')}</span>
        <div class="sandbox-list">
            {#each mounts as mount}
                <div class="sandbox-item">
                    <span class="sandbox-item-key sandbox-trunc" title={mount.host_path}>{mount.host_path}</span>
                    <span class="sandbox-item-text sandbox-trunc" title={mount.container_path}>{mount.container_path}</span>
                    <span class="sandbox-mode sandbox-mode-end" class:sandbox-mode-rw={mount.mode === 'rw'} class:sandbox-mode-ro={mount.mode === 'ro'} title={mount.mode === 'rw' ? t('sandbox.readWrite') : t('sandbox.readOnly')}>{mount.mode}</span>
                </div>
            {/each}
        </div>
    </div>
{/if}

{#if envs.length > 0}
    <div class="sandbox-section" class:sandbox-section-bounded={bounded}>
        <span class="sandbox-section-label">{t('sandbox.environmentVariables')}</span>
        <div class="sandbox-list">
            {#each envs as env}
                <div class="sandbox-item">
                    <span class="sandbox-item-key sandbox-trunc" title={env.key}>{env.key}</span>
                    <span class="sandbox-item-text sandbox-trunc" title={env.value}>{env.value || t('sandbox.empty')}</span>
                </div>
            {/each}
        </div>
    </div>
{/if}

{#if capabilities.length > 0}
    <div class="sandbox-section" class:sandbox-section-bounded={bounded}>
        <span class="sandbox-section-label">{t('sandbox.capabilities')}</span>
        <div class="sandbox-list-wrap">
            {#each capabilities as cap}
                <span class="sandbox-cap" title={cap}>{cap}</span>
            {/each}
        </div>
    </div>
{/if}
