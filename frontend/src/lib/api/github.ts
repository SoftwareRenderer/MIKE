import { apiRequest } from './helpers';

/**
 * Import content from a GitHub issue URL.
 * @param url The GitHub issue URL (e.g. https://github.com/owner/repo/issues/123)
 * @returns The formatted issue content (Title + Body)
 */
export async function importGitHubIssue(url: string): Promise<string> {
    const res = await apiRequest('POST', '/api/github/import-issue', { url });
    return res as string;
}
