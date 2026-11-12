import { t } from '$lib/i18n';

const MINUTE_SECONDS = 60;

export function formatQueueWait(seconds: number): string {
    if (seconds > MINUTE_SECONDS) {
        return t('chat.queue.waitMinutes', { count: Math.round(seconds / MINUTE_SECONDS) });
    }
    return t('chat.queue.waitSeconds', { count: seconds });
}
