import type { PortForward } from '$lib/types';
import { t } from '$lib/i18n';

/** What the interface field shows when the port is published on every interface. */
export const DEFAULT_BIND = '0.0.0.0';

/**
 * The canonical `host_ip`: "" means every interface, and so do 0.0.0.0 and ::.
 * Null when the text is not an IP address.
 *
 * The server runs the same rules (types.NormalizeSandboxHostIP) and is the one that
 * decides; this only stops an obvious typo before it is saved.
 */
export function normalizeBind(text: string): string | null {
    const trimmed = text.trim();
    if (!trimmed) return '';
    if (trimmed.includes('.')) {
        const octets = trimmed.split('.');
        if (octets.length !== 4) return null;
        const bytes: number[] = [];
        for (const part of octets) {
            if (!/^\d{1,3}$/.test(part)) return null;
            const value = Number(part);
            if (value > 255) return null;
            bytes.push(value);
        }
        const canonical = bytes.join('.');
        return canonical === '0.0.0.0' ? '' : canonical;
    }
    if (trimmed === '::') return '';
    if (!/^[0-9a-fA-F:]+$/.test(trimmed) || !trimmed.includes(':')) return null;
    return trimmed.toLowerCase();
}

/** True when the port is published on every interface - the case not worth printing. */
export function isDefaultBind(hostIP?: string): boolean {
    return normalizeBind(hostIP ?? '') === '';
}

/** The address to show in the field: an empty binding is entered as 0.0.0.0. */
export function bindForInput(hostIP?: string): string {
    return hostIP && hostIP.length > 0 ? hostIP : DEFAULT_BIND;
}

/** Why a port row cannot be saved, or '' when it can. The port arrives as text or as the
 * number a number input yields. The reason is copy, so it comes from the catalog: the caller
 * puts it straight into the panel's error line. */
export function portRowError(containerText: string | number, bindText: string): string {
    const text = String(containerText).trim();
    const container = Number(text);
    if (!text || !Number.isInteger(container) || container < 1 || container > 65535) {
        return t('sandbox.portRangeError');
    }
    if (bindText.trim() && normalizeBind(bindText) === null) {
        return t('sandbox.hostInterfaceError');
    }
    return '';
}

/** One configured forward: "80" or "192.168.1.100:80". */
export function formatForwardPort(port: PortForward): string {
    return isDefaultBind(port.host_ip) ? `${port.container_port}` : `${port.host_ip}:${port.container_port}`;
}

/** "3000, 192.168.1.100:80" - the address only where one was chosen. */
export function formatForwardPorts(ports: PortForward[]): string {
    return ports.map(formatForwardPort).join(', ');
}

/** The host side of a live mapping, prefixed with the interface when it is not every one. */
export function formatAssignedHost(hostPort: number, hostIP?: string): string {
    return isDefaultBind(hostIP) ? `${hostPort}` : `${hostIP}:${hostPort}`;
}
