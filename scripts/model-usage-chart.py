#!/usr/bin/env python3
"""Generate an SVG pie chart of models credited in `Assisted-by:` commit trailers.

Reads the full git history of the current repository, normalizes the many
spellings the trailer has accumulated (agent prefixes like `MIKE:`/`pi:`, vendor
prefixes like `deepseek-ai/`, quantization suffixes like `-Q4_K_XL`), and writes
a self-contained SVG (no scripts, no external fonts) with a donut chart plus a
legend of raw commit counts.

Usage:
    python3 scripts/model-usage-chart.py [-o docs/model-usage.svg] [--repo PATH] [--json]

Counting unit: a *commit*. A commit that credits two different models (a handoff)
counts once for each; a commit that names the same model twice counts once.
"""

import argparse
import json
import math
import os
import re
import subprocess
import sys
from collections import Counter, defaultdict

# Unit separator / record separator sentinels for `git log --format`.
RECORD_SEP = "\x01"
FIELD_SEP = "\x02"

TRAILER_RE = re.compile(r"(?im)^[ \t]*assisted-by[ \t]*:[ \t]*(.+?)[ \t]*$")
# Two trailers once landed on one line separated by a literal backslash-n.
LITERAL_NEWLINE_RE = re.compile(r"\\n|\n")
TRAILER_LABEL_RE = re.compile(r"^\s*assisted-by\s*:\s*", re.I)
# `MIKE:` / `pi:` name the *agent*, not the model.
AGENT_PREFIX_RE = re.compile(r"^(mike|pi|claude[- ]code|codex)\s*[:@]\s*", re.I)
AGENT_SUFFIX_RE = re.compile(r"\s+via\s+(pi|mike|claude[- ]code|codex)\s*$", re.I)
# `deepseek-ai/`, `deepseek/` etc. name the publisher, not the model.
VENDOR_PREFIX_RE = re.compile(r"^[A-Za-z0-9._-]+/")
# `-Q4_K_XL` is a build of the same model, not a different model.
QUANT_SUFFIX_RE = re.compile(r"[-_ ]q\d+[a-z0-9_]*$", re.I)

PALETTE = [
    "#4E79A7", "#F28E2B", "#E15759", "#76B7B2", "#59A14F",
    "#EDC948", "#B07AA1", "#FF9DA7", "#9C755F", "#BAB0AC",
    "#86BCB6", "#D37295", "#A0CBE8", "#FFBE7D", "#8CD17D",
]

# Donut geometry, shared by the renderer and the verifier.
CX, CY, R_OUTER, R_INNER = 300, 340, 175, 96


def git_log(repo):
    """Yield (hash, epoch, parents, subject, body) for every reachable commit."""
    fmt = RECORD_SEP + "%H" + FIELD_SEP + "%ct" + FIELD_SEP + "%P" + FIELD_SEP + "%s" + FIELD_SEP + "%b"
    out = subprocess.run(
        ["git", "-C", repo, "log", "--format=" + fmt],
        check=True, capture_output=True,
    ).stdout.decode("utf-8", "replace")
    for record in out.split(RECORD_SEP):
        if not record.strip():
            continue
        fields = record.split(FIELD_SEP)
        while len(fields) < 5:
            fields.append("")
        yield fields[0], int(fields[1] or 0), fields[2], fields[3], fields[4]


def raw_trailers(body):
    """Every `Assisted-by:` value in a commit body, in order."""
    values = []
    for match in TRAILER_RE.findall(body):
        for piece in LITERAL_NEWLINE_RE.split(match):
            # A second trailer once landed on the same line, so each piece can
            # still carry its own `Assisted-by:` label.
            piece = TRAILER_LABEL_RE.sub("", piece).strip().strip(",;")
            if piece:
                values.append(piece)
    return values


def split_agent(value):
    """Return (agent_or_None, model_text)."""
    agent = None
    m = AGENT_PREFIX_RE.match(value)
    if m:
        agent = m.group(1).lower()
        value = value[m.end():]
    m = AGENT_SUFFIX_RE.search(value)
    if m:
        agent = agent or m.group(1).lower()
        value = value[: m.start()]
    return agent, value.strip()


def canonical_key(model):
    """Identity of a model, ignoring publisher, case, punctuation and quantization."""
    text = VENDOR_PREFIX_RE.sub("", model.strip())
    text = QUANT_SUFFIX_RE.sub("", text)
    text = re.sub(r"[\s_\-./]+", "", text)
    return text.lower()


def collect(repo):
    """Aggregate history into (model_counts, agent_counts, meta)."""
    # canonical key -> Counter of raw spellings, to display the commonest one.
    spellings = defaultdict(Counter)
    model_commits = Counter()
    model_trailers = Counter()
    agent_commits = Counter()
    commits_with = 0
    commits_total = 0
    merge_count = 0
    merge_with = 0
    times = []

    for _sha, epoch, parents, _subject, body in git_log(repo):
        commits_total += 1
        is_merge = " " in parents.strip()
        merge_count += 1 if is_merge else 0
        seen = set()
        seen_agents = set()
        for value in raw_trailers(body):
            agent, model = split_agent(value)
            if not model:
                continue
            key = canonical_key(model)
            if not key:
                continue
            spellings[key][value.strip()] += 1
            model_trailers[key] += 1
            if key not in seen:
                seen.add(key)
                model_commits[key] += 1
            # Same unit as the models: a commit counts once per distinct agent.
            seen_agents.add(agent or "unattributed")
        agent_commits.update(seen_agents)
        if seen:
            commits_with += 1
            merge_with += 1 if is_merge else 0
            times.append(epoch)

    # Display the most common spelling, with the agent/publisher prefix removed
    # so the legend names the model and not the harness that ran it.
    display = {}
    for key, counts in spellings.items():
        clean = [s for s in counts if not AGENT_PREFIX_RE.match(s) and "/" not in s]
        best = max(clean or counts, key=lambda s: counts[s])
        display[key] = VENDOR_PREFIX_RE.sub("", split_agent(best)[1]).strip()

    ordered = [k for k, _ in model_commits.most_common()]
    models = [
        {
            "key": k,
            "name": display[k],
            "commits": model_commits[k],
            "trailers": model_trailers[k],
            "variants": sorted(spellings[k]),
        }
        for k in ordered
    ]
    meta = {
        "commits_total": commits_total,
        "commits_with_trailer": commits_with,
        "merges_total": merge_count,
        "merges_with_trailer": merge_with,
        "trailers_total": sum(model_trailers.values()),
        "first_epoch": min(times) if times else 0,
        "last_epoch": max(times) if times else 0,
    }
    agents = [{"agent": a, "count": c} for a, c in agent_commits.most_common()]
    return models, agents, meta


# ---------------------------------------------------------------- SVG helpers

def esc(text):
    return (
        str(text).replace("&", "&amp;").replace("<", "&lt;")
        .replace(">", "&gt;").replace('"', "&quot;")
    )


def polar(cx, cy, r, angle):
    return cx + r * math.cos(angle), cy + r * math.sin(angle)


def donut_path(cx, cy, r_outer, r_inner, a0, a1):
    """Filled donut slice from angle a0 to a1 (radians, clockwise, 0 == 3 o'clock)."""
    if (a1 - a0) >= math.radians(359.999):
        # One model owns the whole circle. An arc whose endpoints coincide is not
        # drawn at all (SVG drops it), so the ring is built as two half-arcs each
        # way and marked so the verifier can tell it from a wedge.
        t_ox, t_oy = polar(cx, cy, r_outer, a0)
        b_ox, b_oy = polar(cx, cy, r_outer, a0 + math.pi)
        t_ix, t_iy = polar(cx, cy, r_inner, a0)
        b_ix, b_iy = polar(cx, cy, r_inner, a0 + math.pi)
        return (
            f"M {t_ox:.2f} {t_oy:.2f} "
            f"A {r_outer} {r_outer} 0 0 1 {b_ox:.2f} {b_oy:.2f} "
            f"A {r_outer} {r_outer} 0 0 1 {t_ox:.2f} {t_oy:.2f} Z "
            f"M {t_ix:.2f} {t_iy:.2f} "
            f"A {r_inner} {r_inner} 0 0 0 {b_ix:.2f} {b_iy:.2f} "
            f"A {r_inner} {r_inner} 0 0 0 {t_ix:.2f} {t_iy:.2f} Z"
        )
    large = 1 if (a1 - a0) > math.pi else 0
    x0, y0 = polar(cx, cy, r_outer, a0)
    x1, y1 = polar(cx, cy, r_outer, a1)
    xi0, yi0 = polar(cx, cy, r_inner, a0)
    xi1, yi1 = polar(cx, cy, r_inner, a1)
    return (
        f"M {x0:.2f} {y0:.2f} "
        f"A {r_outer} {r_outer} 0 {large} 1 {x1:.2f} {y1:.2f} "
        f"L {xi1:.2f} {yi1:.2f} "
        f"A {r_inner} {r_inner} 0 {large} 0 {xi0:.2f} {yi0:.2f} Z"
    )


def empty_chart(meta):
    """A repository with no `Assisted-by:` trailers gets a notice, not an empty ring.

    A hole with nothing in it renders as a blank page, and the totals would have
    to be invented to divide by — this prints the count instead.
    """
    width, height = 1000, 320
    return (
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" '
        f'viewBox="0 0 {width} {height}" role="img"><title>'
        "Models credited in Assisted-by commit trailers</title>"
        f'<desc>No Assisted-by trailers in {meta["commits_total"]} commits.</desc>'
        f'<rect width="{width}" height="{height}" fill="#ffffff"/>'
        "<style>text{font-family:'Inter','Helvetica Neue',Arial,sans-serif}"
        ".h{font-size:22px;font-weight:600}.sub{font-size:13px;fill:#666}</style>"
        '<text class="h" x="40" y="46">Which models did the work?</text>'
        '<text class="sub" x="40" y="150">No <tspan font-family="ui-monospace,monospace">Assisted-by:</tspan> '
        f'trailer in any of the {meta["commits_total"]:,} commits of this repository.</text>'
        '<text class="sub" x="40" y="172">Nothing to chart.</text></svg>\n'
    )


def wrap(text, width):
    """Greedy word wrap. Only used for the footer, whose width is re-measured by
    rendering the SVG (see scripts/model-usage-chart.py header note)."""
    lines, line = [], ""
    for word in text.split():
        if line and len(line) + 1 + len(word) > width:
            lines.append(line)
            line = word
        else:
            line = f"{line} {word}".strip()
    if line:
        lines.append(line)
    return lines


def group_series(models, total, min_share=0.01):
    """Wedges to draw: models at or above `min_share`, the rest as one "Other".

    A pie with a 53% slice leaves a 0.1% model under two pixels, so the tail
    shares one wedge — measured, not guessed: at R_OUTER a 0.2% wedge is 2px of
    arc and sampled entirely as background. Nothing is hidden: the legend lists
    every grouped model with its own count.
    """
    if total <= 0:
        return [], []
    main = [m for m in models if m["commits"] / total >= min_share]
    tail = [m for m in models if m["commits"] / total < min_share]
    series = list(main)
    if tail:
        series.append(
            {
                "name": f"Other ({len(tail)} models)",
                "commits": sum(m["commits"] for m in tail),
                "members": tail,
            }
        )
    return series, tail


def svg_chart(models, agents, meta):
    total = sum(m["commits"] for m in models)
    series, tail = group_series(models, total)
    if not series:
        return empty_chart(meta), []
    width = 1000
    cx, cy = CX, CY
    r_outer, r_inner = R_OUTER, R_INNER

    # Legend and footer decide the canvas height, so a repository with more
    # models (or a longer provenance note) grows instead of overflowing.
    legend_x, legend_y, row_h, sub_h = 600, 132, 24, 17
    legend_bottom = legend_y + len(series) * row_h + (len(tail) * sub_h if tail else 0) + (10 if tail else 0)
    agent_txt = ", ".join(f"{esc(a['agent'])} {a['count']:,}" for a in agents)
    footer = [
        f"Counting unit is a commit: a commit crediting two models counts once for each; a model named twice in "
        f"one commit counts once. {meta['trailers_total']:,} trailers over {meta['commits_with_trailer']:,} "
        f"trailer-bearing commits.",
        "Spellings normalized: agent prefixes (MIKE:, pi:), publisher prefixes (deepseek-ai/, deepseek/) and "
        "quantization suffixes (-Q4_K_XL) collapse onto one model.",
    ]
    footer.append(
        f"Commits naming each agent: {agent_txt}. "
        f"Source: git log of this repository — {meta['commits_total']:,} commits, {meta['merges_total']:,} merges. "
        f"Regenerate: python3 scripts/model-usage-chart.py"
    )
    lines = []
    for para in footer:
        lines.extend(wrap(para, 140))
    height = max(cy + r_outer + 34, legend_bottom + 26) + len(lines) * 15 + 18

    parts = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" '
        f'viewBox="0 0 {width} {height}" role="img" '
        f'aria-labelledby="title desc">',
        '<title id="title">Models credited in Assisted-by commit trailers</title>',
        f'<desc id="desc">Donut chart. {meta["commits_with_trailer"]} of '
        f'{meta["commits_total"]} commits carry an Assisted-by trailer, naming '
        f'{len(models)} distinct models across {meta["trailers_total"]} trailers.'
        "</desc>",
        f'<rect width="{width}" height="{height}" fill="#ffffff"/>',
        '<style>'
        "text{font-family:'Inter','Helvetica Neue',Arial,sans-serif;fill:#1a1a1a}"
        ".h{font-size:22px;font-weight:600}.sub{font-size:13px;fill:#666}"
        # Label colors must be CSS, not fill="" attributes: a presentation
        # attribute loses to the `text{}` rule above, so white-on-wedge labels
        # would silently render near-black (measured in a browser).
        '.pct-in{font-size:13px;font-weight:600;fill:#ffffff}'
        ".pct-out{font-size:13px;font-weight:600;fill:#555}"
        ".leg{font-size:13px}.legb{font-size:13px;font-weight:600}"
        ".foot{font-size:11px;fill:#777}.ctr{font-size:22px;font-weight:600}"
        "</style>",
        f'<text class="h" x="40" y="46">Which models did the work?</text>',
        f'<text class="sub" x="40" y="68">By model referenced in an <tspan font-family="ui-monospace,monospace">Assisted-by:</tspan> '
        f'commit trailer</text>',
    ]

    # Slices start at 12 o'clock and run clockwise.
    angle = -90.0
    gap = 0.0
    outside = []  # leader-line labels, placed together so they cannot overlap
    for i, m in enumerate(series):
        sweep = m["commits"] / total * 360.0
        a0 = (angle + gap / 2) * 3.141592653589793 / 180.0
        a1 = (angle + sweep - gap / 2) * 3.141592653589793 / 180.0
        color = PALETTE[i % len(PALETTE)]
        d = donut_path(cx, cy, r_outer, r_inner, a0, a1)
        full = ' data-full="1"' if (a1 - a0) >= math.radians(359.999) else ""
        parts.append(
            f'<path d="{d}"{full} '
            f'fill="{color}" stroke="#ffffff" stroke-width="2">'
            f"<title>{esc(m['name'])}: {m['commits']} commits "
            f"({m['commits'] / total * 100:.1f}%)</title></path>"
        )
        mid = (a0 + a1) / 2
        frac = m["commits"] / total
        if frac >= 0.045:
            lx, ly = polar(cx, cy, (r_outer + r_inner) / 2, mid)
            parts.append(
                f'<text class="pct-in" x="{lx:.1f}" y="{ly + 4:.1f}" '
                f'text-anchor="middle">{frac * 100:.0f}%</text>'
            )
        elif frac >= 0.012:
            outside.append({"mid": mid, "frac": frac, "color": color})
        angle += sweep

    # Leader-line labels are placed together, not per wedge: two thin neighbouring
    # wedges put their labels at nearly the same height, and they overlapped by
    # 6x13px before this pass. Each side is pushed apart to a minimum gap.
    label_r = r_outer + 26
    min_gap = 15.0
    for item in outside:
        side = 1 if math.cos(item["mid"]) >= 0 else -1
        sx, sy = polar(cx, cy, r_outer + 2, item["mid"])
        item["side"] = side
        item["anchor"] = (sx, sy)
        item["y"] = polar(cx, cy, label_r, item["mid"])[1]
    for side in (-1, 1):
        group = sorted((o for o in outside if o["side"] == side), key=lambda o: o["y"])
        for k in range(1, len(group)):
            if group[k]["y"] - group[k - 1]["y"] < min_gap:
                group[k]["y"] = group[k - 1]["y"] + min_gap
        # Keep the column on the canvas if the push ran past the bottom.
        for k in range(len(group) - 2, -1, -1):
            if group[k + 1]["y"] - group[k]["y"] < min_gap:
                group[k]["y"] = group[k + 1]["y"] - min_gap
    for item in outside:
        side = item["side"]
        tx = cx + side * (label_r + 6)
        sx, sy = item["anchor"]
        elbow = cx + side * (label_r - 8)
        parts.append(
            f'<polyline points="{sx:.1f},{sy:.1f} {elbow:.1f},{item["y"]:.1f} '
            f'{tx - side * 4:.1f},{item["y"]:.1f}" fill="none" stroke="{item["color"]}" '
            f'stroke-width="1.5"/>'
            f'<text class="pct-out" x="{tx:.1f}" y="{item["y"] + 4:.1f}" '
            f'text-anchor="{"start" if side > 0 else "end"}">'
            f'{item["frac"] * 100:.1f}%</text>'
        )

    parts.append(
        f'<text class="ctr" x="{cx}" y="{cy - 4}" text-anchor="middle">{total:,}</text>'
        f'<text class="sub" x="{cx}" y="{cy + 16}" text-anchor="middle">model-credits</text>'
    )

    # Legend
    lx0, ly0 = legend_x, legend_y
    parts.append(f'<text class="legb" x="{lx0}" y="{ly0 - 24}">Model</text>')
    parts.append(f'<text class="legb" x="{lx0 + 260}" y="{ly0 - 24}" text-anchor="end">commits</text>')
    parts.append(f'<text class="legb" x="{lx0 + 340}" y="{ly0 - 24}" text-anchor="end">share</text>')
    parts.append(f'<line x1="{lx0}" y1="{ly0 - 16}" x2="{lx0 + 340}" y2="{ly0 - 16}" stroke="#ddd"/>')
    y = ly0
    for i, m in enumerate(series):
        frac = m["commits"] / total * 100
        parts.append(
            f'<rect x="{lx0}" y="{y - 11}" width="12" height="12" rx="2" '
            f'fill="{PALETTE[i % len(PALETTE)]}"/>'
            f'<text class="leg" x="{lx0 + 22}" y="{y}">{esc(m["name"])}</text>'
            f'<text class="leg" x="{lx0 + 260}" y="{y}" text-anchor="end">{m["commits"]:,}</text>'
            f'<text class="leg" x="{lx0 + 340}" y="{y}" text-anchor="end">{frac:.1f}%</text>'
        )
        y += row_h
        # The grouped wedge keeps every member visible with its own number.
        for member in m.get("members", []):
            parts.append(
                f'<text class="sub" x="{lx0 + 22}" y="{y}" font-size="12">'
                f'{esc(member["name"])}</text>'
                f'<text class="sub" x="{lx0 + 260}" y="{y}" text-anchor="end" font-size="12">'
                f'{member["commits"]:,}</text>'
                f'<text class="sub" x="{lx0 + 340}" y="{y}" text-anchor="end" font-size="12">'
                f'{member["commits"] / total * 100:.1f}%</text>'
            )
            y += sub_h

    for i, line in enumerate(lines):
        parts.append(
            f'<text class="foot" x="40" y="{height - 6 - (len(lines) - 1 - i) * 15}">{line}</text>'
        )
    parts.append("</svg>")
    return "\n".join(parts) + "\n", series


ARC_RE = re.compile(
    r"M\s+(-?[\d.]+)\s+(-?[\d.]+)\s+A\s+([\d.]+)\s+[\d.]+\s+0\s+([01])\s+1\s+(-?[\d.]+)\s+(-?[\d.]+)"
)
PATH_RE = re.compile(r"<path d=\"([^\"]+)\"([^>]*)>")


def verify_geometry(svg_text, models):
    """Check the rendered arcs against the counts they are supposed to draw.

    Returns a list of problems; empty means the wedges are contiguous, start at
    12 o'clock, sweep 360 degrees in total, and each one's angle matches its
    share of the commits.
    """
    problems = []
    if not models:
        # Nothing to draw: the chart is a written notice, not a ring.
        return [] if "<path " not in svg_text else ["a chart was drawn with no data behind it"]
    paths = PATH_RE.findall(svg_text)
    if len(paths) != len(models):
        problems.append(f"{len(paths)} wedges drawn for {len(models)} models")
        return problems

    def angle_of(x, y):
        return math.degrees(math.atan2(float(y) - CY, float(x) - CX))

    total = sum(m["commits"] for m in models) or 1
    running = 0.0
    for i, ((d, attrs), model) in enumerate(zip(paths, models)):
        expected = model["commits"] / total * 360.0
        if 'data-full="1"' in attrs:
            # A ring, not a wedge: both radii must be present and it must be the
            # only thing drawn, or the angles below mean nothing.
            if f"A {R_OUTER} {R_OUTER}" not in d or f"A {R_INNER} {R_INNER}" not in d:
                problems.append(f"wedge {i} ({model['name']}) full ring has the wrong radii")
            if expected < 359.999:
                problems.append(
                    f"wedge {i} ({model['name']}) is a full ring at {expected:.1f}deg"
                )
            running += expected
            continue
        arcs = ARC_RE.findall(d)
        if len(arcs) != 1:
            problems.append(f"wedge {i} ({model['name']}) has {len(arcs)} outer arcs, expected 1")
            continue
        sx, sy, _r, large_flag, ex, ey = arcs[0]
        if abs(float(_r) - R_OUTER) > 0.01:
            problems.append(f"wedge {i} outer radius {_r} != {R_OUTER}")
        start = (angle_of(sx, sy) + 90.0) % 360.0
        end = (angle_of(ex, ey) + 90.0) % 360.0
        sweep = (end - start) % 360.0
        if expected > 359.999:
            problems.append(
                f"wedge {i} ({model['name']}) owns 100% but is drawn as a wedge, not a "
                "ring — coincident arc endpoints render as nothing"
            )
            running += expected
            continue
        # The endpoints only fix the arc once the large-arc flag says which of the
        # two arcs between them is drawn; ignoring it would let a wedge draw its
        # reflex complement and still measure correctly here. With the radius
        # pinned to R_OUTER and sweep-flag 1, the flag is determined by the sweep.
        if (large_flag == "1") != (sweep > 180):
            problems.append(
                f"wedge {i} ({model['name']}) has large-arc flag {large_flag} "
                f"for a {sweep:.3f}deg sweep"
            )
        if abs(sweep - expected) > 0.05:
            problems.append(
                f"wedge {i} ({model['name']}) sweeps {sweep:.3f}deg, expected {expected:.3f}deg"
            )
        if abs(start - (running % 360.0)) > 0.05:
            problems.append(
                f"wedge {i} ({model['name']}) starts at {start:.3f}deg, "
                f"previous wedge ended at {running % 360.0:.3f}deg (gap or overlap)"
            )
        running += expected
    if abs(running - 360.0) > 0.05:
        problems.append(f"expected sweeps sum to {running:.3f}deg, not 360")
    first_path = paths[0][0]
    if 'data-full="1"' not in paths[0][1]:
        first = (angle_of(ARC_RE.findall(first_path)[0][0], ARC_RE.findall(first_path)[0][1]) + 90.0) % 360.0
        if first > 0.05:
            problems.append(f"first wedge starts at {first:.3f}deg past 12 o'clock")
    return problems


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--repo", default=".", help="git repository to read (default: cwd)")
    ap.add_argument("-o", "--output", default="docs/model-usage.svg")
    ap.add_argument("--json", action="store_true", help="print the aggregate table as JSON and exit")
    ap.add_argument("--check", action="store_true",
                    help="verify the committed SVG is current and geometrically sound; change nothing")
    args = ap.parse_args()

    repo = os.path.abspath(args.repo)
    if subprocess.call(["git", "-C", repo, "rev-parse", "--git-dir"],
                       stdout=subprocess.DEVNULL) != 0:
        sys.exit(f"not a git repository: {repo}")

    models, agents, meta = collect(repo)
    if args.json:
        json.dump({"meta": meta, "agents": agents, "models": models}, sys.stdout, indent=2)
        sys.stdout.write("\n")
        return
    svg, series = svg_chart(models, agents, meta)
    out = os.path.abspath(args.output)

    if args.check:
        problems = []
        try:
            with open(out, encoding="utf-8") as fh:
                on_disk = fh.read()
        except OSError as exc:
            problems.append(f"cannot read {out}: {exc} — write it first with "
                            "python3 scripts/model-usage-chart.py")
            on_disk = None
        if on_disk is not None and on_disk != svg:
            problems.append(f"{os.path.relpath(out, os.getcwd())} is stale — regenerate with "
                            "python3 scripts/model-usage-chart.py")
        if on_disk is not None:
            problems.extend(verify_geometry(on_disk, series))
        if problems:
            for p in problems:
                print(f"FAIL: {p}", file=sys.stderr)
            sys.exit(1)
        total = sum(m["commits"] for m in models)
        print(f"OK: {os.path.relpath(out, os.getcwd())} is current — "
              f"{len(models)} models in {len(series)} wedges, {total:,} model-credits, geometry verified")
        return

    parent = os.path.dirname(out)
    if parent:
        os.makedirs(parent, exist_ok=True)
    with open(out, "w", encoding="utf-8") as fh:
        fh.write(svg)

    total = sum(m["commits"] for m in models)
    print(f"{meta['commits_with_trailer']:,}/{meta['commits_total']:,} commits carry a trailer; "
          f"{meta['trailers_total']:,} trailers\n")
    if not models:
        print("no Assisted-by: trailer found — wrote a notice, not a chart, to "
              f"{os.path.relpath(out, os.getcwd())}")
        return
    print(f"{'MODEL':<34}{'COMMITS':>8}{'TRAILERS':>10}{'SHARE':>8}")
    for m in models:
        print(f"{m['name'][:33]:<34}{m['commits']:>8}{m['trailers']:>10}{m['commits'] / total * 100:>7.1f}%")
    print(f"\n{'total model-credits':<34}{total:>8}")
    print("commits naming each agent: " + ", ".join(f"{a['agent']}={a['count']}" for a in agents))
    grouped = [m for m in models if m["commits"] / total < 0.01]
    if grouped:
        print(f"chart: {len(models)} models drawn as {len(series)} wedges — "
              f"{', '.join(m['name'] for m in grouped)} share one 'Other' wedge "
              f"(each under 1%; sub-pixel at this radius) and are listed in the legend.")
    print(f"\nwrote {os.path.relpath(out, os.getcwd())}")


if __name__ == "__main__":
    main()
