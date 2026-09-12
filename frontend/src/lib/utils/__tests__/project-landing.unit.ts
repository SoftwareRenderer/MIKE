import { describe, it, expect } from 'vitest';
import { resolveLandingProject } from '../project-landing';

const projects = [{ id: 'p-a' }, { id: 'p-b' }, { id: 'p-c' }];

describe('resolveLandingProject', () => {
    it('lands on the project the user opened last', () => {
        expect(resolveLandingProject(projects, 'p-b')).toBe(projects[1]);
        expect(resolveLandingProject(projects, 'p-c')).toBe(projects[2]);
    });

    it('falls back to the first project when nothing was stored', () => {
        expect(resolveLandingProject(projects, '')).toBe(projects[0]);
    });

    it('ignores a stored id that is not in the list', () => {
        expect(resolveLandingProject(projects, 'deleted-project')).toBe(projects[0]);
    });

    it('answers null when there are no projects', () => {
        expect(resolveLandingProject([], '')).toBeNull();
        expect(resolveLandingProject([], 'p-a')).toBeNull();
    });
});
