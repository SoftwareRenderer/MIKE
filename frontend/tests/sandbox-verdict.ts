/**
 * Whether a gVisor container can start on this host. Global setup writes it once per run, because the
 * question costs a container start, and the specs that start one read it. `docs/testing.md → Go unit
 * test environment` holds why asking means starting a container.
 */

import fs from 'node:fs';

export const SANDBOX_VERDICT_FILE = '/tmp/kanban-test-sandbox.json';

export interface SandboxVerdict {
    usable: boolean;
    reason: string;
}

export function writeSandboxVerdict(verdict: SandboxVerdict): void {
    fs.writeFileSync(SANDBOX_VERDICT_FILE, JSON.stringify(verdict));
}

/** The verdict setup recorded. A missing file is an error, not a skip: that would let every container spec skip and the run look clean. */
export function readSandboxVerdict(): SandboxVerdict {
    try {
        return JSON.parse(fs.readFileSync(SANDBOX_VERDICT_FILE, 'utf-8')) as SandboxVerdict;
    } catch (e) {
        throw new Error(`readSandboxVerdict: ${SANDBOX_VERDICT_FILE} could not be read (${e}) — global-setup did not run`);
    }
}
