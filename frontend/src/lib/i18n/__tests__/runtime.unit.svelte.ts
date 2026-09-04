import { describe, it, expect, beforeEach } from 'vitest';
import { t, locale, setCatalog, resetToEnglish } from '$lib/i18n';

describe('i18n runtime', () => {
    beforeEach(() => resetToEnglish());

    it('resolves English from the catalog of record', () => {
        expect(t('i18n.test.plain')).toBe('Plain message');
    });

    it('interpolates named placeholders and leaves unknown ones visible', () => {
        expect(t('i18n.test.files', { count: 3 })).toBe('3 files');
        expect(t('i18n.test.files', {})).toBe('{count} files');
    });

    it('selects plural forms through Intl.PluralRules, not count === 1', () => {
        expect(t('i18n.test.files', { count: 0 })).toBe('0 files');
        expect(t('i18n.test.files', { count: 1 })).toBe('1 file');
        expect(t('i18n.test.files', { count: 1.5 })).toBe('1.5 files');
    });

    it('falls back to English for anything a pack has not translated', () => {
        setCatalog('fr', {
            'i18n.test.files': { one: '{count} fichier', other: '{count} fichiers' },
        });
        expect(locale.code).toBe('fr');
        expect(t('i18n.test.files', { count: 1 })).toBe('1 fichier');
        expect(t('i18n.test.files', { count: 2 })).toBe('2 fichiers');
        expect(t('i18n.test.plain')).toBe('Plain message');
    });

    it('answers with the key when even English is missing it', () => {
        // @ts-expect-error the key deliberately does not exist
        expect(t('does.not.exist')).toBe('does.not.exist');
    });

    it('returns to English on resetToEnglish()', () => {
        setCatalog('fr', { 'i18n.test.plain': 'Message simple' });
        expect(t('i18n.test.plain')).toBe('Message simple');
        resetToEnglish();
        expect(locale.code).toBe('en');
        expect(t('i18n.test.plain')).toBe('Plain message');
    });
});
