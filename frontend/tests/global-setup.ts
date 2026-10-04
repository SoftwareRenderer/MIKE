/**
 * Global setup/teardown for e2e tests.
 * Set USE_MOCK_LLM=0 to use real LLM endpoint (default: mock).
 */

import { spawn } from 'node:child_process';
import https from 'node:https';
import net from 'node:net';
import fs from 'node:fs';
import path from 'node:path';
import { execSync, execFileSync } from 'node:child_process';
import { pairAndReadSession, readPairingCode } from './helpers-auth.mjs';
import { laneRequestOptions } from './lane-tls';

const __dirname = path.dirname(new URL(import.meta.url).pathname);
const PROJECT_ROOT = path.resolve(__dirname, '../..');
const FRONTEND_DIR = path.resolve(__dirname, '..');

const TEST_REPO = '/tmp/kanban-test-repo';
const TEST_REPO_2 = '/tmp/kanban-test-repo-2';
const TEST_DATA_DIR = '/tmp/kanban-test-data';
const TEST_DATA_DIR_ISOLATED = '/tmp/kanban-test-data-isolated';
const TEST_WORKTREES = '/tmp/kanban-test-worktrees';
// Where the three children's own output goes. A turn-bound failure used to leave no server-side trace at all,
// because every spawn ran with `stdio: 'ignore'` and the only evidence was Playwright's DOM snapshot. The
// servers inherit MIKE_LOG_LEVEL from the shell, so a diagnosis can ask for `debug` without a code change.
const SERVER_LOG = '/tmp/kanban-test-server.log';
const SERVER_LOG_ISOLATED = '/tmp/kanban-test-server-isolated.log';
const MOCK_LLM_LOG = '/tmp/kanban-test-mock-llm.log';
const HARNESS_LOGS = [SERVER_LOG, SERVER_LOG_ISOLATED, MOCK_LLM_LOG];
// Per-worker project repos created by helpers.ts isolatedProject() — scratch, like the worktrees.
const TEST_OWN_REPOS = '/tmp/kanban-test-repos';
const KANBAN_PORT = 3000;
const KANBAN_PORT_ISOLATED = 3001;
const MOCK_PORT = 9999;
const KANBAN_BASE = `https://localhost:${KANBAN_PORT}`;
const KANBAN_BASE_ISOLATED = `https://localhost:${KANBAN_PORT_ISOLATED}`;
const LOCK_FILE = '/tmp/kanban-test-setup.lock';
// Playwright storage state files — read at context creation time
const STORAGE_STATE_FILE = '/tmp/kanban-test-storage-state.json';
const STORAGE_STATE_FILE_ISOLATED = '/tmp/kanban-test-storage-state.json.isolated';
const KANBAN_BIN = 'go';
const KANBAN_RUN_ARGS = ['run', '.'];
// Release updates are switched off for the run: the boot pass (`updateCheck.start()` in App.svelte) asks the
// opt-in question of every fresh browser and then dials the manifest host. A spec that wants the dot stubs
// /api/updates/status.
const NO_UPDATES_ENV = { MIKE_DISABLE_UPDATES: '1' };
// Sign-in is a WebAuthn ceremony, and a browser lane runs a dozen of them a minute through one address
// (every pairing, every assertion, from the setup walk and from the specs in parallel). The budget that
// governs a real install is held by `backend/server/rate_limit_test.go`, which pins it at 5/IP/minute;
// the lane raises it so the limit cannot be what a ceremony spec fails on.
// Point the test servers at a local registry fixture, so a registry fetch never depends on the hosted one.
// One entry is pinned to a git commit that only exists once setup has built the repo it points at, so the
// fixture is copied to GENERATE_REGISTRY_FILE with that placeholder resolved rather than served directly.
const REGISTRY_FIXTURE_FILE = path.join(__dirname, 'registry-fixture.json');
const GENERATED_REGISTRY_FILE = '/tmp/kanban-test-registry.json';
const TEST_REGISTRY_URL = 'file://' + GENERATED_REGISTRY_FILE;
// The i18n pack the registry fixture offers: committed sources under tests/fixtures/i18n-pack-repo, committed
// into a throwaway local git repo so the registry's git-pinned install path has something real to install.
// The commit is rebuilt every run with a fixed identity and date, so identical content gives the same hash.
/** The scroll specs' transcript: one project, one task, one long conversation, built once per run. */
const CHAT_REPO = '/tmp/kanban-test-chat';
const CHAT_FIXTURE_FILE = '/tmp/kanban-test-chat-fixture.json';
/**
 * Rows the transcript holds. Scroll height comes from the rows the app has loaded and the server answers five
 * rows per older page, so this is several pages beyond what opening the chat pulls in.
 */
const CHAT_ROWS = 60;

const I18N_PACK_REPO = '/tmp/kanban-test-i18n-pack-repo';
const I18N_PACK_FIXTURE_DIR = path.join(__dirname, 'fixtures', 'i18n-pack-repo');
const I18N_PACK_COMMIT_PLACEHOLDER = '__I18N_PACK_COMMIT__';
// Local extension fixtures seeded into the repo extensions dir for the Browse specs: a fresh checkout has an
// empty `extensions/`. Only missing entries are created and teardown removes exactly what setup created, so a
// developer's own installs are never touched; SEEDED_EXTENSIONS_FILE is what an interrupted run leaves behind.
const LOCAL_EXT_FIXTURES_DIR = path.join(__dirname, 'fixtures', 'local-extensions');
const SEEDED_EXTENSIONS_FILE = '/tmp/kanban-test-seeded-extensions.json';
// Published extensions the specs install with `install-local` have no fixture here — their source is the
// sibling extensions repo, and **deleting an extension deletes its directory**, so a run that uninstalls one
// consumes the source the next run needs. Setup seeds it where absent, and refuses the run naming the path.
const PUBLISHED_EXT_SOURCE = path.resolve(PROJECT_ROOT, '..', 'MIKE-extensions');
const LOCAL_EXT_PUBLISHED: { author: string; name: string; subdir: string }[] = [
    { author: 'softwarerenderer', name: 'html-preview', subdir: 'ui/html-preview' },
];

let mockServer: ReturnType<typeof spawn> | null = null;
let kanbanServer: ReturnType<typeof spawn> | null = null;
let kanbanServerIsolated: ReturnType<typeof spawn> | null = null;

/**
 * The servers serve `frontend/dist` from disk and this setup never builds it, so a stale build answers every
 * spec with code the run did not change. The newest file a build reads is compared with the newest artifact.
 */
function newestMtime(target: string): number {
    if (!fs.existsSync(target)) return 0;
    const stat = fs.statSync(target);
    if (!stat.isDirectory()) return stat.mtimeMs;
    let newest = stat.mtimeMs;
    for (const entry of fs.readdirSync(target, { withFileTypes: true })) {
        if (entry.name === 'node_modules' || entry.name.startsWith('.')) continue;
        newest = Math.max(newest, newestMtime(path.join(target, entry.name)));
    }
    return newest;
}

function assertFrontendBuilt(): void {
    const dist = path.join(FRONTEND_DIR, 'dist');
    if (!fs.existsSync(path.join(dist, 'index.html'))) {
        throw new Error('[global-setup] frontend/dist/index.html is missing — run `cd frontend && npm run build` first');
    }
    const built = newestMtime(dist);
    const sources = ['src', 'vite.config.ts', 'svelte.config.ts', 'tsconfig.json']
        .map(rel => newestMtime(path.join(FRONTEND_DIR, rel)));
    const source = Math.max(...sources);
    if (source > built) {
        throw new Error('[global-setup] frontend/dist is older than frontend/src — every spec would run against the '
            + 'previous build. Run `cd frontend && npm run build` (or `make test-e2e`, which builds first).');
    }
}

/**
 * Build the long conversation the scroll specs measure a reader inside, once for the run. A transcript built
 * inside a test puts the fixture inside that test's own timeout.
 */
async function seedChatConversation(cookie: string, csrfToken: string): Promise<void> {
    if (!fs.existsSync(CHAT_REPO)) fs.cpSync(TEST_REPO, CHAT_REPO, { recursive: true });
    const project = await postJson(`${KANBAN_BASE}/api/projects`, { name: 'chat-fixture', path: CHAT_REPO }, cookie, csrfToken);
    if (!project?.id) throw new Error(`[global-setup] chat fixture project could not be created: ${JSON.stringify(project)}`);
    const task = await postJson(`${KANBAN_BASE}/api/projects/${project.id}/tasks`,
        { title: 'chat-transcript', prompt: 'scroll fixture', status: 'in_progress' }, cookie, csrfToken);
    if (!task?.id) throw new Error(`[global-setup] chat fixture task could not be created: ${JSON.stringify(task)}`);

    let made = 0;
    for (let i = 0; i < CHAT_ROWS; i++) {
        const row = await postJson(`${KANBAN_BASE}/api/projects/${project.id}/tasks/${task.id}/messages`, {
            role: 'user',
            content: `seed row ${i}\n${'filler text that gives the entry some height '.repeat(3)}`,
        }, cookie, csrfToken);
        if (!row?.id) break;
        made++;
    }
    if (made < CHAT_ROWS) {
        throw new Error(`[global-setup] chat fixture stopped at row ${made} of ${CHAT_ROWS} — the scroll specs need the whole transcript`);
    }
    fs.writeFileSync(CHAT_FIXTURE_FILE, JSON.stringify({ projectId: project.id, taskId: task.id, rows: made }));
    console.log(`[global-setup] Chat fixture: project ${project.id}, task ${task.id}, ${made} rows`);
}

async function setup(): Promise<void> {
    assertFrontendBuilt();
    ensureTestRepo();
    seedLocalExtensions();
    seedPublishedExtensionSources();
    prepareRegistryIndex();
    await killKanbanServer();

    console.log('[global-setup] Starting test kanban server...');
    const serverLog = fs.openSync(SERVER_LOG, 'w');
    kanbanServer = spawn('setsid', [KANBAN_BIN, ...KANBAN_RUN_ARGS, '--data-dir', TEST_DATA_DIR], {
        cwd: PROJECT_ROOT,
        // The fixture repos and worktrees live under /tmp, outside the host directory jail.
        env: { ...process.env, ...NO_UPDATES_ENV, MIKE_LOGIN_RATE_LIMIT: '120', MIKE_WORKTREES_DIR: TEST_WORKTREES, MIKE_REGISTRY_URL: TEST_REGISTRY_URL, DANGER_ZONE: '1', MIKE_ALLOW_ROOT_FS: '1' },
        stdio: ['ignore', serverLog, serverLog],
        detached: true,
    });
    kanbanServer.unref();

    await waitForServer(KANBAN_PORT, 15000);
    console.log(`[global-setup] Test kanban server ready on port ${KANBAN_PORT}`);

    // The lane server booted into an empty data directory, so it printed the one pairing code it minted
    // for itself; a browser with a virtual authenticator spends it the way a person would.
    const { cookie: sessionCookie, csrfToken } = await pairAndReadSession(KANBAN_BASE, await readPairingCode(SERVER_LOG));
    fs.writeFileSync(STORAGE_STATE_FILE, JSON.stringify({
        cookies: [{
            name: 'PHPSESSID',
            value: sessionCookie.split('=')[1],
            domain: 'localhost',
            path: '/',
            httpOnly: true,
            secure: true,
            sameSite: 'Lax',
            expires: Date.now() / 1000 + 86400 * 365,
        }],
        origins: [],
    }));
    // Legacy auth file for CSRF token
    fs.writeFileSync('/tmp/kanban-test-auth.json', JSON.stringify({
        cookies: [{ name: 'PHPSESSID', value: sessionCookie.split('=')[1], domain: 'localhost', path: '/', httpOnly: true, secure: true, sameSite: 'Lax' }],
        csrfToken,
    }));
    console.log(`[global-setup] Paired a passkey with the test server`);

    const useMock = process.env.MIKE_USE_MOCK_LLM !== '0';
    const mockUrl = `http://localhost:${MOCK_PORT}/v1`;
    if (useMock) {
        const config = await getJson(`${KANBAN_BASE}/api/config`, sessionCookie);
        await putJson(`${KANBAN_BASE}/api/config`, {
            ...config,
            providers: [{
                name: 'mock',
                display_name: 'Mock LLM',
                url: mockUrl,
                models: [{ name: 'mock-model', reasoning_effort: 'off' }],
                concurrency: 16,
            }],
            worker_provider: 'mock',
            worker_model: 'mock-model',
            utility_provider: 'mock',
            utility_model: 'mock-model',
        }, sessionCookie, csrfToken);
        console.log(`[global-setup] Mock provider configured at ${mockUrl}`);
    }

    // Start mock LLM before creating tasks (title-gen calls LLM immediately)
    if (useMock) {
        console.log('[global-setup] Starting mock LLM server...');
        const mockLog = fs.openSync(MOCK_LLM_LOG, 'w');
        mockServer = spawn('setsid', ['node', 'tests/mock-llm.js', MOCK_PORT.toString()], {
            cwd: FRONTEND_DIR,
            stdio: ['ignore', mockLog, mockLog],
            detached: true,
        });
        mockServer.unref();
        await waitForServer(MOCK_PORT);
        console.log(`[global-setup] Mock LLM server ready on port ${MOCK_PORT}`);
    } else {
        console.log('[global-setup] Using real LLM endpoint (MIKE_USE_MOCK_LLM=0)');
    }

    const project = await postJson(`${KANBAN_BASE}/api/projects`, {
        name: 'test-project',
        path: TEST_REPO,
    }, sessionCookie, csrfToken);
    console.log(`[global-setup] Test project created: ${project.id}`);

    const taskPayload = useMock
        ? { prompt: 'Test task for e2e tests', llama_server_url: mockUrl, model_name: 'mock-model' }
        : { prompt: 'Test task for e2e tests' };
    const taskPayload2 = useMock
        ? { prompt: 'Second test task', llama_server_url: mockUrl, model_name: 'mock-model' }
        : { prompt: 'Second test task' };
    const task1 = await postJson(`${KANBAN_BASE}/api/projects/${project.id}/tasks`, taskPayload, sessionCookie, csrfToken);
    const task2 = await postJson(`${KANBAN_BASE}/api/projects/${project.id}/tasks`, taskPayload2, sessionCookie, csrfToken);
    console.log(`[global-setup] Test tasks created: ${task1.id}, ${task2.id}`);

    await seedChatConversation(sessionCookie, csrfToken);

    console.log('[global-setup] Starting isolated kanban server...');
    const isolatedLog = fs.openSync(SERVER_LOG_ISOLATED, 'w');
    kanbanServerIsolated = spawn('setsid', [KANBAN_BIN, ...KANBAN_RUN_ARGS, '--data-dir', TEST_DATA_DIR_ISOLATED, '--port', String(KANBAN_PORT_ISOLATED)], {
        cwd: PROJECT_ROOT,
        env: { ...process.env, ...NO_UPDATES_ENV, MIKE_LOGIN_RATE_LIMIT: '120', MIKE_WORKTREES_DIR: TEST_WORKTREES, MIKE_REGISTRY_URL: TEST_REGISTRY_URL, DANGER_ZONE: '1', MIKE_ALLOW_ROOT_FS: '1' },
        stdio: ['ignore', isolatedLog, isolatedLog],
        detached: true,
    });
    kanbanServerIsolated.unref();
    await waitForServer(KANBAN_PORT_ISOLATED, 15000);
    console.log(`[global-setup] Isolated kanban server ready on port ${KANBAN_PORT_ISOLATED}`);

    const { cookie: sessionCookieIso, csrfToken: csrfTokenIso } = await pairAndReadSession(
        KANBAN_BASE_ISOLATED, await readPairingCode(SERVER_LOG_ISOLATED));
    fs.writeFileSync(STORAGE_STATE_FILE_ISOLATED, JSON.stringify({
        cookies: [{
            name: 'PHPSESSID',
            value: sessionCookieIso.split('=')[1],
            domain: 'localhost',
            path: '/',
            httpOnly: true,
            secure: true,
            sameSite: 'Lax',
            expires: Date.now() / 1000 + 86400 * 365,
        }],
        origins: [],
    }));
    // Legacy auth file for CSRF token
    fs.writeFileSync('/tmp/kanban-test-auth.json.isolated', JSON.stringify({
        cookies: [{ name: 'PHPSESSID', value: sessionCookieIso.split('=')[1], domain: 'localhost', path: '/', httpOnly: true, secure: true, sameSite: 'Lax' }],
        csrfToken: csrfTokenIso,
    }));
    console.log(`[global-setup] Paired a passkey with the isolated server`);

    // Configure mock provider on isolated server
    if (useMock) {
        const configIso = await getJson(`${KANBAN_BASE_ISOLATED}/api/config`, sessionCookieIso);
        await putJson(`${KANBAN_BASE_ISOLATED}/api/config`, {
            ...configIso,
            providers: [{
                name: 'mock',
                display_name: 'Mock LLM',
                url: mockUrl,
                models: [{ name: 'mock-model', reasoning_effort: 'off' }],
                concurrency: 16,
            }],
            worker_provider: 'mock',
            worker_model: 'mock-model',
            utility_provider: 'mock',
            utility_model: 'mock-model',
        }, sessionCookieIso, csrfTokenIso);
        console.log(`[global-setup] Mock provider configured on isolated server`);
    }

    // Create test project on isolated server
    const projectIso = await postJson(`${KANBAN_BASE_ISOLATED}/api/projects`, {
        name: 'test-project',
        path: TEST_REPO,
    }, sessionCookieIso, csrfTokenIso);
    console.log(`[global-setup] Test project created on isolated server: ${projectIso.id}`);

    // Create test tasks on isolated server
    const taskIso = await postJson(`${KANBAN_BASE_ISOLATED}/api/projects/${projectIso.id}/tasks`, taskPayload, sessionCookieIso, csrfTokenIso);
    const taskIso2 = await postJson(`${KANBAN_BASE_ISOLATED}/api/projects/${projectIso.id}/tasks`, taskPayload2, sessionCookieIso, csrfTokenIso);
    console.log(`[global-setup] Test tasks created on isolated server: ${taskIso.id}, ${taskIso2.id}`);

    // Create lock file
    try { fs.writeFileSync(LOCK_FILE, ''); } catch { /* ignore */ }
}

/**
 * Remove the sandbox containers this run created. Ownership is read from what a container mounts, never from
 * its name, so a removal cannot reach the developer's own project containers. Both podman stores are tried:
 * inspecting the rootful store for a rootless container answers an empty list.
 */
function removeHarnessContainers(): void {
    const roots = [TEST_REPO, TEST_REPO_2, TEST_OWN_REPOS, TEST_WORKTREES, TEST_DATA_DIR];
    for (const podman of ['podman', 'sudo podman']) {
        let ids: string[];
        try {
            const out = execSync(`${podman} ps -aq --filter name=kanban-sandbox 2>/dev/null`).toString().trim();
            ids = out ? out.split('\n') : [];
        } catch {
            continue; // that podman flavour is not available here
        }
        const doomed: string[] = [];
        for (const id of ids) {
            try {
                const mounts = execSync(`${podman} inspect -f '{{range .Mounts}}{{.Source}} {{end}}' ${id}`).toString();
                if (roots.some(root => mounts.split(' ').some(src => src === root || src.startsWith(`${root}/`)))) doomed.push(id);
            } catch { /* unreadable container: not ours to guess about */ }
        }
        if (doomed.length) {
            try {
                execSync(`${podman} rm -f ${doomed.join(' ')}`, { stdio: 'ignore' });
                console.log(`[global-teardown] Removed ${doomed.length} sandbox container(s) of this harness`);
            } catch {
                console.log('[global-teardown] Warning: sandbox containers of this harness survived');
            }
        }
        return; // one flavour is the host's; the other has nothing of ours
    }
}

/**
 * Print what survived the run: a path the harness owns that still exists, and the containers the sandbox left
 * up. Reported rather than removed here, because a leftover may belong to another run.
 */
function reportLeftovers(): void {
    const roots = [TEST_REPO, TEST_REPO_2, CHAT_REPO, CHAT_FIXTURE_FILE, TEST_DATA_DIR, TEST_DATA_DIR_ISOLATED, TEST_WORKTREES, TEST_OWN_REPOS, ...HARNESS_LOGS];
    const survivors = roots.filter(root => fs.existsSync(root));
    let containers = '?';
    try {
        containers = execSync('podman ps -aq 2>/dev/null | wc -l').toString().trim();
    } catch { /* no podman on this host */ }
    console.log(`[global-teardown] leftovers: paths=${survivors.length ? survivors.join(' ') : 'none'} containers=${containers}`);
}

export async function teardown(): Promise<void> {
    console.error('[teardown] ENTRY');
    // Stop servers — by identity first (the port does not reach the whole process tree), then by
    // port for anything else (the mock LLM).
    killTestServers();
    for (const port of [MOCK_PORT, KANBAN_PORT_ISOLATED, KANBAN_PORT]) {
        console.error(`[teardown] killing port ${port}...`);
        killPort(port);
        console.error(`[teardown] port ${port} stopped`);
    }

    // Remove local extension fixtures this (or an interrupted previous) setup created.
    removeSeededLocalExtensions();

    removeHarnessContainers();

    // Delete test data — only once the server has let go of it (see waitForFilesRelease)
    waitForFilesRelease(TEST_DATA_DIR);
    try {
        fs.rmSync(TEST_DATA_DIR, { recursive: true, force: true });
        console.log('[global-teardown] Test data directory deleted');
    } catch {
        console.log('[global-teardown] Test data directory already gone');
    }

    // Delete isolated test data
    waitForFilesRelease(TEST_DATA_DIR_ISOLATED);
    try {
        fs.rmSync(TEST_DATA_DIR_ISOLATED, { recursive: true, force: true });
        console.log('[global-teardown] Isolated test data directory deleted');
    } catch {
        console.log('[global-teardown] Isolated test data directory already gone');
    }

    // Delete test worktrees
    try {
        fs.rmSync(TEST_WORKTREES, { recursive: true, force: true });
        console.log('[global-teardown] Test worktrees directory deleted');
    } catch {
        console.log('[global-teardown] Test worktrees already gone');
    }

    // The run's evidence, gone with the run. An interrupted run's log is truncated by the next setup's open.
    for (const log of HARNESS_LOGS) {
        try { fs.rmSync(log, { force: true }); } catch { /* ignore */ }
    }

    // Delete the per-worker project repos (helpers.ts isolatedProject)
    try { fs.rmSync(TEST_OWN_REPOS, { recursive: true, force: true }); } catch { /* ignore */ }

    // The fixture repos are inputs this setup builds from `tests/test-repo`, so a stale checkout must not be
    // the next run's starting point.
    for (const repo of [TEST_REPO, TEST_REPO_2, CHAT_REPO]) {
        try { fs.rmSync(repo, { recursive: true, force: true }); } catch { /* ignore */ }
    }
    try { fs.rmSync(CHAT_FIXTURE_FILE, { force: true }); } catch { /* ignore */ }

    // Delete the generated registry index and the i18n pack repo setup built
    try { fs.rmSync(GENERATED_REGISTRY_FILE, { force: true }); } catch { /* ignore */ }
    try { fs.rmSync(I18N_PACK_REPO, { recursive: true, force: true }); } catch { /* ignore */ }

    reportLeftovers();

    // Clean up lock and auth files
    try { fs.unlinkSync(LOCK_FILE); } catch { /* ignore */ }
    try { fs.unlinkSync(STORAGE_STATE_FILE); } catch { /* ignore */ }
    try { fs.unlinkSync(STORAGE_STATE_FILE_ISOLATED); } catch { /* ignore */ }
    try { fs.unlinkSync('/tmp/kanban-test-auth.json'); } catch { /* ignore */ }
    try { fs.unlinkSync('/tmp/kanban-test-auth.json.isolated'); } catch { /* ignore */ }
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
 * Build the i18n pack repo and write the registry index the servers read. The fixture pins its translation pack
 * to a commit, and that hash cannot live in the fixture because it is a property of the content: setup builds
 * the repo and substitutes its HEAD. When the repo cannot be built the placeholder stays in place, so the spec
 * that installs this entry reports that setup did not build it instead of naming a hash that was never a commit.
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
    try {
        fs.writeFileSync(GENERATED_REGISTRY_FILE, index);
        console.log(
            commit
                ? `[global-setup] Registry index written with the i18n pack pinned to ${commit.slice(0, 12)}`
                : '[global-setup] Registry index written WITHOUT a resolved i18n pack commit — '
                  + 'e2e/i18n-first-run-offer will fail; see the git error above',
        );
    } catch (e) {
        console.error(`[global-setup] cannot write ${GENERATED_REGISTRY_FILE}: ${e}`);
    }
}

/**
 * Commit `tests/fixtures/i18n-pack-repo` into a throwaway repo and return its HEAD, or null when git could not
 * do it. The identity and both commit dates are fixed, so the hash is a function of the pack's content alone.
 */
function buildI18nPackRepo(): string | null {
    const gitEnv = {
        ...process.env,
        GIT_AUTHOR_DATE: '2020-01-01T00:00:00Z',
        GIT_COMMITTER_DATE: '2020-01-01T00:00:00Z',
    };
    // An explicit identity: this must work on a machine with no global git config.
    const identity = ['-c', 'user.name=MIKE e2e', '-c', 'user.email=e2e@localhost.test',
        '-c', 'commit.gpgsign=false', '-c', 'core.autocrlf=false'];
    const git = (args: string[]) =>
        execFileSync('git', ['-C', I18N_PACK_REPO, ...args], { stdio: 'pipe', env: gitEnv }).toString().trim();
    try {
        fs.rmSync(I18N_PACK_REPO, { recursive: true, force: true });
        fs.cpSync(I18N_PACK_FIXTURE_DIR, I18N_PACK_REPO, { recursive: true });
        execFileSync('git', ['init', '-q', I18N_PACK_REPO], { stdio: 'pipe', env: gitEnv });
        git([...identity, 'add', '-A']);
        git([...identity, 'commit', '-q', '-m', 'fixture: i18n translation pack']);
        const head = git(['rev-parse', 'HEAD']);
        if (!/^[0-9a-f]{40}$/i.test(head)) throw new Error(`unexpected HEAD: ${head}`);
        return head;
    } catch (e) {
        console.error(`[global-setup] could not build the i18n pack repo: ${e}`);
        return null;
    }
}

// --- Helpers ---

function ensureTestRepo(): void {
    // Recreate test repos for clean state
    for (const repo of [TEST_REPO, TEST_REPO_2]) {
        const label = repo === TEST_REPO ? 'test git repo' : 'second test git repo';
        // Remove existing repo
        try { fs.rmSync(repo, { recursive: true, force: true }); } catch { /* ignore */ }
        console.log(`[global-setup] Creating ${label}...`);
        fs.mkdirSync(repo, { recursive: true });
        const readme = repo === TEST_REPO ? '# Test Repository\n' : '# Second Test Repository\n';
        fs.writeFileSync(`${repo}/README.md`, readme);
        try {
            execSync('git init', { cwd: repo });
            // Set a local git identity so backend commits (e.g. commit-and-done,
            // which don't pass -c flags) succeed even when no global git config exists.
            execSync('git config user.email "test@kanban.local"', { cwd: repo });
            execSync('git config user.name "Test"', { cwd: repo });
            execSync('git add README.md', { cwd: repo });
            execSync('git commit -m "Initial commit"', { cwd: repo });
        } catch (e) {
            console.error(`[global-setup] Failed to create ${label}:`, e);
            throw e;
        }
    }
}

/**
 * Stop the test servers by identity (--data-dir) and wait for them to be gone. The port is not
 * enough: the servers are grandchildren (`setsid go run . …`), so the port owner is not the process
 * that should stop, and `lsof -ti:PORT` also lists the port's clients. A server that outlives its
 * run keeps the ports and its SQLite files, so the next run boots on the previous run's database.
 * The pattern is anchored because the two data dirs share a prefix.
 */
function killTestServers(): void {
    // Isolated first: its pattern is the only one that cannot also match the main server.
    for (const dir of [TEST_DATA_DIR_ISOLATED, TEST_DATA_DIR]) {
        const pattern = `--data-dir ${dir}([[:space:]]|$)`;
        let pids = '';
        try {
            pids = execSync(`pgrep -f "${pattern}" 2>/dev/null || true`, { encoding: 'utf-8', timeout: 5000 }).trim();
        } catch { /* none */ }
        const mine = String(process.pid);
        let victims = pids ? pids.split('\n').map((x) => x.trim()).filter((x) => x && x !== mine) : [];
        if (victims.length === 0) continue;
        for (const pid of victims) { try { process.kill(parseInt(pid), 'SIGTERM'); } catch { /* ignore */ } }
        const deadline = Date.now() + 8000;
        while (Date.now() < deadline) {
            const still = survivors(pattern, mine);
            if (still.length === 0) return;
            victims = still;
            execSync('sleep 0.25');
        }
        for (const pid of victims) {
            console.error(`[global-setup] test server pid ${pid} (${dir}) ignored SIGTERM — SIGKILL`);
            try { process.kill(parseInt(pid), 'SIGKILL'); } catch { /* ignore */ }
        }
        const killed = Date.now() + 5000;
        while (Date.now() < killed && survivors(pattern, mine).length > 0) execSync('sleep 0.2');
    }
}

function survivors(pattern: string, selfPid: string): string[] {
    try {
        const out = execSync(`pgrep -f "${pattern}" 2>/dev/null || true`, { encoding: 'utf-8', timeout: 5000 }).trim();
        return out ? out.split('\n').map((x) => x.trim()).filter((x) => x && x !== selfPid) : [];
    } catch {
        return [];
    }
}

/** PIDs listening on `port`, excluding this process (killing the runner would end the suite). */
function portPids(port: number): string[] {
    try {
        const output = execSync(`lsof -ti:${port}`, { encoding: 'utf-8', timeout: 3000 }).trim();
        return output ? output.split('\n').map((p) => p.trim()).filter((p) => p && p !== String(process.pid)) : [];
    } catch {
        return [];
    }
}

/**
 * Stop whoever owns `port` and do not return until it is free. A wedged server ignores SIGTERM and
 * keeps the listener; a polite one stays bound for a moment while it shuts down.
 */
function killPort(port: number): void {
    let pids = portPids(port);
    if (pids.length === 0) return;
    for (const pid of pids) {
        try { process.kill(parseInt(pid), 'SIGTERM'); } catch { /* ignore */ }
    }
    const deadline = Date.now() + 8000;
    while (Date.now() < deadline) {
        pids = portPids(port);
        if (pids.length === 0) return;
        execSync('sleep 0.25');
    }
    for (const pid of pids) {
        console.error(`[global-setup] port ${port} pid ${pid} ignored SIGTERM — SIGKILL`);
        try { process.kill(parseInt(pid), 'SIGKILL'); } catch { /* ignore */ }
    }
    const killed = Date.now() + 3000;
    while (Date.now() < killed && portPids(port).length > 0) execSync('sleep 0.2');
}

/**
 * Wait until no process holds anything open inside `dir`. Deleting the data dir while the killed
 * server is still checkpointing SQLite leaves it re-created afterwards, and the next run then
 * recovers the PREVIOUS run's database from the leftover WAL.
 */
// Budget: server.ShutdownTimeout (the worst-case graceful exit) plus slack to notice.
function waitForFilesRelease(dir: string, timeoutMs = 20000): void {
    if (!fs.existsSync(dir)) return;
    const holders = (): string => {
        try {
            return execSync(`lsof +D ${dir} -t 2>/dev/null || true`, { encoding: 'utf-8', timeout: 5000 }).trim();
        } catch {
            return '';
        }
    };
    const deadline = Date.now() + timeoutMs;
    for (;;) {
        const open = holders();
        if (!open) return;
        if (Date.now() > deadline) {
            console.error(`[global-setup] Warning: ${dir} still held by ${open.split('\n').join(',')} — deleting anyway`);
            return;
        }
        execSync('sleep 0.25');
    }
}

function cleanupStaleState(keepLockFile: boolean): void {
    // Leftover seeded fixtures from an interrupted run.
    removeSeededLocalExtensions();
    if (!keepLockFile) {
        try { fs.unlinkSync(LOCK_FILE); } catch { /* ignore */ }
    }
    // Kill stale processes (no shell pipeline to avoid self-kill)
    killTestServers();
    for (const port of [MOCK_PORT, KANBAN_PORT, KANBAN_PORT_ISOLATED]) {
        killPort(port);
    }
    // Clean stale data — after the servers above have actually released their files
    waitForFilesRelease(TEST_DATA_DIR);
    waitForFilesRelease(TEST_DATA_DIR_ISOLATED);
    try { fs.rmSync(TEST_DATA_DIR, { recursive: true, force: true }); } catch { /* ignore */ }
    try { fs.rmSync(TEST_DATA_DIR_ISOLATED, { recursive: true, force: true }); } catch { /* ignore */ }
    try { fs.rmSync(TEST_WORKTREES, { recursive: true, force: true }); } catch { /* ignore */ }
    try { fs.rmSync(TEST_OWN_REPOS, { recursive: true, force: true }); } catch { /* ignore */ }
}

async function killKanbanServer(): Promise<void> {
    let killed = false;
    try {
        const pids = execSync('lsof -ti:3000 2>/dev/null || true').toString().trim();
        // Skip this process — killing it would SIGTERM the Playwright runner.
        const toKill = pids ? pids.split('\n').map(p => p.trim()).filter(p => p && p !== String(process.pid)) : [];
        if (toKill.length > 0) {
            execSync(`kill ${toKill.join(' ')} 2>/dev/null || true`);
            console.log('[global-setup] Killed existing server on port 3000');
            killed = true;
        }
    } catch { /* no server running */ }

    if (!killed) return;

    // Wait for port to free (up to 5s)
    for (let attempt = 0; attempt < 10; attempt++) {
        if (!(await portAnswering(KANBAN_PORT))) return;
        await new Promise(r => setTimeout(r, 500));
    }
}

/** Whether something answers on the port. A TCP connection is the whole question: the lane servers serve
 *  TLS, and a probe that spoke HTTP would have to trust a certificate it is only checking for. */
function portAnswering(port: number, timeout = 500): Promise<boolean> {
    return new Promise((resolve) => {
        const socket = net.connect({ host: 'localhost', port });
        const done = (answer: boolean) => {
            socket.destroy();
            resolve(answer);
        };
        socket.once('connect', () => done(true));
        socket.once('error', () => done(false));
        socket.setTimeout(timeout, () => done(false));
    });
}

async function waitForServer(port: number, timeout = 5000): Promise<void> {
    const start = Date.now();
    for (;;) {
        if (await portAnswering(port)) return;
        if (Date.now() - start >= timeout) {
            throw new Error(`Server on port ${port} did not start within ${timeout}ms`);
        }
        await new Promise(r => setTimeout(r, 200));
    }
}

function postJson(url: string, body: any, cookie?: string, csrfToken?: string): Promise<any> {
    return new Promise((resolve, reject) => {
        const data = JSON.stringify(body);
        const headers: Record<string, string> = {
            'Content-Type': 'application/json',
            'Content-Length': Buffer.byteLength(data).toString(),
        };
        if (cookie) headers['Cookie'] = cookie;
        if (csrfToken) headers['X-CSRF-Token'] = csrfToken;
        const req = https.request(url, {
            ...laneRequestOptions(url),
            method: 'POST',
            headers,
        }, (res) => {
            let responseData = '';
            res.on('data', chunk => responseData += chunk);
            res.on('end', () => {
                try { resolve(JSON.parse(responseData)); } catch { resolve(null); }
            });
        });
        req.on('error', reject);
        req.write(data);
        req.end();
    });
}

function putJson(url: string, body: any, cookie?: string, csrfToken?: string): Promise<any> {
    return new Promise((resolve, reject) => {
        const data = JSON.stringify(body);
        const headers: Record<string, string> = {
            'Content-Type': 'application/json',
            'Content-Length': Buffer.byteLength(data).toString(),
        };
        if (cookie) headers['Cookie'] = cookie;
        if (csrfToken) headers['X-CSRF-Token'] = csrfToken;
        const req = https.request(url, {
            ...laneRequestOptions(url),
            method: 'PUT',
            headers,
        }, (res) => {
            let responseData = '';
            res.on('data', chunk => responseData += chunk);
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
        const req = https.get(url, { ...laneRequestOptions(url), headers }, (res) => {
            let responseData = '';
            res.on('data', chunk => responseData += chunk);
            res.on('end', () => {
                try { resolve(JSON.parse(responseData)); } catch { resolve(null); }
            });
        });
        req.on('error', reject);
        req.end();
    });
}

export default async function main(): Promise<void> {
    // Only teardown if lock exists AND servers are running.
    // Stale lock + dead servers => clean up and run setup fresh.
    const lockExists = fs.existsSync(LOCK_FILE);
    const serversRunning = await portAnswering(KANBAN_PORT);

    if (lockExists && serversRunning) {
        console.log('[global-setup] Teardown phase: stopping running servers');
        cleanupStaleState(true);
        await teardown();
        try { fs.unlinkSync(LOCK_FILE); } catch { /* ignore */ }
    } else {
        if (lockExists && !serversRunning) {
            console.log('[global-setup] Stale lock detected, cleaning up');
        }
        cleanupStaleState(false);
    }
    await setup();
}
