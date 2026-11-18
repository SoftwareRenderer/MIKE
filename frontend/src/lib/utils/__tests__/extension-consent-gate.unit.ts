import { describe, expect, it } from 'vitest';
import { requiresInstallReview } from '../extension-permissions';

/**
 * The two ends of one rule: this predicate decides whether the install dialog opens, and
 * consentRequired() in backend/extensions/consent.go decides whether a development load is refused
 * (docs/extension-authoring.md → Loading). The fixtures are the same list as `consentFixtures` in
 * backend/extensions/consent_test.go, spelled the same way — change them here, change them there.
 */
const CONSENT_FIXTURES: { name: string; capabilities: Record<string, any>; want: boolean }[] = [
    { name: 'a plain card', capabilities: { ui: { injection_points: ['taskCard'], permissions: ['ui.toast'] } }, want: false },
    { name: 'a stylesheet', capabilities: { css: { css_url: 'theme.css' } }, want: false },
    { name: 'server-side egress', capabilities: { network: { allowed_domains: ['api.example.com'] } }, want: true },
    { name: 'egress with no domain named', capabilities: { network: {} }, want: true },
    { name: 'a proxy call from the frame', capabilities: { ui: { permissions: ['editor.getContent', 'network.proxy'] } }, want: true },
    { name: 'a page served with network', capabilities: { ui: { permissions: ['preview.serveNetwork'] } }, want: true },
    { name: 'a page served with scripts', capabilities: { ui: { permissions: ['preview.serveScripts'] } }, want: true },
    {
        name: 'a declared dependency directory',
        capabilities: { lsp: { sandbox: { dependency_cache: { container_path: '/go/pkg/mod', env: 'GOMODCACHE' } } } },
        want: true,
    },
    {
        name: 'a suggested agent folder',
        capabilities: { agent: { sandbox: { mount: [{ host_path: '~/.pi/agent', container_path: '/home/sandbox/.pi/agent', mode: 'ro' }] } } },
        want: true,
    },
    { name: 'an agent folder with nothing in it', capabilities: { agent: { sandbox: { mount: [] } } }, want: false },
    { name: 'nothing at all', capabilities: {}, want: false },
];

describe('the consent rule the install dialog and the development load share', () => {
    for (const fixture of CONSENT_FIXTURES) {
        it(`${fixture.name} → ${fixture.want ? 'asks' : 'loads'}`, () => {
            expect(requiresInstallReview({ capabilities: fixture.capabilities })).toBe(fixture.want);
        });
    }
});
