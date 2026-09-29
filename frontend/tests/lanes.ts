/**
 * The e2e lane topology: which spec runs against which server, and what each lane's port, database and
 * session files are called.
 *
 * `ISOLATED_SPECS` is the single source of truth for the config-modifying suites, and it is an array of
 * spec names and nothing else: the chromium lane's `testMatch` excludes the whole set, and the shard
 * projects in `playwright.config.ts` are dealt out of it. A new config-modifying spec is one string here
 * and no other edit. A spec that does not mutate server-global state does not belong in it — the shards are
 * not a place to park a slow file.
 *
 * Why the set runs on N servers rather than one serialized queue is ADR-0003.
 */

/** The main lane's port. Shard 1 keeps 3001, so "3001" still means "a lane server". */
export const MAIN_PORT = 3000;
export const ISOLATED_PORT_BASE = 3001;
/** One mock LLM serves every lane. */
export const MOCK_PORT = 9999;

/**
 * Specs that mutate server-global state — `/api/config`, providers, installed extensions, task
 * concurrency — so each needs a server of its own rather than a slot on the shared one.
 */
export const ISOLATED_SPECS: string[] = [
    'extension-frame-channel',
    // Installs a translation pack from the registry fixture on the lane server — and
    // the offer it tests only exists in the no-pack state, so it cannot share a
    // server whose extension list someone else is mutating.
    'i18n-first-run-offer',
    // 13 tests of iframes, preview tokens and a live file listing — the first things to slip when
    // the lane server is answering six workers at once.
    'html-preview',
    'extensions-danger-permissions',
    'mode-select',
    'preferences-hide-model-badge',
    'preferences-keyboard',
    'provider-task-concurrency',
    'settings-extension-dependency-cache',
    'settings-provider',
    // Saves Settings, which is what reads the update status the dot is made of — a config write, so it
    // cannot share the lane every other chromium spec reads.
    'settings-update-dot',
    'settings-sessions',
    // Issues and revokes API tokens on the lane server, and asserts what a token can reach.
    'settings-api-tokens',
];

/**
 * Per-file test seconds, so the deal onto the shards is longest-first: `html-preview` is thirty times
 * `extensions-danger-permissions`, and dealing the array in its written order would put both long files on
 * one shard.
 *
 * Measured on this tree's full run of 2026-09-29 (one lane, chromium at 4 workers, every lane spec green)
 * with `make test-time TIME_ARGS='--show-specs=20 --json=/tmp/pw.json'`. The lane's wall clock, not these
 * numbers, is what a run claims, so they weigh the deal rather than predict it — every file takes longer
 * once shards share the machine's tail with the chromium lane, so this table is one shard's uncontended
 * reading and only the ratio between entries is used. A name missing from this table is priced at
 * `DEFAULT_SPEC_SECONDS`, so adding a spec to `ISOLATED_SPECS` needs no edit here.
 */
const ISOLATED_SPEC_SECONDS: Record<string, number> = {
    'html-preview': 33.1,
    'settings-provider': 26.1,
    'preferences-keyboard': 21.3,
    'provider-task-concurrency': 19.7,
    'extension-frame-channel': 9.4,
    'mode-select': 9.4,
    'settings-extension-dependency-cache': 6.1,
    'settings-api-tokens': 4.3,
    'preferences-hide-model-badge': 3.4,
    'settings-sessions': 3.4,
    'i18n-first-run-offer': 2.5,
    'settings-update-dot': 1.8,
    'extensions-danger-permissions': 1.0,
};

/** The median of the priced files: a new spec is neither the cheapest nor the dearest thing on the lane. */
const DEFAULT_SPEC_SECONDS = 4.3;

/**
 * The measured shard count — see ADR-0003 for the runs that chose it.
 *
 * Two is the largest deal whose own lanes lost no test in six full runs. A third shard buys 21.9 s of median
 * run body — less than the spread between runs of one configuration — and costs `html-preview`, the lane's
 * dearest file: its own test time is 36.6 s at one shard and 38.3-40.1 s at two, but 49.3-60.2 s at three,
 * where one of its tests overspends the 15 s `expect` it waits on.
 */
const MEASURED_SHARDS = 2;

/**
 * How many lane servers the set is dealt onto, and so how many lane workers run at once.
 * `MIKE_E2E_ISOLATED_SHARDS` re-measures it on a machine whose cores and memory differ from the one this
 * was measured on; a value outside 1..len(ISOLATED_SPECS) names the variable and stops the run.
 */
export const ISOLATED_SHARDS = resolveShards(process.env.MIKE_E2E_ISOLATED_SHARDS, ISOLATED_SPECS.length);

function resolveShards(raw: string | undefined, max: number): number {
    if (raw === undefined || raw === '') return MEASURED_SHARDS;
    const n = Number(raw);
    if (!Number.isInteger(n) || n < 1 || n > max) {
        throw new Error(`[lanes] MIKE_E2E_ISOLATED_SHARDS must be an integer between 1 and ${max}, got "${raw}"`);
    }
    return n;
}

/** One lane: a server of its own, with its own database, session files, PID file and boot log. */
export interface Lane {
    /** The Playwright project name — the lane and the project are the same thing. */
    name: string;
    port: number;
    /** The origin its project's `baseURL` and the Node-side helpers dial. */
    base: string;
    dataDir: string;
    /** Playwright's `storageState`, read at context creation. */
    storageState: string;
    /** The same session plus its CSRF token, for the Node-side `serverApi` helper. */
    authFile: string;
    pidFile: string;
    logFile: string;
    /** The specs dealt to this lane; empty for the main lane, which takes everything else. */
    specs: string[];
}

/** The boot log and PID file of one lane, named after the lane. */
function laneFiles(name: string): { pidFile: string; logFile: string } {
    return {
        pidFile: `/tmp/kanban-test-lane-${name}.pid`,
        logFile: `/tmp/kanban-test-lane-${name}.log`,
    };
}

/**
 * One lane server per shard, with the specs dealt to it.
 *
 * The deal is longest-spec-first round-robin, which balances the shards without knowing how many of them
 * there are: shard k takes the k-th, k+N-th, k+2N-th dearest file.
 */
export function isolatedLanes(count: number = ISOLATED_SHARDS): Lane[] {
    const bySeconds = [...ISOLATED_SPECS].sort((a, b) => specSeconds(b) - specSeconds(a));
    const deals: string[][] = Array.from({ length: count }, () => []);
    bySeconds.forEach((spec, i) => deals[i % count].push(spec));
    return deals.map((specs, i) => {
        const name = `isolated-${i + 1}`;
        const port = ISOLATED_PORT_BASE + i;
        return {
            name,
            port,
            base: `http://localhost:${port}`,
            dataDir: `/tmp/kanban-test-data-isolated-${i + 1}`,
            storageState: `/tmp/kanban-test-storage-state.json.isolated-${i + 1}`,
            authFile: `/tmp/kanban-test-auth.json.isolated-${i + 1}`,
            ...laneFiles(name),
            specs,
        };
    });
}

function specSeconds(spec: string): number {
    return ISOLATED_SPEC_SECONDS[spec] ?? DEFAULT_SPEC_SECONDS;
}

/** The main lane, which runs every spec `ISOLATED_SPECS` does not name. */
export const MAIN_LANE: Lane = {
    name: 'chromium',
    port: MAIN_PORT,
    base: `http://localhost:${MAIN_PORT}`,
    dataDir: '/tmp/kanban-test-data',
    storageState: '/tmp/kanban-test-storage-state.json',
    authFile: '/tmp/kanban-test-auth.json',
    ...laneFiles('chromium'),
    specs: [],
};

/** Every server a run starts, in the order it starts them. */
export function allLanes(): Lane[] {
    return [MAIN_LANE, ...isolatedLanes()];
}

/** The lane a Playwright project runs on — the project name is the lane name. */
export function laneByProject(projectName: string): Lane | undefined {
    return allLanes().find((lane) => lane.name === projectName);
}

/** The lane listening on a port, for the Node-side helpers that build a request out of a baseURL. */
export function laneByPort(port: string | number): Lane | undefined {
    const wanted = String(port);
    return allLanes().find((lane) => String(lane.port) === wanted);
}
