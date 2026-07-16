/**
 * Utility functions for the @ file mention feature.
 * Detects @ triggers in textarea input, extracts search queries,
 * inserts @path references, and computes caret coordinates for dropdown positioning.
 */

/**
 * Result of detecting an @ mention trigger at the cursor position.
 */
interface MentionTrigger {
	/** The index in the textarea value where the @ appears */
	atIndex: number;
	/** The search query typed after the @ (may be empty) */
	query: string;
}

/**
 * Check if the cursor is inside or immediately after an @ mention trigger.
 * Returns the trigger info if found, or null if no @ is active at the cursor.
 *
 * Rules:
 * - The @ must be preceded by start-of-string, whitespace, or another @
 * - The cursor must be at or after the @, within the word following it
 * - The word after @ can contain alphanumeric, /, -, _, . characters
 */
export function detectMentionAtCursor(value: string, cursorPos: number): MentionTrigger | null {
	// Find the last @ before or at the cursor position
	let atIndex = -1;
	for (let i = Math.min(cursorPos, value.length - 1); i >= 0; i--) {
		if (value[i] === '@') {
			// Check that @ is at word boundary (preceded by start, whitespace, or another @)
			if (i === 0 || /\s/.test(value[i - 1]) || value[i - 1] === '@') {
				atIndex = i;
				break;
			}
		}
		// Stop if we hit whitespace before finding a valid @
		if (i > 0 && /\s/.test(value[i])) {
			break;
		}
	}

	if (atIndex === -1) return null;

	// Extract the query after @ — only valid path characters
	const afterAt = value.slice(atIndex + 1, cursorPos);
	const match = afterAt.match(/^([a-zA-Z0-9/_\-.\s]*)$/);
	if (!match) return null;

	// Trim trailing whitespace from query
	const query = match[1].trimEnd();
	// Cursor must be within the mention region (after @ and within/after the query)
	const mentionEnd = atIndex + 1 + query.length;
	if (cursorPos < atIndex || cursorPos > mentionEnd + 1) return null;

	return { atIndex, query };
}

/**
 * Insert @path at the given cursor position, replacing the current @ trigger.
 * Returns the new value and the new cursor position (end of inserted text).
 */
export function insertMention(value: string, atIndex: number, query: string, path: string): { value: string; cursorPos: number } {
	const before = value.slice(0, atIndex);
	// Remove the old query text after @
	const after = value.slice(atIndex + 1 + query.length);
	const newValue = before + '@' + path + after;
	const newCursorPos = atIndex + 1 + path.length;
	return { value: newValue, cursorPos: newCursorPos };
}

/**
 * Get the bounding rectangle of the input area as viewport coordinates.
 * Uses the provided container element (e.g., the styled input wrapper)
 * so the dropdown aligns with the visible input boundary, not the textarea content.
 */
export function getInputRect(container: HTMLElement): { top: number; bottom: number; left: number; width: number } | null {
	try {
		const rect = container.getBoundingClientRect();
		return { top: rect.top, bottom: rect.bottom, left: rect.left, width: rect.width };
	} catch {
		return null;
	}
}
