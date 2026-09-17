import type { Config, NetworkedExtension } from '$lib/types';

// A contributed tool runs in the server process, so Network Isolation on the task's container says nothing
// about it. An empty list means there is nothing to report.
export function serverToolExtensions(
    config: Pick<Config, 'server_network_extensions'> | null | undefined,
    sandboxEnabled: boolean,
    isolationEnabled: boolean
): NetworkedExtension[] {
    if (!sandboxEnabled || !isolationEnabled) return [];
    return config?.server_network_extensions ?? [];
}

// The extensions a notice names, by the label their card shows, with the tools each one contributes.
export function formatNetworkedExtensions(extensions: NetworkedExtension[]): string {
    return extensions
        .map((ext) => {
            const tools = ext.tools ?? [];
            return tools.length === 0 ? ext.display_name : `${ext.display_name} (${tools.join(', ')})`;
        })
        .join(', ');
}
