import { describe, it, expect } from 'vitest';
import {
    DANGEROUS_PERMISSIONS,
    isDangerousPermission,
    hasDangerousPermissions,
    sortPermissions,
    hasNetworkCapability,
    requiresInstallReview,
    dependencyCacheDeclaration,
    requiresSetupAfterInstall,
    needsDependencyGrant,
    agentMountDeclaration,
    agentMountGrant,
    needsAgentMountGrant,
    agentMountDraft,
    agentMountUpdate,
    grantFingerprint,
    dependencyDraft,
} from '../extension-permissions';

describe('isDangerousPermission (sandbox-escaping permissions only)', () => {
    it('flags the sandbox-escaping permissions', () => {
        for (const perm of ['network.proxy', 'preview.serveScripts', 'preview.serveNetwork']) {
            expect(isDangerousPermission(perm)).toBe(true);
        }
        expect(DANGEROUS_PERMISSIONS.size).toBe(3);
    });

    it('does not flag sandbox-contained permissions', () => {
        for (const perm of [
            'editor.getContent',
            'editor.setContent',
            'editor.save',
            'editor.setOverlay',
            'editor.contentChange',
            'preview.serve',
            'preview.release',
            'ui.status',
            'ui.setSize',
            'ui.modal',
            'createTask.events.paste',
            'storage.get',
        ]) {
            expect(isDangerousPermission(perm)).toBe(false);
        }
    });

    it('does not flag unknown permission names', () => {
        expect(isDangerousPermission('files.read')).toBe(false);
        expect(isDangerousPermission('')).toBe(false);
    });
});

describe('hasDangerousPermissions', () => {
    it('is true when at least one permission is sandbox-escaping', () => {
        expect(hasDangerousPermissions(['editor.save', 'network.proxy'])).toBe(true);
        expect(hasDangerousPermissions(['preview.serveScripts'])).toBe(true);
    });

    it('is false for lists without dangerous permissions', () => {
        expect(hasDangerousPermissions(['editor.save', 'storage.get', 'ui.setSize'])).toBe(false);
    });

    it('is false for an empty list', () => {
        expect(hasDangerousPermissions([])).toBe(false);
    });

    it('is false for unknown permission names', () => {
        expect(hasDangerousPermissions(['files.read', ''])).toBe(false);
    });
});

describe('sortPermissions (stable: dangerous first, manifest order kept)', () => {
    it('moves dangerous permissions to the top, keeping relative order in both groups', () => {
        const perms = [
            'editor.addFooterButton',
            'preview.serveNetwork',
            'editor.setOverlay',
            'network.proxy',
            'preview.serveScripts',
            'editor.getContent',
        ];
        expect(sortPermissions(perms)).toEqual([
            'preview.serveNetwork',
            'network.proxy',
            'preview.serveScripts',
            'editor.addFooterButton',
            'editor.setOverlay',
            'editor.getContent',
        ]);
    });

    it('returns a permutation of the input — nothing added, dropped, or duplicated', () => {
        const perms = ['network.proxy', 'editor.save', 'preview.serveScripts'];
        const sorted = sortPermissions(perms);
        expect(sorted).toHaveLength(perms.length);
        expect([...sorted].sort()).toEqual([...perms].sort());
    });

    it('leaves a list without dangerous permissions untouched', () => {
        const perms = ['editor.addFooterButton', 'editor.getContent', 'ui.status'];
        expect(sortPermissions(perms)).toEqual(perms);
    });

    it('leaves an all-dangerous list in its original order', () => {
        const perms = ['preview.serveNetwork', 'network.proxy'];
        expect(sortPermissions(perms)).toEqual(perms);
    });

    it('returns an empty list for empty input', () => {
        expect(sortPermissions([])).toEqual([]);
    });

    it('does not mutate the input array', () => {
        const perms = ['editor.save', 'network.proxy'];
        sortPermissions(perms);
        expect(perms).toEqual(['editor.save', 'network.proxy']);
    });
});

describe('hasNetworkCapability (manifest-level network capability)', () => {
    it('is true when a network capability object is declared', () => {
        expect(hasNetworkCapability({ network: { allowed_domains: ['api.github.com'] } })).toBe(true);
    });

    it('is true for an empty network capability object (declared = dangerous)', () => {
        expect(hasNetworkCapability({ network: {} })).toBe(true);
    });

    it('is false without a network capability', () => {
        expect(hasNetworkCapability({ ui: { permissions: [] } })).toBe(false);
        expect(hasNetworkCapability({})).toBe(false);
    });

    it('is false for missing, null, or non-object capabilities / network values', () => {
        expect(hasNetworkCapability(undefined)).toBe(false);
        expect(hasNetworkCapability(null)).toBe(false);
        expect(hasNetworkCapability({ network: null })).toBe(false);
    });
});

describe('requiresInstallReview (network capability OR dangerous permissions)', () => {
    it('is true for a network capability with no UI permissions', () => {
        expect(requiresInstallReview({ capabilities: { network: { allowed_domains: ['x.dev'] } } })).toBe(true);
    });

    it('is true for dangerous UI permissions without a network capability', () => {
        expect(requiresInstallReview({ capabilities: { ui: { permissions: ['network.proxy'] } } })).toBe(true);
    });

    it('is true when both are declared', () => {
        expect(
            requiresInstallReview({
                capabilities: { ui: { permissions: ['network.proxy'] }, network: { allowed_domains: ['x.dev'] } },
            }),
        ).toBe(true);
    });

    it('is false for safe permissions only', () => {
        expect(requiresInstallReview({ capabilities: { ui: { permissions: ['editor.save', 'storage.get'] } } })).toBe(false);
    });

    it('is false for missing or empty capabilities', () => {
        expect(requiresInstallReview({ capabilities: undefined })).toBe(false);
        expect(requiresInstallReview({})).toBe(false);
        expect(requiresInstallReview({ capabilities: {} })).toBe(false);
    });

    it('does not treat a legacy "proxy" key as the network capability', () => {
        expect(requiresInstallReview({ capabilities: { proxy: { allowed_domains: ['x.dev'] } } })).toBe(false);
    });

    it('is true when the extension declares a dependency cache it will ask about', () => {
        // Installing grants nothing, but it means a folder on this machine gets asked for later,
        // and "install this?" is the last question that can still be answered with the facts.
        expect(
            requiresInstallReview({
                capabilities: { lsp: { language: 'go', dependency_cache: { container_path: '/go/pkg/mod', env: 'GOMODCACHE' } } },
            }),
        ).toBe(true);
    });

    it('is true for a local entry whose declaration is under lsp.sandbox', () => {
        // The local endpoint returns the manifest as written, with the declaration under lsp.sandbox.
        expect(
            requiresInstallReview({
                capabilities: {
                    lsp: { language: 'go', sandbox: { dependency_cache: { container_path: '/fake/mod', env: 'FAKE_CACHE' } } },
                },
            }),
        ).toBe(true);
    });

    it('is false for an LSP extension that declares no dependency cache', () => {
        expect(requiresInstallReview({ capabilities: { lsp: { language: 'go' } } })).toBe(false);
    });
});

describe('dependencyCacheDeclaration', () => {
    it('reads the projected declaration', () => {
        expect(
            dependencyCacheDeclaration({ lsp: { dependency_cache: { container_path: '/go/pkg/mod', env: 'GOMODCACHE' } } }),
        ).toEqual({ container_path: '/go/pkg/mod', env: 'GOMODCACHE' });
    });

    it('reads the manifest shape a local entry carries', () => {
        expect(
            dependencyCacheDeclaration({
                lsp: { language: 'go', sandbox: { dependency_cache: { container_path: '/fake/mod', env: 'FAKE_CACHE' } } },
            }),
        ).toEqual({ container_path: '/fake/mod', env: 'FAKE_CACHE' });
    });

    it('prefers the projection when a document somehow carries both', () => {
        expect(
            dependencyCacheDeclaration({
                lsp: {
                    dependency_cache: { container_path: '/go/pkg/mod', env: 'GOMODCACHE' },
                    sandbox: { dependency_cache: { container_path: '/elsewhere', env: 'OTHER' } },
                },
            }),
        ).toEqual({ container_path: '/go/pkg/mod', env: 'GOMODCACHE' });
    });

    it('says nothing about write access, because there is none to disclose', () => {
        // The extension's own container reads the directory; the containers that fill it are the
        // user's tasks and terminals. A `read_only` left in a published manifest must not come back as
        // a permission claim, which is what an alarm over a write nobody asked for would be.
        expect(
            dependencyCacheDeclaration({ lsp: { dependency_cache: { container_path: '/go/pkg/mod', env: 'GOMODCACHE', read_only: false } } }),
        ).toEqual({ container_path: '/go/pkg/mod', env: 'GOMODCACHE' });
    });

    it('is null for anything incomplete, so the disclosure cannot invent one', () => {
        expect(dependencyCacheDeclaration(undefined)).toBeNull();
        expect(dependencyCacheDeclaration({})).toBeNull();
        expect(dependencyCacheDeclaration({ lsp: {} })).toBeNull();
        expect(dependencyCacheDeclaration({ lsp: { dependency_cache: null } })).toBeNull();
        // A half-declared cache would render a sentence with an empty path in it.
        expect(dependencyCacheDeclaration({ lsp: { dependency_cache: { env: 'GOMODCACHE' } } })).toBeNull();
        expect(dependencyCacheDeclaration({ lsp: { dependency_cache: { container_path: '/go/pkg/mod' } } })).toBeNull();
        // The same rule for the manifest shape.
        expect(dependencyCacheDeclaration({ lsp: { sandbox: {} } })).toBeNull();
        expect(dependencyCacheDeclaration({ lsp: { sandbox: { dependency_cache: { env: 'FAKE_CACHE' } } } })).toBeNull();
    });

    it('is null for capabilities that are not the objects they claim to be', () => {
        expect(dependencyCacheDeclaration({ lsp: null })).toBeNull();
        expect(dependencyCacheDeclaration({ lsp: 'go' })).toBeNull();
        expect(dependencyCacheDeclaration({ lsp: { dependency_cache: 'GOMODCACHE' } })).toBeNull();
        expect(dependencyCacheDeclaration({ lsp: { sandbox: null } })).toBeNull();
        expect(dependencyCacheDeclaration({ lsp: { sandbox: { dependency_cache: [] } } })).toBeNull();
    });
});

describe('requiresSetupAfterInstall (what a Browse card badges)', () => {
    it('is true for an entry whose dependency directory is configured after installation', () => {
        expect(requiresSetupAfterInstall({ lsp: { dependency_cache: { container_path: '/go/pkg/mod', env: 'GOMODCACHE' } } })).toBe(true);
        expect(
            requiresSetupAfterInstall({ lsp: { sandbox: { dependency_cache: { container_path: '/fake/mod', env: 'FAKE_CACHE' } } } }),
        ).toBe(true);
    });

    it('is false for an extension that will work straight out of its image', () => {
        expect(requiresSetupAfterInstall(undefined)).toBe(false);
        expect(requiresSetupAfterInstall({})).toBe(false);
        expect(requiresSetupAfterInstall({ lsp: { language: 'typescript' } })).toBe(false);
        expect(requiresSetupAfterInstall({ ui: { permissions: ['editor.save'] } })).toBe(false);
    });

    it('does not confuse a permission review with setup: only a declared directory counts', () => {
        // Dangerous permissions get the review dialog; they are not a step to do after installing.
        expect(requiresSetupAfterInstall({ network: { allowed_domains: ['x.dev'] } })).toBe(false);
        expect(requiresSetupAfterInstall({ ui: { permissions: ['network.proxy'] } })).toBe(false);
    });
});

describe('needsDependencyGrant', () => {
    const DECLARED = { container_path: '/go/pkg/mod', env: 'GOMODCACHE' };

    it('is true for a declarer nobody has given a directory yet', () => {
        expect(needsDependencyGrant({ enabled: true, sandbox_config: { dependency_cache: DECLARED } })).toBe(true);
        expect(needsDependencyGrant({ enabled: true, sandbox_config: { dependency_cache: DECLARED, dependency_host_path: '' } })).toBe(true);
        // Whitespace is not a directory: the endpoint trims, and a field of spaces is not a grant.
        expect(needsDependencyGrant({ enabled: true, sandbox_config: { dependency_cache: DECLARED, dependency_host_path: '   ' } })).toBe(true);
    });

    it('is false once a directory is shared', () => {
        expect(needsDependencyGrant({
            enabled: true,
            sandbox_config: { dependency_cache: DECLARED, dependency_host_path: '/home/tester/go/pkg/mod' },
        })).toBe(false);
    });

    it('is false for an extension that declares none', () => {
        // tsserver reads the checkout's node_modules; warning about a directory it would never read
        // would train people to ignore the warning that matters.
        expect(needsDependencyGrant({ enabled: true, sandbox_config: {} })).toBe(false);
        expect(needsDependencyGrant({ enabled: true })).toBe(false);
        expect(needsDependencyGrant({})).toBe(false);
    });

    it('is false for an extension that is switched off', () => {
        // One that cannot run is not waiting on anyone, and the warning returns when it is enabled.
        expect(needsDependencyGrant({ enabled: false, sandbox_config: { dependency_cache: DECLARED } })).toBe(false);
    });
});

describe('agentMountDeclaration', () => {
    const PI = {
        agent: {
            wasm_module: 'adapter.wasm',
            sandbox: {
                mount: [{ host_path: '~/.pi/agent', container_path: '/root/.pi/agent', mode: 'rw' }],
                env: [{ key: 'PI_HOME', value: '/root/.pi' }],
            },
        },
    };

    it('reads the proposal out of the manifest shape the server sends', () => {
        expect(agentMountDeclaration(PI)).toEqual([
            { host_path: '~/.pi/agent', container_path: '/root/.pi/agent', mode: 'rw' },
        ]);
    });

    it('is empty for anything that declares no mount', () => {
        expect(agentMountDeclaration(null)).toEqual([]);
        expect(agentMountDeclaration({ lsp: {} })).toEqual([]);
        expect(agentMountDeclaration({ agent: { wasm_module: 'a.wasm' } })).toEqual([]);
        expect(agentMountDeclaration({ agent: { sandbox: { env: [] } } })).toEqual([]);
        // A folder with no path inside the container is not a proposal; there is nothing to mount it to.
        expect(agentMountDeclaration({ agent: { sandbox: { mount: [{ host_path: '~/x' }] } } })).toEqual([]);
    });
});

describe('agentMountGrant', () => {
    it('reads the stored grant', () => {
        expect(agentMountGrant({
            config: { sandbox_mount: [{ host_path: '/home/dev/.pi/agent', container_path: '/root/.pi/agent', mode: 'rw' }] },
        })).toEqual([{ host_path: '/home/dev/.pi/agent', container_path: '/root/.pi/agent', mode: 'rw' }]);
    });

    it('is empty when nothing was granted, revoked, or written wrong', () => {
        expect(agentMountGrant({ config: {} })).toEqual([]);
        expect(agentMountGrant({})).toEqual([]);
        expect(agentMountGrant({ config: { sandbox_mount: [] } })).toEqual([]);
        expect(agentMountGrant({ config: { sandbox_mount: [{ host_path: '' }] } })).toEqual([]);
        // A path missing on either side is not an approval, and the endpoint would refuse it.
        expect(agentMountGrant({ config: { sandbox_mount: [{ host_path: '/home/dev/.pi/agent' }] } })).toEqual([]);
        expect(agentMountGrant({ config: { sandbox_mount: 'not-an-array' } })).toEqual([]);
    });
});

describe('needsAgentMountGrant', () => {
    const CAPS = { agent: { sandbox: { mount: [{ host_path: '~/.pi/agent', container_path: '/root/.pi/agent', mode: 'rw' }] } } };
    const GRANTED = [{ host_path: '/home/dev/.pi/agent', container_path: '/root/.pi/agent', mode: 'rw' }];

    it('is true for an agent nobody has given a folder yet', () => {
        expect(needsAgentMountGrant({ enabled: true, manifest: { capabilities: CAPS }, config: {} })).toBe(true);
        expect(needsAgentMountGrant({ enabled: true, manifest: { capabilities: CAPS }, config: { sandbox_mount: [] } })).toBe(true);
    });

    it('is false once the folder is granted, or the agent is switched off', () => {
        expect(needsAgentMountGrant({ enabled: true, manifest: { capabilities: CAPS }, config: { sandbox_mount: GRANTED } })).toBe(false);
        expect(needsAgentMountGrant({ enabled: false, manifest: { capabilities: CAPS }, config: {} })).toBe(false);
    });

    it('is false for an extension that asked for nothing', () => {
        expect(needsAgentMountGrant({ enabled: true, manifest: { capabilities: { lsp: {} } }, config: {} })).toBe(false);
    });
});

describe('the agent mount as an install gate', () => {
    // The agent shape the registry index carries, as written by scripts/gen-registry.py.
    const CAPS = { agent: { sandbox: { mount: [{ host_path: '~/.pi/agent', container_path: '/root/.pi/agent', mode: 'rw' }] } } };

    it('a declarer goes through the install review, so the mount question is asked', () => {
        expect(requiresInstallReview({ capabilities: CAPS })).toBe(true);
        expect(requiresSetupAfterInstall(CAPS)).toBe(true);
    });

    it('an agent that asks for no folder installs without one', () => {
        expect(requiresInstallReview({ capabilities: { agent: { wasm_module: 'a.wasm' } } })).toBe(false);
        expect(requiresSetupAfterInstall({ agent: { wasm_module: 'a.wasm' } })).toBe(false);
    });
});

describe('the Edit dialog\u2019s grant drafts', () => {
    const DECLARED = { agent: { sandbox: { mount: [{ host_path: '~/.pi/agent', container_path: '/root/.pi/agent', mode: 'rw' }] } } };

    it('opens with one row per declared folder, filled from what is approved', () => {
        const rows = agentMountDraft({
            manifest: { capabilities: DECLARED },
            config: { sandbox_mount: [{ host_path: '/home/a/.pi/agent', container_path: '/root/.pi/agent', mode: 'ro' }] },
        });
        expect(rows).toEqual([{ host_path: '/home/a/.pi/agent', container_path: '/root/.pi/agent', mode: 'ro', suggestion: '~/.pi/agent' }]);
    });

    it('keeps an approved folder the manifest no longer declares, because the save sends the whole list', () => {
        const rows = agentMountDraft({
            manifest: { capabilities: {} },
            config: { sandbox_mount: [{ host_path: '/srv/old', container_path: '/old', mode: '' }] },
        });
        expect(rows).toEqual([{ host_path: '/srv/old', container_path: '/old', mode: 'ro', suggestion: '' }]);
    });

    it('sends only the rows with a host folder, and sends it trimmed', () => {
        expect(agentMountUpdate([
            { host_path: '  /home/a/.pi  ', container_path: '/root/.pi', mode: 'ro' },
            { host_path: '   ', container_path: '/none', mode: 'rw' },
        ])).toEqual([{ host_path: '/home/a/.pi', container_path: '/root/.pi', mode: 'ro' }]);
    });

    it('compares two lists whatever order the server wrote the fields in', () => {
        const saved = agentMountGrant({
            config: { sandbox_mount: [{ mode: 'ro', container_path: '/b', host_path: '/host-b' }, { container_path: '/a', host_path: '/host-a', mode: 'rw' }] },
        });
        const typed = [
            { host_path: '/host-a', container_path: '/a', mode: 'rw', suggestion: '' },
            { host_path: '/host-b', container_path: '/b', mode: 'ro', suggestion: '' },
        ];
        expect(grantFingerprint(agentMountUpdate(typed))).toBe(grantFingerprint(saved));
        typed[0].mode = 'ro';
        expect(grantFingerprint(agentMountUpdate(typed))).not.toBe(grantFingerprint(saved));
    });
});

describe('the dependency directory draft', () => {
    const DECLARED = { dependency_cache: { container_path: '/go/pkg/mod', env: 'GOMODCACHE' } };

    it('suggests the declared container path under the server home', () => {
        expect(dependencyDraft({ sandbox_config: DECLARED }, '/home/tester')).toBe('/home/tester/go/pkg/mod');
    });

    it('suggests nothing before the home directory is known', () => {
        expect(dependencyDraft({ sandbox_config: DECLARED }, '')).toBe('');
        expect(dependencyDraft({ sandbox_config: {} }, '/home/tester')).toBe('');
    });

    it('opens with the granted directory wherever the suggestion would have pointed', () => {
        const ext = { sandbox_config: { ...DECLARED, dependency_host_path: '/data/shared/gomodcache' } };
        expect(dependencyDraft(ext, '/home/tester')).toBe('/data/shared/gomodcache');
    });
});
