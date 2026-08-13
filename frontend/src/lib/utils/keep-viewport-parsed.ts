import { ViewPlugin, ViewUpdate, EditorView } from '@codemirror/view';
import { forceParsing, syntaxTreeAvailable, language } from '@codemirror/language';

/**
 * Prevent "washed out" scrolling. CodeMirror only parses ~100KB ahead of the
 * viewport and defers the rest to idle-time slices throttled while input is
 * pending, so jumps into unparsed regions render without syntax highlighting
 * until the background parse catches up. This parses the whole document
 * (files up to `fullParseLimit`, the server's 1MB read cap) in bounded rAF
 * slices from creation and on viewport/doc changes; larger files get a
 * bounded viewport-only force-parse. Work is deferred out of the view update
 * via rAF because dispatching while an update is in progress throws.
 */
export function keepViewportParsed(timeout = 16, fullParseLimit = 1_000_000) {
    return ViewPlugin.fromClass(
        class {
            raf: number | null = null;
            driving = false;
            driveIterations = 0;

            constructor(view: EditorView) {
                // Parse the whole file up front so jumps never land unparsed.
                this.maybeDriveFullParse(view);
            }

            update(update: ViewUpdate) {
                // Plain-text files have no syntax tree.
                if (!update.state.facet(language)) return;
                if (update.docChanged || update.viewportChanged) {
                    if (update.state.doc.length <= fullParseLimit) {
                        this.maybeDriveFullParse(update.view);
                    } else {
                        this.schedule(update.view, update.view.viewport.to);
                    }
                }
            }

            /** Drive a bounded background parse of the whole document. */
            maybeDriveFullParse(view: EditorView) {
                if (
                    this.driving ||
                    view.state.doc.length > fullParseLimit ||
                    !view.state.facet(language)
                ) {
                    return;
                }
                if (syntaxTreeAvailable(view.state, view.state.doc.length)) return;
                this.driving = true;
                this.driveIterations = 0;
                const drive = () => {
                    this.raf = null;
                    if (!view.dom.isConnected) {
                        this.driving = false;
                        return;
                    }
                    // Safety cap so a stuck parser can't chain forever.
                    if (this.driveIterations++ > 100) {
                        this.driving = false;
                        return;
                    }
                    const done = forceParsing(view, view.state.doc.length, timeout);
                    // done == whole doc parsed.
                    if (!done) {
                        this.raf = requestAnimationFrame(drive);
                    } else {
                        this.driving = false;
                    }
                };
                this.raf = requestAnimationFrame(drive);
            }

            /** Force-parse just the visible viewport (large files). */
            schedule(view: EditorView, upto: number) {
                if (this.raf != null || this.driving) return;
                this.raf = requestAnimationFrame(() => {
                    this.raf = null;
                    if (view.dom.isConnected) {
                        forceParsing(view, upto, timeout);
                    }
                });
            }

            destroy() {
                if (this.raf != null) cancelAnimationFrame(this.raf);
                this.raf = null;
            }
        },
    );
}
