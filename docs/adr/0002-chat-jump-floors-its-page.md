# ADR-0002 — A chat jump floors its page instead of walking to the prompt

**Status:** accepted

**Rules that follow:** `docs/api-contract.md → The conversation is a window, not the thread`

## Abstract

A jump to an older prompt names where its page stops: `GET …/messages?before=<frontier>&from=<cursor>`
answers the rows between the two and no further, and the prompt read hands out that `cursor` beside every
prompt. The click used to walk the conversation five answers at a time until the row appeared. The walk is
still the shape of the read — and still the fallback when no prompt read named the row — so no row between
the two reads is ever skipped.

## Context

The chat's sticky header names the prompt above the rows on screen, and a window of answers almost never
holds it: `store.MessageWindowDefault` is five answers and one agent turn is routinely more than five, which
is exactly why the prompt read exists. So the header's prompt is nearly always unrendered, and one click had
to fetch before it could land. The old jump asked for the next-older window, looked for the row, and asked
again — with the route's default page size, because its caller passed no `limit` — waiting out
`settleOnRow`'s re-pin (up to `ANCHOR_SETTLE_FRAMES = 20` frames) after each page. Measured on one jump to
the prompt above the window a task opens on, over a session of 8 turns of 30 answers each, where each answer
carries a 6 KiB call and result: **6 reads, 59 rows, 423 KiB**. The same jump with a floor: **2 reads, 51
rows, 365 KiB**. Re-measure with `go test ./backend/server -run TestStickyJump -v`; its assertion is the row
set rather than the count, so a floor that skipped or repeated a row fails instead of merely getting faster.

## Decision

A floor is a second bound on the read that already travels: `conversationWhere` adds
`AND (created_at, rowid) >= (?, ?)` for it, using the same pair the `ORDER BY` compares, so it stops a page
at a position rather than at a count. The page keeps prepending, and `X-Messages-Before` names its oldest row
as it does for any page, so the rows below a floor stay one read away instead of being jumped — the
contract that every row is handed over exactly once survives a floor verbatim. A floor cannot travel without
`before`: alone it would bound a read that starts at the newest end, which is not a window anyone means, and
the route answers that with a 400 naming `from`. `GET …/turns` answers a `cursor` per prompt because a
prompt's position is already the thing its cursor encodes; a jump reads it and stops asking once its row is
on screen. A jump declines the per-page re-pin (`settle: false`) because it scrolls once, at the end; that
wait stays where it is needed, under scroll-to-top paging.

## Consequences

What a click costs is now the rows it will draw, in as few reads as the 25-answer ceiling allows: one read
for a prompt less than a ceiling-span away, and the walk below that unchanged in shape. A jump that lands
mid-conversation still hands over every row between the frontier and the floor, so the transcript never
shows a conversation with a hole in it. What this rules out: a blind seek that returns a window *around* a
row and leaves a gap — cheap, and a lie about the conversation, which is why `from` is a floor and not a
centre. Also declined, on measurement or on evidence: prefetching the named prompt's window while the header
shows it (`needsRows` is recomputed on every scroll event, so a prefetch loop reads rows no one clicks), and
caching older pages in IndexedDB, which the client deliberately does not do — an older page cached alone is
what a reload would open on. Reopen it if a jump over a span far wider than the ceiling becomes common: the
remaining cost is one read per 25 answers, and the fix is a range read bounded in bytes rather than in
answers. Note too that the ceiling is a byte ceiling in disguise — 25 answers of a tool-heavy turn is
hundreds of KiB — so raising it is not free and was not tried.
