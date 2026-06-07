# TODO

## Goal
Move concurrency configuration from a global `endpoint_limits` map into a per-provider `concurrency` field (default: 1).

## Tasks

### 1. Backend — types and config
- [x] Add `Concurrency int` field to `Provider` struct in `backend/types.go`
- [x] Remove `EndpointLimits map[string]int` field from `Config` struct in `backend/types.go`
- [x] Remove all `endpoint_limits` handling from `backend/config.go` (GetConfig, UpdateConfig, HandleConfig partial update, HandleTestOverride)
- [x] Remove `endpoint_limits` from default config seed in `backend/store.go`
- [x] Remove `endpoint_limits` assertion from `backend/store_test.go`

### 2. Frontend — types and UI
- [x] Update `frontend/src/lib/types.ts`: add `concurrency: number` to `Provider`, remove `endpoint_limits` from `Config`
- [x] Update `ConfigModal.svelte`: add concurrency input inside each provider card, remove the separate "Endpoint Concurrency Limits" section, set default concurrency to 1 when adding a new provider

### 3. Build and verify
- [x] Build backend (`go build -o kanban .`)
- [x] Build frontend (`cd frontend && npm run build`)
- [x] Restart server and verify config endpoint returns updated schema
- [x] Run e2e tests (`cd frontend && npx playwright test`) — Provider Settings: 5/5 pass, 4 pre-existing UI failures unrelated to this change

## Notes
- Default concurrency per provider: 1
- Fully remove `endpoint_limits` — no backward compatibility layer
- Concurrency is not actively enforced in code yet; this task only moves the configuration setting
