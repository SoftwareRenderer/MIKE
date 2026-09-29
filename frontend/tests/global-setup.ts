/**
 * Global setup/teardown for e2e tests.
 * Set USE_MOCK_LLM=0 to use real LLM endpoint (default: mock).
 *
 * Everything the lane needs before its first test is built here, so this file owns the run's fixed
 * cost and keeps it flat: one `go build` for both lane servers, fixture artifacts cached behind a
 * content hash of their source, and both lane servers booting while each other's seed work runs.
 * `make test-time` (scripts/e2e-time.py) splits the wall clock at `[teardown] ENTRY`, so the phases
 * below are visible without this file measuring anything.
 */

import { spawn } from 'node:child_process';
import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { execSync, execFileSync } from 'node:child_process';

const __dirname = path.dirname(new URL(import.meta.url).pathname);
const PROJECT_ROOT = path.resolve(__dirname, '../..');
const FRONTEND_DIR = path.resolve(__dirname, '..');

const TEST_REPO = '/tmp/kanban-test-repo';
const TEST_REPO_2 = '/tmp/kanban-test-repo-2';
const TEST_DATA_DIR = '/tmp/kanban-test-data';
const TEST_DATA_DIR_ISOLATED = '/tmp/kanban-test-data-isolated';
const TEST_WORKTREES = '/tmp/kanban-test-worktrees';
// Per-worker project repos created by helpers.ts isolatedProject() — scratch, like the worktrees.
const TEST_OWN_REPOS = '/tmp/kanban-test-repos';
const KANBAN_PORT = 3000;
const KANBAN_PORT_ISOLATED = 3001;
const MOCK_PORT = 9999;
const KANBAN_BASE = `http://localhost:${KANBAN_PORT}`;
const KANBAN_BASE_ISOLATED = `http://localhost:${KANBAN_PORT_ISOLATED}`;
const LOCK_FILE = '/tmp/kanban-test-setup.lock';
// Playwright storage state files — read at context creation time
const STORAGE_STATE_FILE = '/tmp/kanban-test-storage-state.json';
const STORAGE_STATE_FILE_ISOLATED = '/tmp/kanban-test-storage-state.json.isolated';
// Set to 1 to rebuild every cached fixture instead of trusting its marker.
const REBUILD_ENV = 'MIKE_E2E_REBUILD';
const TEST_PASSWORD = 'test-password-1234';
// Release updates are switched off for the run. The boot pass (`updateCheck.start()` in App.svelte)
// asks the opt-in question of every fresh browser and, once the answer is yes, dials the manifest
// host — neither belongs in a hermetic suite. `disabled` is the posture the client stops on: no dot,
// no question, no prompt (docs/auto-update.md). A spec that wants the dot stubs /api/updates/status.
const NO_UPDATES_ENV = { MIKE_DISABLE_UPDATES: '1' };
// The lane's own git identity: this must work on a machine with no global git config, and must not
// depend on the one a developer exports.
const GIT_IDENTITY = ['-c', 'user.name=MIKE e2e', '-c', 'user.email=e2e@localhost.test',
    '-c', 'commit.gpgsign=false', '-c', 'core.autocrlf=false'];
const REPO_USER_EMAIL = 'test@kanban.local';
const REPO_USER_NAME = 'Test';

/**
 * One lane: a server of its own, with its own database, storage state, PID file and boot log.
 *
 * The PID file holds the PID of the server itself, which is why the lane binary is spawned directly
 * rather than through a `setsid` wrapper: an identity-based stop cannot reach a grandchild it has no
 * PID for, and the port is not an identity (`lsof -ti:PORT` answers with clients too).
 *
 * The boot log is what the server printed, which is the only record of a lane that died during setup:
 * `waitForServer` reports the timeout, the log names the reason.
 */
interface Lane {
    name: string;
    base: string;
    port: number;
    dataDir: string;
    storageState: string;
    authFile: string;
    pidFile: string;
    logFile: string;
}

const LANES: Lane[] = [
    {
        name: 'main', base: KANBAN_BASE, port: KANBAN_PORT, dataDir: TEST_DATA_DIR,
        storageState: STORAGE_STATE_FILE, authFile: '/tmp/kanban-test-auth.json',
        pidFile: '/tmp/kanban-test-lane-main.pid', logFile: '/tmp/kanban-test-lane-main.log',
    },
    {
        name: 'isolated', base: KANBAN_BASE_ISOLATED, port: KANBAN_PORT_ISOLATED, dataDir: TEST_DATA_DIR_ISOLATED,
        storageState: STORAGE_STATE_FILE_ISOLATED, authFile: '/tmp/kanban-test-auth.json.isolated',
        pidFile: '/tmp/kanban-test-lane-isolated.pid', logFile: '/tmp/kanban-test-lane-isolated.log',
    },
];
const MOCK_PID_FILE = '/tmp/kanban-test-mock-llm.pid';
const MOCK_LOG_FILE = '/tmp/kanban-test-mock-llm.log';

// The server both lanes run. Built by setup, never `go run`: one compile serves both servers, and the
// binary is named `mike` so `pgrep -xc mike` counts the lane's own processes.
const LANE_BIN_DIR = '/tmp/kanban-test-bin';
const LANE_BIN = path.join(LANE_BIN_DIR, 'mike');

// server.ShutdownTimeout is 15 s (backend/server/shutdown.go), so a lane server that is shutting down
// politely is worth waiting out; the slack is for noticing that it is not shutting down at all.
const STOP_GRACE_MS = 18000;
const KILL_GRACE_MS = 5000;
// A killed server can still be writing when the removal walks its data dir.
const REMOVE_BUDGET_MS = 5000;

// Point the test servers at a local registry fixture so e2e registry tests are
// deterministic and never depend on the hosted online registry.
//
// The servers do not read `registry-fixture.json` directly: one entry (the i18n
// pack) is pinned to a git commit that only exists once setup has built the repo
// it points at, so the fixture is copied to GENERATE_REGISTRY_FILE with that
// placeholder resolved. Content is otherwise byte-identical, and a registry fetch
// is a per-request file read (`HandleRegistryIndex` caches nothing), so both lanes
// see the same index.
const REGISTRY_FIXTURE_FILE = path.join(__dirname, 'registry-fixture.json');
const GENERATED_REGISTRY_FILE = '/tmp/kanban-test-registry.json';
const TEST_REGISTRY_URL = 'file://' + GENERATED_REGISTRY_FILE;
// The i18n pack the registry fixture offers: sources committed under
// tests/fixtures/i18n-pack-repo, committed into a throwaway local git repo so the
// registry's git-pinned install path (`repo` + `commit` + `subdir`, cloned and
// commit-verified server-side) has something real to install. The commit is built
// from the fixture with a fixed identity and a fixed commit date, so identical content
// always produces the same hash — which is what lets the repo be reused across runs
// (see cacheHit) while an edited fixture still rebuilds it.
const I18N_PACK_REPO = '/tmp/kanban-test-i18n-pack-repo';
const I18N_PACK_FIXTURE_DIR = path.join(__dirname, 'fixtures', 'i18n-pack-repo');
const I18N_PACK_COMMIT_PLACEHOLDER = '__I18N_PACK_COMMIT__';
// Local (unpublished) extension fixtures seeded into the repo extensions dir
// for the Browse e2e specs. In a fresh checkout `extensions/` is empty, so
// specs that need local extensions would fail; seeding from committed fixtures
// keeps the suite hermetic. Only MISSING entries are created — pre-existing
// dev installs are never touched — and teardown removes exactly what setup
// created (tracked in SEEDED_EXTENSIONS_FILE so an interrupted run's leftovers
// are cleaned by the next run's stale-state cleanup).
const LOCAL_EXT_FIXTURES_DIR = path.join(__dirname, 'fixtures', 'local-extensions');
const SEEDED_EXTENSIONS_FILE = '/tmp/kanban-test-seeded-extensions.json';
// Published extensions the specs install with `install-local` have no committed fixture here — their source
// is the sibling extensions repo. `install-local` reads only what is on disk, and **deleting an extension
// deletes its directory**, so a run that uninstalls one consumes the source the next run needs: the spec
// then says "html-preview not in local extensions list", which reads as a regression in the code under
// test rather than a missing directory. Setup seeds from the sibling repo where the entry is absent, and
// refuses the run naming the path when it cannot, so the cause is stated once instead of fourteen times.
const PUBLISHED_EXT_SOURCE = path.resolve(PROJECT_ROOT, '..', 'MIKE-extensions');
const LOCAL_EXT_PUBLISHED: { author: string; name: string; subdir: string }[] = [
    { author: 'softwarerenderer', name: 'html-preview', subdir: 'ui/html-preview' },
];

const useMock = () => process.env.MIKE_USE_MOCK_LLM !== '0';
const MOCK_URL = `http://localhost:${MOCK_PORT}/v1`;

function forceRebuild(): boolean {
    return process.env[REBUILD_ENV] === '1';
}

async function setup(): Promise<void> {
    // Anything left by an interrupted run holds the lane's ports, data dirs and PID files, and this is
    // also what removes the extension dirs a previous setup seeded — so it runs before the seeding.
    await cleanupStaleState();

    // Fixtures before the servers: a server reads the generated registry index on its first request.
    ensureFixtureRepos();
    seedLocalExtensions();
    seedPublishedExtensionSources();
    prepareRegistryIndex();

    buildLaneBinary();

    // Boot: both lane servers and the mock LLM start without waiting on each other. Neither lane's
    // setup is the other's precondition, and the lane used to pay for a second compile between them.
    const started = Date.now();
    for (const lane of LANES) spawnLane(lane);
    if (useMock()) spawnMockLlm();
    await Promise.all([
        ...LANES.map((lane) => waitForServer(lane.port, lane.logFile)),
        ...(useMock() ? [waitForServer(MOCK_PORT, MOCK_LOG_FILE)] : []),
    ]);
    for (const lane of LANES) {
        console.log(`[global-setup] Lane ${lane.name} ready on port ${lane.port} (${ms(Date.now() - started)})`);
    }

    // The mock LLM is up before any task is created: title-gen dials the provider immediately.
    await Promise.all(LANES.map((lane) => seedLane(lane)));

    try { fs.writeFileSync(LOCK_FILE, ''); } catch { /* ignore */ }
}

/** Log in, point the lane at the mock provider, and create its seed project and two tasks. */
async function seedLane(lane: Lane): Promise<void> {
    const { cookie, csrfToken } = await login(lane.base);
    const session = (cookie.split('=')[1] || '').trim();
    fs.writeFileSync(lane.storageState, JSON.stringify({
        cookies: [{
            name: 'PHPSESSID',
            value: session,
            domain: 'localhost',
            path: '/',
            httpOnly: true,
            secure: false,
            sameSite: 'Lax',
            expires: Date.now() / 1000 + 86400 * 365,
        }],
        origins: [],
    }));
    // Legacy auth file for CSRF token
    fs.writeFileSync(lane.authFile, JSON.stringify({
        cookies: [{ name: 'PHPSESSID', value: session, domain: 'localhost', path: '/', httpOnly: true, secure: false, sameSite: 'Lax' }],
        csrfToken,
    }));
    console.log(`[global-setup] Logged in to ${lane.name} server`);

    if (useMock()) {
        const config = await getJson(`${lane.base}/api/config`, cookie);
        await putJson(`${lane.base}/api/config`, {
            ...config,
            providers: [{
                name: 'mock',
                display_name: 'Mock LLM',
                url: MOCK_URL,
                models: [{ name: 'mock-model', reasoning_effort: 'off' }],
                concurrency: 16,
            }],
            worker_provider: 'mock',
            worker_model: 'mock-model',
            utility_provider: 'mock',
            utility_model: 'mock-model',
        }, cookie, csrfToken);
        console.log(`[global-setup] Mock provider configured on ${lane.name} at ${MOCK_URL}`);
    } else {
        console.log(`[global-setup] ${lane.name}: using the real LLM endpoint (MIKE_USE_MOCK_LLM=0)`);
    }

    const project = await postJson(`${lane.base}/api/projects`, {
        name: 'test-project',
        path: TEST_REPO,
    }, cookie, csrfToken);
    console.log(`[global-setup] Test project created on ${lane.name}: ${project.id}`);

    const payload = (prompt: string) => useMock()
        ? { prompt, llama_server_url: MOCK_URL, model_name: 'mock-model' }
        : { prompt };
    const task1 = await postJson(`${lane.base}/api/projects/${project.id}/tasks`, payload('Test task for e2e tests'), cookie, csrfToken);
    const task2 = await postJson(`${lane.base}/api/projects/${project.id}/tasks`, payload('Second test task'), cookie, csrfToken);
    console.log(`[global-setup] Test tasks created on ${lane.name}: ${task1.id}, ${task2.id}`);
}

export async function teardown(): Promise<void> {
    console.error('[teardown] ENTRY');
    // Stop the lane's own processes by identity, and wait for them to exit: the data dirs must not be
    // removed while a server is still checkpointing SQLite into them.
    for (const lane of LANES) await stopLane(lane);
    await stopMockLlm();

    // Whoever still holds a lane port is not a process this run spawned — a dev server, or a straggler
    // from an interrupted one. Report it, take the port, and say whether the lane is clear.
    await verifyPortsFree([MOCK_PORT, KANBAN_PORT_ISOLATED, KANBAN_PORT]);

    // Remove local extension fixtures this (or an interrupted previous) setup created.
    removeSeededLocalExtensions();

    // Clean up orphaned sandbox containers
    try {
        const containers = execSync('sudo podman --runtime runsc ps --filter name=kanban-sandbox -q 2>/dev/null || true').toString().trim();
        if (containers) {
            execSync('sudo podman --runtime runsc rm -f ' + containers, { stdio: 'inherit' });
            console.log('[global-teardown] Orphaned sandbox containers cleaned up');
        }
    } catch {
        console.log('[global-teardown] Warning: failed to clean up sandbox containers');
    }

    // Delete test data, worktrees and the per-worker project repos.
    for (const dir of [TEST_DATA_DIR, TEST_DATA_DIR_ISOLATED, TEST_WORKTREES, TEST_OWN_REPOS]) {
        await removeTree(dir);
    }

    // Clean up lock, auth and PID files. The fixture cache (the repo templates, the i18n pack repo, the
    // registry index and their `.sha` markers) is deliberately NOT deleted — see cacheHit.
    for (const file of [
        LOCK_FILE, MOCK_PID_FILE,
        STORAGE_STATE_FILE, STORAGE_STATE_FILE_ISOLATED,
        ...LANES.flatMap((l) => [l.authFile, l.pidFile]),
    ]) {
        try { fs.unlinkSync(file); } catch { /* ignore */ }
    }
}

// --- The lane binary ---

/**
 * Compile both lane servers' binary, once, as a development build.
 *
 * No `-tags release`: the harness serves `frontend/dist` from disk so `npm run build` needs no Go
 * rebuild, and a release binary serves only the copy it was compiled with (AGENTS.md → Workflow).
 * Passing `--frontend-dist` is what makes that a stated requirement rather than a default the cwd
 * happens to satisfy — a release build refuses the flag and dies at boot, which is the readable
 * version of the mistake. The build is invoked every run: `go build`'s own cache is the content key,
 * and a lane that ran a stale binary would report green about code that never ran.
 */
function buildLaneBinary(): void {
    fs.mkdirSync(LANE_BIN_DIR, { recursive: true });
    const started = Date.now();
    try {
        execFileSync('go', ['build', '-trimpath', '-o', LANE_BIN, '.'], {
            cwd: PROJECT_ROOT,
            stdio: 'pipe',
            timeout: 300000,
        });
    } catch (e) {
        const detail = e instanceof Error ? `${e.message}\n${(e as any).stderr || ''}` : String(e);
        throw new Error(
            `[global-setup] could not build the lane binary (${LANE_BIN}). A missing `
            + `frontend/dist/index.html fails here too — run \`cd frontend && npm run build\` first.\n${detail}`,
        );
    }
    console.log(`[global-setup] Lane binary ${LANE_BIN} built in ${ms(Date.now() - started)}`);
}

function laneEnv(): NodeJS.ProcessEnv {
    return {
        ...process.env,
        ...NO_UPDATES_ENV,
        // The fixture repos and worktrees live under /tmp, outside the host directory jail.
        MIKE_WORKTREES_DIR: TEST_WORKTREES,
        MIKE_LOGIN_PASSWORD: TEST_PASSWORD,
        MIKE_REGISTRY_URL: TEST_REGISTRY_URL,
        MIKE_TLS: '0',
        DANGER_ZONE: '1',
        MIKE_ALLOW_ROOT_FS: '1',
    };
}

/** Spawn one lane server detached, with its own process group, and record the PID it answers to. */
function spawnLane(lane: Lane): void {
    // cwd stays the repo root so the lane's relative paths (the worktrees default, prompts) resolve
    // as they do for `go run .`.
    const argv = [LANE_BIN, '--data-dir', lane.dataDir, '--port', String(lane.port),
        '--frontend-dist', path.join(PROJECT_ROOT, 'frontend', 'dist')];
    const pid = spawnDetached(argv, { cwd: PROJECT_ROOT, env: laneEnv(), pidFile: lane.pidFile, logFile: lane.logFile });
    console.log(`[global-setup] Starting ${lane.name} test server (pid ${pid}) on port ${lane.port}...`);
}

function spawnMockLlm(): void {
    const pid = spawnDetached(
        [process.execPath, path.join(FRONTEND_DIR, 'tests', 'mock-llm.js'), String(MOCK_PORT)],
        { cwd: FRONTEND_DIR, pidFile: MOCK_PID_FILE, logFile: MOCK_LOG_FILE },
    );
    console.log(`[global-setup] Starting mock LLM server (pid ${pid}) on port ${MOCK_PORT}...`);
}

function spawnDetached(argv: string[], opts: { cwd: string; env?: NodeJS.ProcessEnv; pidFile: string; logFile: string }): number {
    // Truncated here, so what a failed run left behind is still readable when you go looking.
    const log = fs.openSync(opts.logFile, 'w');
    let child;
    try {
        child = spawn(argv[0], argv.slice(1), {
            cwd: opts.cwd,
            env: opts.env,
            stdio: ['ignore', log, log],
            detached: true,
        });
    } finally {
        // The child inherited its own descriptor; ours would only keep the file open for nothing.
        fs.closeSync(log);
    }
    if (!child.pid) throw new Error(`[global-setup] could not spawn ${argv[0]}`);
    // The child is unref'd, so an async spawn failure (the binary deleted between build and spawn)
    // would otherwise surface as an unhandled 'error' event inside Playwright's global setup.
    child.on('error', (e) => console.error(`[global-setup] ${argv[0]}: ${e.message}`));
    child.unref();
    try {
        fs.writeFileSync(opts.pidFile, JSON.stringify({ pid: child.pid, argv }));
    } catch { /* the identity sweep is the fallback */ }
    return child.pid;
}

// --- Stopping what setup started, by identity ---

/** The argv this run's processes were started with, for matching against /proc. */
function isLaneProcess(args: string[], lane: Lane): boolean {
    return args[0] === LANE_BIN && hasArgPair(args, '--data-dir', lane.dataDir);
}

function isMockProcess(args: string[]): boolean {
    return args.some((a) => a.endsWith('mock-llm.js')) && args.includes(String(MOCK_PORT));
}

function hasArgPair(args: string[], flag: string, value: string): boolean {
    const i = args.indexOf(flag);
    return i >= 0 && args[i + 1] === value;
}

function argvOf(pid: number): string[] {
    try {
        return fs.readFileSync(`/proc/${pid}/cmdline`, 'utf-8').split('\0').filter(Boolean);
    } catch {
        return [];
    }
}

/** Every live process whose argv satisfies `matches`. Token equality, so one data dir's prefix never
 *  answers for another's. */
function findProcesses(matches: (args: string[]) => boolean): number[] {
    const found: number[] = [];
    for (const entry of fs.readdirSync('/proc')) {
        if (!/^\d+$/.test(entry)) continue;
        const pid = Number(entry);
        if (pid === process.pid) continue;
        const args = argvOf(pid);
        if (args.length && matches(args)) found.push(pid);
    }
    return found;
}

function isAlive(pid: number): boolean {
    try {
        process.kill(pid, 0);
        return true;
    } catch {
        return false;
    }
}

/** A PID read from a file is only this run's process while its argv still says so — PIDs get reused. */
function liveMatching(pids: number[], matches: (args: string[]) => boolean): number[] {
    return pids.filter((pid) => isAlive(pid) && matches(argvOf(pid)));
}

function readPidFile(file: string): number[] {
    try {
        const parsed = JSON.parse(fs.readFileSync(file, 'utf-8')) as { pid?: number };
        return typeof parsed.pid === 'number' ? [parsed.pid] : [];
    } catch {
        return [];
    }
}

async function stopProcesses(pids: number[], label: string): Promise<void> {
    for (const pid of pids) {
        try { process.kill(pid, 'SIGTERM'); } catch { /* already gone */ }
    }
    let left = pids;
    const term = Date.now() + STOP_GRACE_MS;
    while (Date.now() < term) {
        left = left.filter(isAlive);
        if (!left.length) return;
        await sleep(100);
    }
    for (const pid of left) {
        console.error(`[global-setup] ${label} pid ${pid} ignored SIGTERM — SIGKILL`);
        try { process.kill(pid, 'SIGKILL'); } catch { /* already gone */ }
    }
    const kill = Date.now() + KILL_GRACE_MS;
    while (Date.now() < kill && left.some(isAlive)) await sleep(100);
    left = left.filter(isAlive);
    if (left.length) console.error(`[global-setup] Warning: ${label} pids ${left.join(',')} still alive after SIGKILL`);
}

async function stopLane(lane: Lane): Promise<void> {
    const matches = (args: string[]) => isLaneProcess(args, lane);
    // The PID file is the fast path; the sweep catches a lane whose PID file an interrupted run lost.
    const pids = [...readPidFile(lane.pidFile), ...findProcesses(matches)];
    const targets = liveMatching(pids, matches);
    if (targets.length) {
        const started = Date.now();
        await stopProcesses(targets, `lane ${lane.name}`);
        console.log(`[global-teardown] Lane ${lane.name} stopped (${ms(Date.now() - started)})`);
    }
    try { fs.unlinkSync(lane.pidFile); } catch { /* ignore */ }
}

async function stopMockLlm(): Promise<void> {
    const pids = liveMatching([...readPidFile(MOCK_PID_FILE), ...findProcesses(isMockProcess)], isMockProcess);
    if (pids.length) await stopProcesses(pids, 'mock LLM');
    try { fs.unlinkSync(MOCK_PID_FILE); } catch { /* ignore */ }
}

/** PIDs holding `port`, excluding this process (killing the runner would end the suite). */
function portPids(port: number): string[] {
    try {
        const output = execSync(`lsof -ti:${port}`, { encoding: 'utf-8', timeout: 3000 }).trim();
        return output ? output.split('\n').map((p) => p.trim()).filter((p) => p && p !== String(process.pid)) : [];
    } catch {
        return [];
    }
}

/**
 * The lane's own processes are stopped by identity, never by port: the port owner is a different
 * process from the one that should stop whenever a wrapper is in the way, and `lsof -ti:PORT` answers
 * with clients as well. This is the check that runs afterwards — a port still held belongs to a process
 * this run did not spawn (a `make run` dev server, a straggler of an interrupted run), and the suite
 * may not leave the lane's ports busy.
 */
async function verifyPortsFree(ports: number[]): Promise<void> {
    for (const port of ports) {
        let pids = portPids(port);
        if (!pids.length) continue;
        console.error(`[global-teardown] port ${port} still held by ${pids.join(',')} (not this run's lane) — stopping it`);
        for (const pid of pids) {
            try { process.kill(parseInt(pid), 'SIGTERM'); } catch { /* ignore */ }
        }
        const deadline = Date.now() + 8000;
        while (Date.now() < deadline) {
            pids = portPids(port);
            if (!pids.length) break;
            await sleep(200);
        }
        for (const pid of pids) {
            console.error(`[global-teardown] port ${port} pid ${pid} ignored SIGTERM — SIGKILL`);
            try { process.kill(parseInt(pid), 'SIGKILL'); } catch { /* ignore */ }
        }
        const killed = Date.now() + 3000;
        while (Date.now() < killed && portPids(port).length) await sleep(200);
        if (portPids(port).length) console.error(`[global-teardown] Warning: port ${port} is still busy`);
    }
}

/**
 * Remove a tree, retrying while a subprocess still writing holds it open. The lane's processes are
 * already gone by the time this runs, so ENOTEMPTY here is a straggler, not the server.
 */
async function removeTree(dir: string): Promise<boolean> {
    const deadline = Date.now() + REMOVE_BUDGET_MS;
    for (;;) {
        try {
            fs.rmSync(dir, { recursive: true, force: true });
            return true;
        } catch (e: any) {
            if (e?.code === 'ENOENT') return true;
            if (['ENOTEMPTY', 'EBUSY', 'EAGAIN', 'EPERM'].includes(e?.code) && Date.now() < deadline) {
                await sleep(200);
                continue;
            }
            console.error(`[global-teardown] Warning: left ${dir} behind (${e?.code || e})`);
            return false;
        }
    }
}

async function cleanupStaleState(): Promise<void> {
    // Leftover seeded fixtures from an interrupted run.
    removeSeededLocalExtensions();
    try { fs.unlinkSync(LOCK_FILE); } catch { /* ignore */ }
    // A stale lane server holds the ports and its SQLite files, so the next run would boot on the
    // previous run's database. The PID files name them; the sweep catches ones that lost theirs.
    for (const lane of LANES) await stopLane(lane);
    await stopMockLlm();
    await verifyPortsFree([MOCK_PORT, KANBAN_PORT, KANBAN_PORT_ISOLATED]);
    // Data dirs come last: a process above has to have let go of them first.
    for (const dir of [TEST_DATA_DIR, TEST_DATA_DIR_ISOLATED, TEST_WORKTREES, TEST_OWN_REPOS]) {
        await removeTree(dir);
    }
}

// --- Cached fixtures ---

function ms(elapsed: number): string {
    return `${(elapsed / 1000).toFixed(2)}s`;
}

function hashStrings(...parts: (string | Buffer)[]): string {
    const h = crypto.createHash('sha256');
    for (const p of parts) {
        h.update(Buffer.isBuffer(p) ? p : Buffer.from(p, 'utf-8'));
        h.update('\0');
    }
    return h.digest('hex');
}

/** Content hash of a source tree: relative paths and bytes, so an edit or a rename moves it. */
function hashTree(dir: string): string {
    const h = crypto.createHash('sha256');
    const walk = (rel: string) => {
        const entries = fs.readdirSync(path.join(dir, rel), { withFileTypes: true })
            .sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
        for (const entry of entries) {
            const child = rel ? path.join(rel, entry.name) : entry.name;
            if (entry.isDirectory()) {
                h.update(`d ${child}\n`);
                walk(child);
            } else {
                h.update(`f ${child}\n`);
                h.update(fs.readFileSync(path.join(dir, child)));
                h.update('\0');
            }
        }
    };
    walk('');
    return h.digest('hex');
}

/**
 * Whether `artifact` may be reused: the marker beside it holds the hash of the source as it is NOW,
 * the artifact itself is still there, and `verify` says the artifact still works. A source that moved
 * rebuilds it; so does an artifact that exists but no longer resolves to anything (the pack repo's
 * commit is gone). Nothing here reads an mtime.
 */
function cacheHit(artifact: string, key: string, verify: () => boolean): boolean {
    if (forceRebuild()) return false;
    let recorded: string | null = null;
    try { recorded = fs.readFileSync(`${artifact}.sha`, 'utf-8').trim(); } catch { return false; }
    return recorded === key && fs.existsSync(artifact) && verify();
}

/** Record what the artifact was just rebuilt from, so the next run has something to compare against. */
function markCached(artifact: string, key: string): void {
    try {
        fs.writeFileSync(`${artifact}.sha`, `${key}\n`);
    } catch (e) {
        console.error(`[global-setup] could not write ${artifact}.sha: ${e}`);
    }
}

// --- Fixture repos ---

interface FixtureRepo {
    dir: string;
    readme: string;
    label: string;
}

const FIXTURE_REPOS: FixtureRepo[] = [
    { dir: TEST_REPO, readme: '# Test Repository\n', label: 'test git repo' },
    { dir: TEST_REPO_2, readme: '# Second Test Repository\n', label: 'second test git repo' },
];

/**
 * Recreate the two fixture repos from a cached pristine template.
 *
 * The template is what the cache holds; the project's repo is a fresh copy of it every run, because
 * the project's repo is where specs commit, branch and leave worktree metadata. Reusing it in place
 * would let one run's tree be the next run's starting point, which is the coupling `isolatedProject`
 * exists to avoid (docs/testing.md → E2E conventions).
 */
function ensureFixtureRepos(): void {
    for (const repo of FIXTURE_REPOS) {
        const key = hashStrings('fixture-repo-v1', repo.readme, REPO_USER_EMAIL, REPO_USER_NAME);
        const template = `${repo.dir}.tpl`;
        const reusable = cacheHit(template, key, () =>
            fs.existsSync(path.join(template, '.git')) && fs.existsSync(path.join(template, 'README.md')));
        if (!reusable) {
            buildFixtureRepo(template, repo);
            markCached(template, key);
        }
        try { fs.rmSync(repo.dir, { recursive: true, force: true }); } catch { /* ignore */ }
        fs.cpSync(template, repo.dir, { recursive: true });
        console.log(`[global-setup] ${repo.label} ${reusable ? 'cached' : 'built'}: ${repo.dir}`);
    }
}

function buildFixtureRepo(dir: string, repo: FixtureRepo): void {
    console.log(`[global-setup] Creating ${repo.label} template...`);
    try { fs.rmSync(dir, { recursive: true, force: true }); } catch { /* ignore */ }
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(path.join(dir, 'README.md'), repo.readme);
    try {
        git(['init', '-q', dir]);
        // A local identity so backend commits (commit-and-done, which pass no -c flags) succeed even
        // where no global git config exists.
        git(['-C', dir, ...GIT_IDENTITY, 'config', 'user.email', REPO_USER_EMAIL]);
        git(['-C', dir, ...GIT_IDENTITY, 'config', 'user.name', REPO_USER_NAME]);
        git(['-C', dir, ...GIT_IDENTITY, 'add', 'README.md']);
        git(['-C', dir, ...GIT_IDENTITY, 'commit', '-q', '-m', 'Initial commit']);
    } catch (e) {
        console.error(`[global-setup] Failed to create the ${repo.label} template:`, e);
        throw e;
    }
}

function git(args: string[]): string {
    return execFileSync('git', args, { stdio: 'pipe' }).toString().trim();
}

// --- Local extension seeding ---

function readSeededExtensions(): string[] {
    try {
        return JSON.parse(fs.readFileSync(SEEDED_EXTENSIONS_FILE, 'utf-8')) as string[];
    } catch {
        return [];
    }
}

function writeSeededExtensions(list: string[]): void {
    try {
        fs.writeFileSync(SEEDED_EXTENSIONS_FILE, JSON.stringify(list));
    } catch { /* ignore */ }
}

/** Copy committed local-extension fixtures into the repo extensions dir, but
 *  only where the extension is not already present (dev installs win). */
function seedLocalExtensions(): void {
    let authors: string[];
    try {
        authors = fs.readdirSync(LOCAL_EXT_FIXTURES_DIR);
    } catch {
        return; // no fixtures dir — nothing to seed
    }
    const created = readSeededExtensions();
    for (const author of authors) {
        const fixtureAuthor = path.join(LOCAL_EXT_FIXTURES_DIR, author);
        if (!fs.statSync(fixtureAuthor).isDirectory()) continue;
        for (const name of fs.readdirSync(fixtureAuthor)) {
            const src = path.join(fixtureAuthor, name);
            if (!fs.statSync(src).isDirectory()) continue;
            const dest = path.join(PROJECT_ROOT, 'extensions', author, name);
            if (fs.existsSync(dest)) continue; // pre-existing dev install wins
            fs.cpSync(src, dest, { recursive: true });
            created.push(dest);
            console.log(`[global-setup] Seeded local extension fixture: ${author}/${name}`);
        }
    }
    writeSeededExtensions(created);
}

/** Seed the published extensions the specs install, from the sibling extensions repo. */
function seedPublishedExtensionSources(): void {
    const created = readSeededExtensions();
    const missing: string[] = [];
    for (const ext of LOCAL_EXT_PUBLISHED) {
        const dest = path.join(PROJECT_ROOT, 'extensions', ext.author, ext.name);
        if (fs.existsSync(dest)) continue;
        const src = path.join(PUBLISHED_EXT_SOURCE, ext.subdir);
        if (!fs.existsSync(src)) {
            missing.push(`${src}  (needed at extensions/${ext.author}/${ext.name})`);
            continue;
        }
        fs.cpSync(src, dest, { recursive: true });
        created.push(dest);
        console.log(`[global-setup] Seeded published extension: ${ext.author}/${ext.name}`);
    }
    writeSeededExtensions(created);
    if (missing.length) {
        throw new Error(
            `[global-setup] local extension source is missing:\n  ${missing.join('\n  ')}\n` +
            'Specs that install it with install-local cannot run. Restore the sibling checkout, or place a '
            + 'copy of the extension under extensions/<author>/<name>.');
    }
}

/** Remove exactly the extension dirs a (possibly previous) setup created. */
function removeSeededLocalExtensions(): void {
    for (const dir of readSeededExtensions()) {
        try {
            fs.rmSync(dir, { recursive: true, force: true });
            // Drop the now-empty author dir if setup created it too.
            fs.rmdirSync(path.dirname(dir));
        } catch { /* not empty or already gone — leave it */ }
    }
    try {
        fs.unlinkSync(SEEDED_EXTENSIONS_FILE);
    } catch { /* ignore */ }
}

// --- Registry index ---

/**
 * Write the registry index the servers read, reusing the previous run's while its inputs agree.
 *
 * The key is the fixture plus the i18n pack commit it was resolved to, so a rebuilt pack repo (whose
 * content moved, therefore whose commit moved) rewrites the index. The verification is that the
 * artifact holds no unresolved placeholder: a pin of `__I18N_PACK_COMMIT__` is the one state that
 * reads as a spec failure instead of a setup failure.
 */
function prepareRegistryIndex(): void {
    const commit = buildI18nPackRepo();
    let fixture: string;
    try {
        fixture = fs.readFileSync(REGISTRY_FIXTURE_FILE, 'utf-8');
    } catch (e) {
        console.error(`[global-setup] cannot read ${REGISTRY_FIXTURE_FILE}: ${e}`);
        return;
    }
    const index = commit
        ? fixture.split(I18N_PACK_COMMIT_PLACEHOLDER).join(commit)
        : fixture;
    const key = hashStrings('registry-index-v1', index);
    // No commit means no resolvable pin: write the fixture as it is and let the spec say so.
    const reusable = !commit || cacheHit(GENERATED_REGISTRY_FILE, key, () =>
        // An unresolved placeholder is the one state that reads as a spec failure, not a setup one.
        !fs.readFileSync(GENERATED_REGISTRY_FILE, 'utf-8').includes(I18N_PACK_COMMIT_PLACEHOLDER));
    if (!reusable) {
        try {
            fs.writeFileSync(GENERATED_REGISTRY_FILE, index);
            markCached(GENERATED_REGISTRY_FILE, key);
        } catch (e) {
            console.error(`[global-setup] cannot write ${GENERATED_REGISTRY_FILE}: ${e}`);
            return;
        }
    }
    console.log(
        `[global-setup] Registry index ${reusable ? 'cached' : 'written'}`
        + (commit ? ` — i18n pack pinned to ${commit.slice(0, 12)}`
            : ' WITHOUT a resolved i18n pack commit — e2e/i18n-first-run-offer will fail; see the git error above'),
    );
}

/**
 * The commit the i18n pack repo holds, building it from `tests/fixtures/i18n-pack-repo` when the cache
 * does not answer, or null when git could not do it.
 *
 * The identity and both commit dates are fixed, so the commit is a function of the pack's content
 * alone: the same fixture commits to the same hash on any machine and in any run, which is what makes
 * the repo cacheable — and what keeps a stale generated registry correct rather than plausible.
 *
 * When the repo cannot be built (no git, no identity, a read-only /tmp) the placeholder is left in
 * place rather than replaced with a plausible-looking fake: the one spec that installs this entry
 * asserts its commit resolved, so a broken setup reads as "setup did not build the pack repo" instead
 * of a checkout error naming a hash that was never a commit.
 */
function buildI18nPackRepo(): string | null {
    if (!fs.existsSync(I18N_PACK_FIXTURE_DIR)) {
        console.error(`[global-setup] no i18n pack fixture at ${I18N_PACK_FIXTURE_DIR}`);
        return null;
    }
    const key = hashStrings('i18n-pack-repo-v1', hashTree(I18N_PACK_FIXTURE_DIR));
    if (cacheHit(I18N_PACK_REPO, key, () => resolveRepoHead(I18N_PACK_REPO) !== null)) {
        const head = resolveRepoHead(I18N_PACK_REPO);
        if (head) {
            console.log(`[global-setup] i18n pack repo cached at ${head.slice(0, 12)}`);
            return head;
        }
    }
    const built = commitI18nPackRepo();
    // A repo git could not build gets no marker, so the next run tries again instead of believing one.
    if (built) markCached(I18N_PACK_REPO, key);
    console.log(built
        ? `[global-setup] i18n pack repo built at ${built.slice(0, 12)}`
        : '[global-setup] i18n pack repo NOT built');
    return built;
}

/**
 * The repo's HEAD, or null unless it is a commit the repo actually holds. A git-pinned extension
 * install is resolved by commit, so a cached repo is reusable only while this answers.
 */
function resolveRepoHead(repo: string): string | null {
    try {
        const head = git(['-C', repo, 'rev-parse', 'HEAD']);
        if (!/^[0-9a-f]{40}$/i.test(head)) return null;
        git(['-C', repo, 'cat-file', '-e', `${head}^{commit}`]);
        return head;
    } catch {
        return null;
    }
}

function commitI18nPackRepo(): string | null {
    // Fixed dates so the hash is the content's, not the clock's. An explicit identity: this must work
    // on a machine with no global git config.
    const gitEnv = {
        ...process.env,
        GIT_AUTHOR_DATE: '2020-01-01T00:00:00Z',
        GIT_COMMITTER_DATE: '2020-01-01T00:00:00Z',
    };
    const gitIn = (args: string[]) =>
        execFileSync('git', ['-C', I18N_PACK_REPO, ...args], { stdio: 'pipe', env: gitEnv }).toString().trim();
    try {
        fs.rmSync(I18N_PACK_REPO, { recursive: true, force: true });
        fs.cpSync(I18N_PACK_FIXTURE_DIR, I18N_PACK_REPO, { recursive: true });
        execFileSync('git', ['init', '-q', I18N_PACK_REPO], { stdio: 'pipe', env: gitEnv });
        gitIn([...GIT_IDENTITY, 'add', '-A']);
        gitIn([...GIT_IDENTITY, 'commit', '-q', '-m', 'fixture: i18n translation pack']);
        const head = gitIn(['rev-parse', 'HEAD']);
        if (!/^[0-9a-f]{40}$/i.test(head)) throw new Error(`unexpected HEAD: ${head}`);
        return head;
    } catch (e) {
        console.error(`[global-setup] could not build the i18n pack repo: ${e}`);
        return null;
    }
}

// --- Helpers ---

function sleep(msToSleep: number): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, msToSleep));
}

/** Check if port is free. */
async function isPortFree(port: number): Promise<boolean> {
    return new Promise((resolve) => {
        const req = http.get(`http://localhost:${port}/`, () => {
            req.destroy();
            resolve(false);
        });
        req.on('error', () => {
            resolve(true);
        });
        req.setTimeout(500, () => {
            req.destroy();
            resolve(false);
        });
        req.end();
    });
}

function waitForServer(port: number, logFile?: string, timeout = 15000): Promise<void> {
    return new Promise((resolve, reject) => {
        const start = Date.now();
        const tryConnect = () => {
            const req = http.get(`http://localhost:${port}/`, () => {
                req.destroy(); // release the port socket
                resolve();
            });
            req.on('error', () => {
                if (Date.now() - start < timeout) {
                    setTimeout(tryConnect, 100);
                } else {
                    // The process was spawned, so a port that never answers means it exited or refused
                    // to start. Its own output is the only record of why.
                    reject(new Error(`Server on port ${port} did not start within ${timeout}ms`
                        + (logFile ? `\n--- ${logFile} ---\n${tailOf(logFile)}` : '')));
                }
            });
            req.end();
        };
        tryConnect();
    });
}

function tailOf(file: string, lines = 30): string {
    try {
        return fs.readFileSync(file, 'utf-8').trimEnd().split('\n').slice(-lines).join('\n');
    } catch {
        return '(unreadable)';
    }
}

function postJson(url: string, body: any, cookie?: string, csrfToken?: string): Promise<any> {
    return requestJson('POST', url, body, cookie, csrfToken);
}

function putJson(url: string, body: any, cookie?: string, csrfToken?: string): Promise<any> {
    return requestJson('PUT', url, body, cookie, csrfToken);
}

function requestJson(method: string, url: string, body?: any, cookie?: string, csrfToken?: string): Promise<any> {
    return new Promise((resolve, reject) => {
        const data = JSON.stringify(body);
        const headers: Record<string, string> = {
            'Content-Type': 'application/json',
            'Content-Length': Buffer.byteLength(data).toString(),
        };
        if (cookie) headers['Cookie'] = cookie;
        if (csrfToken) headers['X-CSRF-Token'] = csrfToken;
        const req = http.request(url, { method, headers }, (res) => {
            let responseData = '';
            res.on('data', (chunk) => { responseData += chunk; });
            res.on('end', () => {
                try { resolve(JSON.parse(responseData)); } catch { resolve(null); }
            });
        });
        req.on('error', reject);
        req.write(data);
        req.end();
    });
}

function getJson(url: string, cookie?: string): Promise<any> {
    return new Promise((resolve, reject) => {
        const headers: Record<string, string> = {};
        if (cookie) headers['Cookie'] = cookie;
        const req = http.get(url, { headers }, (res) => {
            let responseData = '';
            res.on('data', (chunk) => { responseData += chunk; });
            res.on('end', () => {
                try { resolve(JSON.parse(responseData)); } catch { resolve(null); }
            });
        });
        req.on('error', reject);
        req.end();
    });
}

async function login(base: string): Promise<{ cookie: string; csrfToken: string }> {
    return new Promise((resolve, reject) => {
        const data = JSON.stringify({ password: TEST_PASSWORD });
        const req = http.request(`${base}/api/auth/login`, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
                'Content-Length': Buffer.byteLength(data).toString(),
            },
        }, (res) => {
            const setCookie = res.headers['set-cookie'];
            const csrf = res.headers['x-csrf-token'];
            const csrfToken = Array.isArray(csrf) ? csrf[0] : csrf;
            let responseData = '';
            res.on('data', (chunk) => { responseData += chunk; });
            res.on('end', () => {
                if (!setCookie || setCookie.length === 0) {
                    reject(new Error(`Login failed: ${responseData}`));
                    return;
                }
                const cookie = Array.isArray(setCookie) ? setCookie[0] : String(setCookie);
                resolve({ cookie: cookie.split(';')[0].trim(), csrfToken: csrfToken || '' });
            });
        });
        req.on('error', reject);
        req.write(data);
        req.end();
    });
}

export default async function main(): Promise<void> {
    // A lock plus a live server means a previous run left the lane standing: run teardown for it before
    // setting up. Anything else needs no teardown — setup's own stale-state cleanup covers it.
    if (fs.existsSync(LOCK_FILE) && !(await isPortFree(KANBAN_PORT))) {
        console.log('[global-setup] Teardown phase: stopping running servers');
        await teardown();
    } else if (fs.existsSync(LOCK_FILE)) {
        console.log('[global-setup] Stale lock detected, cleaning up');
    }
    await setup();
}
