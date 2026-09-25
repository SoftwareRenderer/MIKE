/**
 * The DOM measurements a real layout engine answers and jsdom does not — without them the renderer
 * throws before it draws anything. A helper, not a suite: nothing here runs on its own.
 */

/** Width per character, so a longer label is laid out wider. */
const CHAR_WIDTH = 8;
const LINE_HEIGHT = 18;

function boxFor(text: string) {
    return { width: Math.max(text.length * CHAR_WIDTH, 20), height: LINE_HEIGHT };
}

/** Installs the stubs; the tests assert on the diagram, not on these. */
export function stubSvgMeasurement(): void {
    const proto = Element.prototype as Element & {
        getBBox?: () => unknown;
        getComputedTextLength?: () => number;
    };
    proto.getBBox = function (this: Element) {
        const box = boxFor(this.textContent ?? '');
        return { x: 0, y: 0, ...box };
    };
    proto.getComputedTextLength = function (this: Element) {
        return boxFor(this.textContent ?? '').width;
    };
    Element.prototype.getBoundingClientRect = function (this: Element) {
        const box = boxFor(this.textContent ?? '');
        return {
            x: 0, y: 0, top: 0, left: 0, right: box.width, bottom: box.height,
            width: box.width, height: box.height, toJSON: () => ({}),
        } as DOMRect;
    };
}
