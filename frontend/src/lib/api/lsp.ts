import type { SandboxMemoryStats } from '../types';
import { API_BASE, apiFetch } from './helpers';

/**
 * Fetch the memory usage of the LSP extension sandbox serving the given
 * workspace+file (cgroup stats of the language server's gVisor container).
 * Resolves to null on any failure (no LSP for the file, sandbox not
 * running, non-200, network error) — the caller renders a fallback row.
 */
export async function fetchLSPSandboxMemory(workspace: string, file: string): Promise<SandboxMemoryStats | null> {
    if (!workspace || !file) return null;
    try {
        const params = new URLSearchParams({ workspace, file });
        const res = await apiFetch(`${API_BASE}/lsp/memory?${params.toString()}`, { method: 'GET' });
        if (!res.ok) return null;
        return (await res.json()) as SandboxMemoryStats;
    } catch {
        return null;
    }
}
