import { describe, it, expect } from 'vitest';
import {
    DEFAULT_BIND,
    normalizeBind,
    isDefaultBind,
    bindForInput,
    portRowError,
    formatForwardPort,
    formatForwardPorts,
    formatAssignedHost,
} from '$lib/utils/sandbox-ports';

// Same cases as backend/types/validate_test.go: the field means the same thing on both
// sides, or a saved value reads differently in Settings than it publishes.
describe('normalizeBind', () => {
    it.each([
        ['', ''],
        ['   ', ''],
        ['0.0.0.0', ''],
        ['::', ''],
        ['192.168.1.100', '192.168.1.100'],
        [' 10.0.0.5 ', '10.0.0.5'],
        ['127.0.0.1', '127.0.0.1'],
        ['::1', '::1'],
        ['FD00::5', 'fd00::5'],
    ])('%j -> %j', (input, want) => {
        expect(normalizeBind(input)).toBe(want);
    });

    it.each(['localhost', 'saturn.lan', '192.168.1', '192.168.1.100:8080', '256.0.0.1', '10.0.0.0/24', 'fe80::1%enp1s0', 'not-an-ip'])(
        'rejects %j',
        input => {
            expect(normalizeBind(input)).toBeNull();
        },
    );
});

describe('isDefaultBind', () => {
    it.each([undefined, '', '0.0.0.0', '::'])('%j is the default', hostIP => {
        expect(isDefaultBind(hostIP)).toBe(true);
    });
    it.each(['192.168.1.100', '127.0.0.1', '::1'])('%j is not', hostIP => {
        expect(isDefaultBind(hostIP)).toBe(false);
    });
});

describe('bindForInput', () => {
    it('shows the wildcard spelling for an empty binding', () => {
        expect(bindForInput(undefined)).toBe(DEFAULT_BIND);
        expect(bindForInput('')).toBe(DEFAULT_BIND);
        expect(bindForInput('192.168.1.100')).toBe('192.168.1.100');
    });
});

describe('portRowError', () => {
    it('accepts a container port with no interface', () => {
        expect(portRowError('3000', '')).toBe('');
    });
    it('accepts an interface', () => {
        expect(portRowError('3000', '192.168.1.100')).toBe('');
    });
    it('accepts the number a number input yields', () => {
        expect(portRowError(3000, '')).toBe('');
        expect(portRowError(0, '')).toContain('Container port');
    });

    it.each(['', '0', '65536', '-1', 'abc'])('rejects container port %j', container => {
        expect(portRowError(container, '')).toContain('Container port');
    });
    it('rejects an interface that is not an IP address', () => {
        expect(portRowError('3000', 'saturn.lan')).toContain('IP address');
    });
});

describe('formatForwardPort', () => {
    it('shows the interface only when one was chosen', () => {
        expect(formatForwardPort({ container_port: 3000 })).toBe('3000');
        expect(formatForwardPort({ container_port: 80, host_ip: '192.168.1.100' })).toBe('192.168.1.100:80');
    });
});

describe('formatForwardPorts', () => {
    it('prints the interface only where one was chosen', () => {
        expect(formatForwardPorts([{ container_port: 3000 }, { container_port: 80, host_ip: '192.168.1.100' }])).toBe(
            '3000, 192.168.1.100:80',
        );
    });
    it('prints bare ports for the default', () => {
        expect(formatForwardPorts([{ container_port: 3000, host_ip: '0.0.0.0' }])).toBe('3000');
    });
});

describe('formatAssignedHost', () => {
    it('prefixes a bound interface', () => {
        expect(formatAssignedHost(42585, '192.168.1.100')).toBe('192.168.1.100:42585');
        expect(formatAssignedHost(42585, '')).toBe('42585');
        expect(formatAssignedHost(42585)).toBe('42585');
    });
});
