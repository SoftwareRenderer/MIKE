/**
 * The project a URL without a `project` param lands on: the one the user opened last,
 * else the first. A stored id that is not in the list (deleted project, another browser
 * profile) is ignored rather than trusted, so nothing has to clean it up.
 */
export function resolveLandingProject<T extends { id: string }>(projects: T[], lastOpenedId: string): T | null {
    if (projects.length === 0) return null;
    return projects.find(p => p.id === lastOpenedId) ?? projects[0];
}
