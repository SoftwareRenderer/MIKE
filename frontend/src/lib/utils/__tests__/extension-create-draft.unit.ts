import { describe, expect, it } from 'vitest';
import { validateExtensionCreateDraft } from '../extension-create-draft';

const submittable = (name: string, description: string) =>
    Object.keys(validateExtensionCreateDraft({ name, description })).length === 0;

describe('extension create draft', () => {
    it('refuses a click that could only have been a refusal', () => {
        expect(validateExtensionCreateDraft({ name: '  ', description: 'does a thing' }))
            .toEqual({ name: 'settings.extensions.createNameRequired' });
        expect(validateExtensionCreateDraft({ name: 'Clock', description: '   ' }))
            .toEqual({ description: 'settings.extensions.createDescriptionRequired' });
        expect(submittable('', '')).toBe(false);
    });

    it('refuses a name with nothing a directory can be called from it', () => {
        expect(validateExtensionCreateDraft({ name: '??? ---', description: 'does a thing' }))
            .toEqual({ name: 'settings.extensions.createNameUnusable' });
    });

    it("lets a name of any script through, because the slugifier is the server's", () => {
        expect(submittable('サイドバー時計', 'shows time')).toBe(true);
        expect(submittable('Уголок отзывов', 'shows time')).toBe(true);
    });

    it('submits the two answers a person owes', () => {
        expect(submittable('Sidebar clock', 'Show how long each task has been in Review.')).toBe(true);
    });
});
