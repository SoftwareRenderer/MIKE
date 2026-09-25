// One domain's plain HTTP calls: the offline queue replays these, and nothing here knows the queue exists.
import type { Config } from '../../types';
import { API_BASE, apiFetch, apiError } from '../helpers';
import { t } from '$lib/i18n';

export async function updateConfigRaw(config: Config): Promise<Config> {
    const res = await apiFetch(`${API_BASE}/config`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(config),
    });
    if (!res.ok) throw await apiError(res, t('api.updateConfig'));
    return res.json();
}
