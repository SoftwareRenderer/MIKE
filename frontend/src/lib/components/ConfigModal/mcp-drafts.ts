import type { Config, MCPServer } from '$lib/types';
import { MASKED_SECRET } from '$lib/api/helpers';
import type { MessageKey } from '$lib/i18n';

/**
 * The registry Settings → MCP edits before saving: the `MCPServer` the server sent plus the per-card
 * state a probe needs. The list is saved whole, so a server missing from the payload is a deleted one.
 */

/** The bounds the server refuses past (`backend/types/mcp.go`), mirrored so the form refuses what the
 *  save refuses. */
export const MAX_MCP_SERVERS = 16;
export const MAX_MCP_HEADERS = 8;
export const MAX_MCP_TIMEOUT_SECONDS = 300;

/**
 * A new row's deadline, and what a cleared field reads as. Zero is a timeout a person types on purpose —
 * it means the call waits with no deadline — so the default has to be a number the form starts with, not an
 * absent field.
 */
const DEFAULT_MCP_TIMEOUT_SECONDS = 30;

interface MCPHeaderDraft {
    name: string;
    /** Only what this browser typed: "" for nothing typed, never the masked placeholder. */
    value: string;
    /** Whether a stored value stands behind this row, which an empty `value` then stands in for. */
    stored: boolean;
}

export interface MCPServerDraft {
    /** Stable key for {#each}: the stored id, or a uid for a row never saved. */
    uid: string;
    id: string;
    name: string;
    url: string;
    enabled: boolean;
    timeoutSeconds: number;
    headers: MCPHeaderDraft[];
    testing: boolean;
    result: { ok: boolean; message: string } | null;
}

/** The row the config view sent, as an editable draft. `stored` is read off the masked value, and the
 *  mask is never left in the field: someone typing there would type after the placeholder. */
function buildMCPServerDraft(server: MCPServer): MCPServerDraft {
    return {
        uid: server.id || `new-${Math.random().toString(36).slice(2)}`,
        id: server.id || '',
        name: server.name || '',
        url: server.url || '',
        enabled: server.enabled !== false,
        timeoutSeconds: server.timeout_seconds ?? 0,
        headers: (server.headers || []).map((h) => ({
            name: h.name || '',
            stored: h.value === MASKED_SECRET,
            value: h.value === MASKED_SECRET ? '' : h.value ?? '',
        })),
        testing: false,
        result: null,
    };
}

export function buildMCPServers(config: Pick<Config, 'mcp_servers'> | null | undefined): MCPServerDraft[] {
    return (config?.mcp_servers || []).map(buildMCPServerDraft);
}

/** An empty draft, appended by the Add button. Enabled, and carrying the default deadline; the row's own
 *  checkbox and timeout field say otherwise. */
export function newMCPServerDraft(uid: string): MCPServerDraft {
    return {
        uid, id: '', name: '', url: '', enabled: true, timeoutSeconds: DEFAULT_MCP_TIMEOUT_SECONDS,
        headers: [], testing: false, result: null,
    };
}

/** A draft with no name and no address is a row someone opened by accident, and it costs the payload
 *  nothing. */
function isDiscardableMCPServerDraft(draft: MCPServerDraft): boolean {
    return !draft.id && !draft.name.trim() && !draft.url.trim();
}

/** A header row that carries neither a name nor a value — typed or stored — sends nothing. */
function usableHeaders(draft: MCPServerDraft): { name: string; value: string }[] {
    return draft.headers
        .map((h) => ({ name: h.name.trim(), value: h.value || (h.stored ? MASKED_SECRET : '') }))
        .filter((h) => h.name !== '' || h.value !== '');
}

/** The `MCPServer` the server takes, from the draft. A stored value nobody retyped goes back as the
 *  masked placeholder, which is what keeps it. */
export function mcpServerPayload(draft: MCPServerDraft): MCPServer {
    const server: MCPServer = {
        name: draft.name.trim(),
        url: draft.url.trim().replace(/\/+$/, ''),
        enabled: draft.enabled,
        // Always sent: 0 means "no deadline", so an absent field cannot be allowed to mean it too. A
        // field cleared on screen is the default, because "forever" is something a person types.
        timeout_seconds: Number.isFinite(draft.timeoutSeconds)
            ? draft.timeoutSeconds
            : DEFAULT_MCP_TIMEOUT_SECONDS,
    };
    if (draft.id) server.id = draft.id;
    const headers = usableHeaders(draft);
    if (headers.length) server.headers = headers;
    return server;
}

export function mcpServersPayload(drafts: MCPServerDraft[]): MCPServer[] {
    return drafts.filter((d) => !isDiscardableMCPServerDraft(d)).map(mcpServerPayload);
}

/** Why this registry cannot be saved: the catalog key of the sentence and the names it interpolates. */
export interface MCPValidationProblem {
    key: MessageKey;
    params?: Record<string, string | number>;
}

/**
 * Why this registry cannot be saved, or null when it can. The server refuses the same shapes; saying it
 * here names the field while it is still on screen.
 */
export function validateMCPServers(drafts: MCPServerDraft[]): MCPValidationProblem | null {
    const kept = drafts.filter((d) => !isDiscardableMCPServerDraft(d));
    if (kept.length > MAX_MCP_SERVERS) {
        return { key: 'settings.mcp.validate.tooMany', params: { max: MAX_MCP_SERVERS } };
    }
    const names = new Map<string, string>();
    for (const draft of kept) {
        const name = draft.name.trim();
        if (!name) return { key: 'settings.mcp.validate.noName' };
        const url = draft.url.trim();
        if (!url) return { key: 'settings.mcp.validate.noUrl', params: { name } };
        if (!/^[Hh][Tt][Tt][Pp][Ss]?:\/\//.test(url)) {
            return { key: 'settings.mcp.validate.transport', params: { name } };
        }
        if (draft.timeoutSeconds < 0 || draft.timeoutSeconds > MAX_MCP_TIMEOUT_SECONDS) {
            return {
                key: 'settings.mcp.validate.timeout',
                params: { name, seconds: draft.timeoutSeconds, max: MAX_MCP_TIMEOUT_SECONDS },
            };
        }
        const headers = usableHeaders(draft);
        if (headers.length > MAX_MCP_HEADERS) {
            return {
                key: 'settings.mcp.validate.headerCount',
                params: { name, count: headers.length, max: MAX_MCP_HEADERS },
            };
        }
        const seenName = new Set<string>();
        for (const h of headers) {
            if (!h.name) return { key: 'settings.mcp.validate.headerNoName', params: { name } };
            if (!HEADER_FIELD_NAME.test(h.name)) {
                return { key: 'settings.mcp.validate.headerName', params: { name, header: h.name } };
            }
            const lower = h.name.toLowerCase();
            if (seenName.has(lower)) {
                return { key: 'settings.mcp.validate.headerTwice', params: { name, header: h.name } };
            }
            seenName.add(lower);
        }
        // Two names that slug alike would name their tools alike. The server holds the slugifier and
        // refuses the rest.
        const slug = slugifyForMCP(name);
        const clash = names.get(slug);
        if (clash) return { key: 'settings.mcp.validate.slugClash', params: { other: clash, name, slug } };
        names.set(slug, name);
    }
    return null;
}

/** An HTTP field name (RFC 9110 tchar) — the same character set `types.isHeaderFieldName` accepts. */
const HEADER_FIELD_NAME = /^[\w!#$%&'*+.^`|~-]+$/;

/** The slug half of the name rule, matching `types.Slugify` closely enough to warn early: lowercase,
 *  runs of anything else become one "-", capped at `mcpServerSlugLen`. */
const mcpServerSlugLen = 24;

function slugifyForMCP(value: string): string {
    return value
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, '-')
        .replace(/^-+|-+$/g, '')
        .slice(0, mcpServerSlugLen);
}
