import { API_BASE, apiFetch, apiError } from './helpers';
import { t } from '$lib/i18n';
import type { MCPHeader } from '../types';

// Settings → MCP asks one server whether it is there. A refusal, a wrong-era server and a timeout are
// all a 200 carrying a kind; only a form that cannot be dialled at all is an error.

/** One server as the Test action is asked about it: the form's current values, not a stored row, so an
 *  unsaved address and an unsaved token are both exercised. A header holding the masked placeholder
 *  asks the server to use the one it stored. */
export interface MCPServerProbeRequest {
    id?: string;
    name: string;
    url: string;
    timeout_seconds?: number;
    headers?: MCPHeader[];
}

/** What one probe answered. `kind` is the classification in `backend/mcp` and decides which fix the
 *  sentence points at; the card writes its verdict line from `ok`, `message`, `tools` and `dropped`. */
export interface MCPServerProbe {
    ok: boolean;
    kind?: string;
    message: string;
    tools: number;
    dropped?: number;
}

// Never queued when offline: a replayed probe answers a question nobody is still asking.
export async function testMCPServer(body: MCPServerProbeRequest): Promise<MCPServerProbe> {
    const res = await apiFetch(`${API_BASE}/mcp/test`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
    if (!res.ok) throw await apiError(res, t('api.testMCPServer'));
    return res.json();
}
