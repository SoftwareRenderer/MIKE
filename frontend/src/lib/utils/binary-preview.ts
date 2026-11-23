/**
 * Which extensions render which files. An extension declares the suffixes it renders
 * (`ui.file_suffixes`); the core names no format of its own, so a suffix nobody declared renders nothing.
 */
import { fileSuffix } from './file-suffix';
import type { InjectionContext } from '$lib/extensions/types';

/** The part of a registration this file cares about, kept structural so the injector can import this. */
export interface SuffixClaim {
    fileSuffixes: string[];
}

/** The suffix a mount is for, or '' when it names no file. `repoPath` wins: it is the spelling
 * `GET /api/fs/read?kind=bytes` answers, so both sides name a file the same way. */
export function fileSuffixOf(context?: InjectionContext | null): string {
    const path = String(context?.repoPath ?? context?.filePath ?? '');
    return fileSuffix(path);
}

/**
 * Whether a registration may mount for `suffix`. One that declared none never asked to be a file viewer,
 * so it mounts wherever its point does; one that declared some mounts only for a file it said it renders,
 * and only where the mount names a file.
 */
export function registrationClaims(reg: SuffixClaim, suffix: string): boolean {
    if (!reg.fileSuffixes.length || !suffix) return true;
    return reg.fileSuffixes.includes(suffix);
}

/** Whether anything mounted at this point can render `path` — the question behind a Preview button. */
export function previewableBinary(path: string, registrations: SuffixClaim[]): boolean {
    const suffix = fileSuffix(path);
    if (!suffix) return false;
    return registrations.some((reg) => reg.fileSuffixes.includes(suffix));
}
