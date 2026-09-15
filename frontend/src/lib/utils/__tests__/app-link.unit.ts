import { describe, it, expect } from 'vitest';
import { projectTerminalHref, taskTerminalHref, asksForTerminal } from '../app-link';

const path = location.pathname;

describe('terminal deep links', () => {
    it('names the project then the view, as updateUrl orders them', () => {
        expect(projectTerminalHref('p1')).toBe(`${path}?project=p1&view=terminal`);
    });

    it('puts the task between project and view', () => {
        expect(taskTerminalHref('p1', 't2')).toBe(`${path}?project=p1&task=t2&view=terminal`);
    });

    it('encodes ids so they cannot add params of their own', () => {
        expect(projectTerminalHref('a&x=1')).toBe(`${path}?project=a%26x%3D1&view=terminal`);
    });

    it('reads the terminal screen back out of a query', () => {
        expect(asksForTerminal('?project=p1&view=terminal')).toBe(true);
        expect(asksForTerminal('?project=p1&task=t2&view=terminal')).toBe(true);
    });

    it('is not the terminal screen for any other view or no view', () => {
        expect(asksForTerminal('?project=p1')).toBe(false);
        expect(asksForTerminal('?project=p1&view=explorer')).toBe(false);
        expect(asksForTerminal('?project=p1&view=task-terminal')).toBe(false);
        expect(asksForTerminal('')).toBe(false);
    });
});
