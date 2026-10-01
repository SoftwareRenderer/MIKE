import type { Config, NetworkedExtension, Project } from '$lib/types';
import { enabledMCPServerNames } from './mcp-project';

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

/**
 * The MCP servers this project enabled, by name. A second sentence beside the extensions' because the two
 * are turned on in different places. An empty list means there is nothing to report.
 */
export function serverToolMCPServers(
    config: Pick<Config, 'mcp_servers'> | null | undefined,
    project: Pick<Project, 'mcp_server_ids'> | null | undefined,
    sandboxEnabled: boolean,
    isolationEnabled: boolean
): string[] {
    if (!sandboxEnabled || !isolationEnabled) return [];
    return enabledMCPServerNames(config, project);
}
