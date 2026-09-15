import { describe, it, expect } from 'vitest';
import { fsEventConcernsFile, taskRelativeFsPath } from '$lib/utils/fs-event';

const PROJECT = 'bear-comet';
const TASK = 'lore-9qq1';
/** The same event as each watcher reports it: worktree-relative and project-relative. */
const WORKTREE_EVENT = { op: 'create' as const, path: 'src/lib/format.ts' };
const PROJECT_EVENT = { op: 'create' as const, path: `worktrees/${PROJECT}/${TASK}/src/lib/format.ts` };

describe('taskRelativeFsPath', () => {
  it('passes a worktree-relative path through', () => {
    expect(taskRelativeFsPath(WORKTREE_EVENT, PROJECT, TASK)).toBe('src/lib/format.ts');
  });

  it('strips the project-relative prefix of this task', () => {
    expect(taskRelativeFsPath(PROJECT_EVENT, PROJECT, TASK)).toBe('src/lib/format.ts');
  });

  it('refuses another task of the same project', () => {
    const other = { op: 'create' as const, path: `worktrees/${PROJECT}/other-task/src/lib/format.ts` };
    expect(taskRelativeFsPath(other, PROJECT, TASK)).toBeNull();
  });

  it('keeps a path outside the worktrees dir (a workspace dir elsewhere on disk)', () => {
    expect(taskRelativeFsPath({ op: 'delete', path: 'README.md' }, PROJECT, TASK)).toBe('README.md');
  });

  it('says nothing about an empty path', () => {
    expect(taskRelativeFsPath({ op: 'create', path: '' }, PROJECT, TASK)).toBeNull();
  });
});

describe('fsEventConcernsFile', () => {
  const host = `/home/projects/wt/${PROJECT}/${TASK}`;
  const mount = '/home/sandbox/kanban';

  it('matches the file under the explorer root, from either watcher', () => {
    const file = `${host}/src/lib/format.ts`;
    expect(fsEventConcernsFile(WORKTREE_EVENT, file, { projectId: PROJECT, taskId: TASK, rootPath: host })).toBe(true);
    expect(fsEventConcernsFile(PROJECT_EVENT, file, { projectId: PROJECT, taskId: TASK, rootPath: host })).toBe(true);
  });

  it('does not match a sibling file under the same root', () => {
    const file = `${host}/src/lib/other.ts`;
    expect(fsEventConcernsFile(WORKTREE_EVENT, file, { projectId: PROJECT, taskId: TASK, rootPath: host })).toBe(false);
  });

  it('does not match a file of another task', () => {
    const file = `${host}/src/lib/format.ts`;
    const otherTask = { op: 'create' as const, path: `worktrees/${PROJECT}/other-task/src/lib/format.ts` };
    expect(fsEventConcernsFile(otherTask, file, { projectId: PROJECT, taskId: TASK, rootPath: host })).toBe(false);
  });

  it('matches a sandbox-mounted file, whose host root never appears in its path', () => {
    expect(fsEventConcernsFile(WORKTREE_EVENT, `${mount}/src/lib/format.ts`, {
      projectId: PROJECT, taskId: TASK, rootPath: host,
    })).toBe(true);
  });

  it('matches without a root (the project explorer, and diff-mode editors)', () => {
    expect(fsEventConcernsFile({ op: 'create', path: 'notes/a.md' }, `${mount}/notes/a.md`)).toBe(true);
    expect(fsEventConcernsFile({ op: 'create', path: 'notes/a.md' }, `${mount}/other/a.md`)).toBe(false);
  });

  it('wakes rather than strands a file it cannot place', () => {
    expect(fsEventConcernsFile(WORKTREE_EVENT, '', { rootPath: host })).toBe(true);
    expect(fsEventConcernsFile({ op: 'create', path: '' }, `${host}/x`, { rootPath: host })).toBe(true);
  });

  it('wakes for a same-named file deeper down, which it cannot place without a root', () => {
    expect(fsEventConcernsFile({ op: 'create', path: 'format.ts' }, `${host}/src/lib/format.ts`, {
      projectId: PROJECT, taskId: TASK,
    })).toBe(true);
    // ...and does not once it knows the root the file is addressed by.
    expect(fsEventConcernsFile({ op: 'create', path: 'format.ts' }, `${host}/src/lib/format.ts`, {
      projectId: PROJECT, taskId: TASK, rootPath: host,
    })).toBe(false);
  });
});
