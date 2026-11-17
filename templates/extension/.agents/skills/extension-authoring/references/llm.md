# A model provider (`llm`)

An `llm` extension teaches MIKE one provider's dialect. It ships a **mappings file** and nothing
else: MIKE sends OpenAI-shaped requests, and a mapping rewrites the request body and the headers for
one provider's base URL. No container, no image, no network grant — MIKE still makes the call; you
only translate it.

## What you owe

`capabilities.llm.mappings_file`, default `mappings.json`. The file is a JSON array of mappings; the
**first whose `match` prefix fits the configured base URL wins**, and everything after it is ignored
for that request.

```json
[
  {
    "match": "https://api.example.com",
    "headers": { "x-provider": "example" },
    "body": {
      "remove": ["temperature"],
      "remove_if": { "tool_choice": "tools" },
      "set": {
        "max_tokens": { "value": 8192 },
        "reasoning_effort": { "from": "reasoning_effort", "value": { "low": "fast", "*": "fast" } }
      }
    }
  }
]
```

- `match` is a base-URL prefix: `https://openrouter` does **not** match `https://openrouter.ai`.
- `body.remove` drops a path; `body.remove_if` drops a path only when the referenced path is absent.
- `body.set` writes a path. A rule is `{ "value": … }` — a scalar verbatim, a string as a template,
  or a map used as a lookup table with `"*"` as its fallback — optionally gated by `from` (the
  variable to look up: the model name or the reasoning effort) and `when` (the values it applies to).
  A gated rule whose variable cannot be resolved never applies.
- A header whose template does not resolve is dropped, not sent blank.

A mappings file that does not parse is **skipped with a warning**, so a broken one looks exactly like
a provider that went quiet. Keep one mapping per provider.

## How to tell it worked

Nothing in the interface says "the mapping loaded". Choose a model whose provider base URL your
`match` covers and send a message: the answer, or that provider's own error, is the evidence. Say in
your final message which model you tested with — an untested `match` prefix is the usual reason a
mapping "did nothing".
