/**
 * The certificate the node side of the lane verifies against.
 *
 * Each lane server signs its leaf with a CA it created in its own data directory, which nothing on this
 * machine trusts. Node-side requests verify against that file instead of switching verification off, so
 * the lane exercises the same certificate the pairing dialog offers a device (`docs/tls.md → Certificates
 * on disk`). The browsers ignore the certificate instead — a spec measures the app, not a chain this
 * machine never installed — and `playwright.config.ts` says so where it sets that option.
 */

import fs from 'node:fs';
import https from 'node:https';

// The data directory of the lane server on each port, which is where that server writes its CA at boot.
const DATA_DIR_BY_PORT: Record<string, string> = {
    '3000': '/tmp/kanban-test-data',
    '3001': '/tmp/kanban-test-data-isolated',
};

const agents = new Map<string, https.Agent>();

/** Request options carrying the CA of the lane server behind this URL or port. */
export function laneRequestOptions(urlOrPort: string | number): https.RequestOptions {
    const text = String(urlOrPort);
    const port = text.startsWith('http') ? new URL(text).port : text;
    const dataDir = DATA_DIR_BY_PORT[port];
    if (!dataDir) {
        throw new Error(`[lane-tls] no lane server known on port ${port || '(none)'} — the lane listens on 3000 and 3001`);
    }
    let agent = agents.get(port);
    if (!agent) {
        const file = `${dataDir}/tls/mike-ca.crt`;
        if (!fs.existsSync(file)) {
            throw new Error(`[lane-tls] no certificate authority at ${file} — the lane server on port ${port} `
                + 'creates one at boot; check that server\'s log');
        }
        agent = new https.Agent({ ca: fs.readFileSync(file) });
        agents.set(port, agent);
    }
    return { agent };
}
