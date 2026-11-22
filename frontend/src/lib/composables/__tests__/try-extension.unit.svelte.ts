import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useTryExtension } from '../use-try-extension.svelte';

const { reload, setMount } = vi.hoisted(() => ({ reload: vi.fn(), setMount: vi.fn() }));

vi.mock('$lib/api', () => ({ reloadExtensionVersion: reload, setAgentMount: setMount }));

function consentAnswer(over: Record<string, any> = {}) {
    return {
        status: 'consent' as const,
        consent: {
            consent_required: true as const,
            entry: {
                name: 'my-ext',
                display_name: 'My Ext',
                version: '0.1.0',
                description: 'd',
                author: 'op',
                type: 'ui' as const,
                capabilities: { network: { allowed_domains: ['api.example.com'] } },
            },
            agent_mounts: [] as { host_path: string; container_path: string; mode: string }[],
            fingerprint: 'fp-1',
            reasons: ['network access to api.example.com'],
            ...over,
        },
    };
}

const loaded = { status: 'loaded' as const, extension: { id: 'dev1' } };

const reloadTab = vi.fn();

beforeEach(() => {
    reload.mockReset();
    setMount.mockReset();
    // A load answers by taking the tab down, so `reload` is asserted, not executed.
    Object.defineProperty(window, 'location', {
        value: { reload: reloadTab },
        configurable: true,
        writable: true,
    });
    reloadTab.mockClear();
});

describe('pressing Try Extension', () => {
    it('loads the version and takes the tab down to show it', async () => {
        reload.mockResolvedValue(loaded);
        const tried = useTryExtension(() => 'dev1', () => 'task1');
        await tried.press();
        expect(reload).toHaveBeenCalledWith('dev1', 'task1', undefined);
        expect(tried.consent).toBeNull();
        expect(tried.notice).toContain('reloading');
        expect(tried.busy).toBe(false);
        expect(reloadTab).toHaveBeenCalledTimes(1);
    });

    it('holds the version as a question the server asked, not one it invented', async () => {
        reload.mockResolvedValue(consentAnswer());
        const tried = useTryExtension(() => 'dev1', () => 'task1');
        await tried.press();
        expect(reload).toHaveBeenCalledTimes(1);
        expect(tried.consent?.fingerprint).toBe('fp-1');
        expect(tried.notice).toBe('');
        expect(reloadTab).not.toHaveBeenCalled();
    });

    it('posts nothing when the question is closed', async () => {
        reload.mockResolvedValue(consentAnswer());
        const tried = useTryExtension(() => 'dev1', () => 'task1');
        await tried.press();
        tried.dismiss();
        expect(tried.consent).toBeNull();
        expect(reload).toHaveBeenCalledTimes(1);
        expect(reloadTab).not.toHaveBeenCalled();
    });

    it('echoes the fingerprint back on the acknowledged retry, and mounts nothing it was not told to', async () => {
        reload.mockResolvedValueOnce(consentAnswer()).mockResolvedValue(loaded);
        const tried = useTryExtension(() => 'dev1', () => 'task1');
        await tried.press();
        await tried.answerConsent(false);
        expect(setMount).not.toHaveBeenCalled();
        expect(reload).toHaveBeenLastCalledWith('dev1', 'task1', 'fp-1');
        expect(tried.consent).toBeNull();
        expect(tried.notice).toContain('reloading');
        expect(reloadTab).toHaveBeenCalledTimes(1);
    });

    it('applies the folders the person granted before retrying', async () => {
        const mounts = [{ host_path: '/home/op/.pi/agent', container_path: '/home/sandbox/.pi/agent', mode: 'ro' }];
        reload.mockResolvedValueOnce(consentAnswer({ agent_mounts: mounts })).mockResolvedValue(loaded);
        setMount.mockResolvedValue({ id: 'dev1' });
        const tried = useTryExtension(() => 'dev1', () => 'task1');
        await tried.press();
        await tried.answerConsent(true);
        expect(setMount).toHaveBeenCalledWith('dev1', mounts);
        expect(reload).toHaveBeenLastCalledWith('dev1', 'task1', 'fp-1');
    });

    it('leaves the folder failure as the sentence, and never loads over it', async () => {
        const mounts = [{ host_path: '/home/op/.ssh', container_path: '/home/sandbox/.ssh', mode: 'ro' }];
        reload.mockResolvedValueOnce(consentAnswer({ agent_mounts: mounts }));
        setMount.mockRejectedValue(new Error('/home/op/.ssh holds credentials, not one agent\u2019s own files'));
        const tried = useTryExtension(() => 'dev1', () => 'task1');
        await tried.press();
        await tried.answerConsent(true);
        expect(tried.notice).toContain('credentials');
        expect(reload).toHaveBeenCalledTimes(1);
        expect(reloadTab).not.toHaveBeenCalled();
    });

    it('surfaces a refusal that is a failure rather than a question', async () => {
        reload.mockRejectedValue(new Error('task1 has no worktree to load'));
        const tried = useTryExtension(() => 'dev1', () => 'task1');
        await tried.press();
        expect(tried.notice).toContain('no worktree');
        expect(tried.consent).toBeNull();
        expect(reloadTab).not.toHaveBeenCalled();
    });

    it('does nothing at all for a task whose row does not exist yet', async () => {
        const tried = useTryExtension(() => '', () => 'task1');
        await tried.press();
        expect(reload).not.toHaveBeenCalled();
        expect(reloadTab).not.toHaveBeenCalled();
    });
});
