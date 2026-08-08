# TODO

## Goal
Revise Settings → Providers so pricing, power consumption, and context size can be configured **per model**, with provider-level values acting as fallback defaults.

## Design decisions (confirmed)
1. Provider-level pricing/power sections remain as **fallback defaults**.
2. Empty per-model fields **inherit** the provider-level value (explicit `0` = free / no power, distinguishable from empty).
3. Re-fetching models **preserves** existing per-model pricing/power for models that still exist; context window is refreshed from the fetch.

## Tasks

### 1. Backend — data model
- [x] Add per-model override fields to `ModelConfig` in `backend/types/domain.go`: `input_price`, `output_price`, `cache_read_price`, `cache_write_price`, `power_consumption_w` as `*float64` (nil = inherit provider default), JSON `omitempty`

### 2. Backend — cost computation
- [x] Update `applyPricing` in `backend/managers/project.go`: resolve effective per-model pricing/power (model override when non-nil, else provider default); keep cache_write-overrides-input semantics per level; model power override feeds the kWh/electricity calc with provider electricity rate

### 3. Backend — unit tests
- [x] `TestApplyPricingModelPricingOverride` — model prices override provider prices
- [x] `TestApplyPricingModelPowerOverride` — model power overrides provider power; electricity cost uses provider rate
- [x] `TestApplyPricingModelPartialFallback` — some model fields set, others inherit provider defaults
- [x] `TestModelConfigJSONRoundTrip` — new `*float64` fields serialize/deserialize, nil omitted
- [x] Confirm existing pricing tests still pass unchanged (provider fallback path)

### 4. Frontend — types
- [x] Extend `ProviderModel` in `frontend/src/lib/types.ts` with optional per-model pricing + power override fields

### 5. Frontend — ConfigModal state & save
- [x] Extend `ModelEntry` + `buildProviders` + `addModelToProvider` in `ConfigModal.svelte` with new fields (null = inherit)
- [x] Update `fetchModelsForProvider` to merge: preserve existing per-model pricing/power for matching model names; refresh `context_window` from fetch
- [x] Update `handleSave` to serialize per-model pricing/power **and** `context_window` (currently dropped on save)

### 6. Frontend — ModelList UI
- [x] Add editable **context size** input per model row (pre-populated from Fetch Models, manual override allowed)
- [x] Add expandable per-model **Pricing & Power** row: input/output/cache read/cache write prices + power (W) override; empty shows "default" placeholder (inherits provider)
- [x] Accept `currencySymbol` prop for price prefix

### 7. Frontend — provider card & defaults labeling
- [x] Pass `currencySymbol` to `ModelList` in `ProviderCard.svelte`
- [x] Relabel `PricingSection.svelte` as provider "defaults" with help tooltip; keep existing input ids (`#power-w-N`, `#elec-cost-N`, …) so `settings-power.spec.ts` stays green
- [x] `cd frontend && npm run check` and `npm run build` pass

### 8. E2E tests (isolated project)
- [x] New `frontend/tests/e2e/settings-model-pricing.spec.ts`: render per-model fields, save & persist via API, fetch-models pre-populates context, empty field inherits provider default
- [x] Register new spec in `frontend/playwright.config.ts` (add to `isolated` testMatch + `chromium` exclusion)

### 9. Validation
- [x] `go test ./backend/... -count=1 -parallel 8` passes; `gofmt -w` on changed Go files
- [x] `cd frontend && npm test` passes
- [x] Targeted e2e: `npx playwright test settings-model-pricing settings-power settings-provider` passes

## Discovered Tasks

- [x] **Context window input organized in a column** (`ModelList.svelte`): restructured the model list into a grid (`Model | Context Window | Reasoning Effort | actions`) with a column-header row above the rows. The context window input now sits in its own column (fixed 120px, right-aligned, `ctx` prefix retained) aligned under a “Context Window” header instead of floating inline next to the model name. Headers use the same `grid-template-columns` as rows; 1px transparent border added to the header row so columns align pixel-perfect with the bordered rows. Verified via screenshot script (all ctx inputs + header share identical x=598/w=120) and `settings-model-pricing` e2e (5/5 pass).

- [x] **isolated project `workers: 2` → `1`** (`frontend/playwright.config.ts`): the comment says “serialized to avoid races” but 2 workers run files in parallel against ONE shared config server (port 3001), so each file’s `beforeEach` PUT races and clobbers the others’ config. Reproduced with the pre-existing pair (settings-power + settings-provider fail together without any new files). With 1 worker the isolated suites are deterministic; my new suite passes. Cost: slower isolated runs (~serial).
- [x] **Update existing `settings-provider.spec.ts` “should default to Medium for new models”**: model edits (name/reasoning) now correctly mark the form dirty (via `onDirty`), so clicking Cancel shows the “Discard changes?” dialog. The test now dismisses it.

## Notes
- Provider-level `electricity_cost_per_kwh` stays provider-wide (only power is overridable per model).
- Per-model pricing/power are `*float64` so "empty (inherit)" ≠ "0 (free)".
- `context_window` becomes editable and is persisted through the Settings save path (previously dropped).
