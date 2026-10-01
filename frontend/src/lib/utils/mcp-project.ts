import type { Config, MCPServer, Project } from '$lib/types';

/**
 * A project's MCP opt-in, read off the config's registry and the project's own id list. A server absent
 * from the registry is inert, not an error.
 */

/** The servers a project can be offered: the registered ones that are enabled globally. */
export function selectableMCPServers(config: Pick<Config, 'mcp_servers'> | null | undefined): MCPServer[] {
    return (config?.mcp_servers || []).filter((s) => s.enabled !== false && !!s.id);
}

/** Whether this project enabled one server. */
export function projectEnablesMCPServer(
    project: Pick<Project, 'mcp_server_ids'> | null | undefined,
    id?: string
): boolean {
    if (!id) return false;
    return (project?.mcp_server_ids || []).includes(id);
}

/** The opt-in list as a payload: the selected ids in the registry's order, so two saves of one
 *  selection send the same body. */
export function projectMCPServerIDs(config: Pick<Config, 'mcp_servers'> | null | undefined, selected: Set<string> | string[]): string[] {
    const chosen = selected instanceof Set ? selected : new Set(selected);
    return (config?.mcp_servers || [])
        .map((s) => s.id)
        .filter((id): id is string => !!id && chosen.has(id));
}

/** The names a project enabled, in the registry's order. Names, not ids, because the Sandbox
 *  Configuration shows them to a person. */
export function enabledMCPServerNames(
    config: Pick<Config, 'mcp_servers'> | null | undefined,
    project: Pick<Project, 'mcp_server_ids'> | null | undefined
): string[] {
    const enabled = new Set(project?.mcp_server_ids || []);
    return (config?.mcp_servers || [])
        .filter((s) => s.id && enabled.has(s.id) && s.enabled !== false)
        .map((s) => s.name);
}
