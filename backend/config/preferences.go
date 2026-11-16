package config

import (
	"encoding/json/v2"
	"strconv"

	"mike/backend/types"
)

// configPref is one row of the preferences table — the table at the bottom of this file. Its concrete
// shape is the Go shape of the field it carries, a flagPref holding a *bool, so the shape is never
// stated twice.
type configPref interface {
	// rowKey is the config-table row and the JSON field name, which are one string by contract.
	rowKey() string
	// decode applies one stored row to the config. A row this build cannot read is coerced, never
	// refused: an older build or a hand-edited database wrote it, and a config read has no answer for
	// that except the one its shape gives. modePref holds the only shape that reads a value back as
	// something other than unanswered.
	decode(raw string, cfg *types.Config)
	// encode renders the row to store. ok=false means the field holds nothing to store, which is not
	// an empty row: an unanswered preference stays absent, so the API omits the key and the client's
	// own default applies.
	encode(cfg *types.Config) (text string, ok bool, err error)
	// merge applies one PUT /api/config field to the config, keeping the current value when the
	// payload never sent the key. A value of the wrong type is recorded on the updates, which is what
	// answers it with a 400 naming the field.
	merge(u *configUpdates, cfg *types.Config)
}

// parsePtr parses a string to a *T using parse. Returns a nil pointer if the string is empty, which is
// how an unanswered row reads: absent, not zero.
func parsePtr[T any](s string, parse func(string) (T, error)) (*T, error) {
	if s == "" {
		return nil, nil
	}
	v, err := parse(s)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// parseBoolPtr parses a string to a *bool. Returns nil pointer if the string is empty.
func parseBoolPtr(s string) (*bool, error) {
	return parsePtr(s, strconv.ParseBool)
}

// parseIntPtr parses a string to a *int. Returns nil pointer if the string is empty.
func parseIntPtr(s string) (*int, error) {
	return parsePtr(s, strconv.Atoi)
}

// decodePtr stores one row of a preference whose field is a pointer. A row that will not parse leaves the
// field unanswered, the same as a row that was never written.
func decodePtr[T any](raw string, cfg *types.Config, parse func(string) (*T, error), set func(*types.Config, *T)) {
	if v, err := parse(raw); err == nil && v != nil {
		set(cfg, v)
	}
}

// encodePtr renders one row of a preference whose field is a pointer. A nil field has no answer to store,
// so ok is false: the key stays out of the response and the client uses its own default. format runs only
// when the field is not nil.
func encodePtr[T any](v *T, format func(T) string) (string, bool, error) {
	if v == nil {
		return "", false, nil
	}
	return format(*v), true, nil
}

// stringPref is a row that stores a plain string. An absent row reads as "", so the empty string is
// both "never answered" and "cleared" — which is how the worker and utility provider/model pairs work.
type stringPref struct {
	key string
	get func(*types.Config) string
	set func(*types.Config, string)
}

func (p stringPref) rowKey() string { return p.key }

func (p stringPref) decode(raw string, cfg *types.Config) { p.set(cfg, raw) }

func (p stringPref) encode(cfg *types.Config) (string, bool, error) {
	return p.get(cfg), true, nil
}

func (p stringPref) merge(u *configUpdates, cfg *types.Config) {
	p.set(cfg, u.strOr(p.key, p.get(cfg)))
}

// flagPref is a *bool row. Unset is nil and not false: the response omits the key and the browser
// applies its own default, while an explicit false is an answer someone gave and comes back present.
type flagPref struct {
	key string
	get func(*types.Config) *bool
	set func(*types.Config, *bool)
}

func (p flagPref) rowKey() string { return p.key }

func (p flagPref) decode(raw string, cfg *types.Config) {
	decodePtr(raw, cfg, parseBoolPtr, p.set)
}

func (p flagPref) encode(cfg *types.Config) (string, bool, error) {
	return encodePtr(p.get(cfg), strconv.FormatBool)
}

func (p flagPref) merge(u *configUpdates, cfg *types.Config) {
	p.set(cfg, u.ptrOr(p.key, p.get(cfg)))
}

// intPref is a *int row, unanswered in the same way a flagPref is.
type intPref struct {
	key string
	get func(*types.Config) *int
	set func(*types.Config, *int)
}

func (p intPref) rowKey() string { return p.key }

func (p intPref) decode(raw string, cfg *types.Config) {
	decodePtr(raw, cfg, parseIntPtr, p.set)
}

func (p intPref) encode(cfg *types.Config) (string, bool, error) {
	return encodePtr(p.get(cfg), strconv.Itoa)
}

func (p intPref) merge(u *configUpdates, cfg *types.Config) {
	p.set(cfg, u.ptrOr(p.key, p.get(cfg)))
}

// modePref is the default operating mode, which the wire and the row both carry as a string.
type modePref struct {
	key string
	get func(*types.Config) *types.OperatingMode
	set func(*types.Config, *types.OperatingMode)
}

func (p modePref) rowKey() string { return p.key }

func (p modePref) decode(raw string, cfg *types.Config) {
	if raw == "" {
		return
	}
	// A stored value is coerced rather than rejected: reading a row written by an older build must not
	// fail the read, and the only thing that can be done with a mode nothing dispatches on is fall
	// back. Writing one is a different story — see configUpdates.modeOr, which refuses it.
	mode, valid := types.ParseOperatingMode(raw)
	if !valid {
		mode = types.ModeAct
	}
	p.set(cfg, &mode)
}

func (p modePref) encode(cfg *types.Config) (string, bool, error) {
	return encodePtr(p.get(cfg), func(m types.OperatingMode) string { return string(m) })
}

func (p modePref) merge(u *configUpdates, cfg *types.Config) {
	p.set(cfg, u.modeOr(p.key, p.get(cfg)))
}

// shortcutsPref is the one row with a shape of its own. The binding set is replaced wholesale rather
// than merged field by field, so it decodes straight into types.KeyboardShortcuts.
type shortcutsPref struct {
	key string
	get func(*types.Config) *types.KeyboardShortcuts
	set func(*types.Config, *types.KeyboardShortcuts)
}

func (p shortcutsPref) rowKey() string { return p.key }

func (p shortcutsPref) decode(raw string, cfg *types.Config) {
	if raw == "" {
		return
	}
	ks := &types.KeyboardShortcuts{}
	if err := json.Unmarshal([]byte(raw), ks); err != nil {
		return
	}
	p.set(cfg, ks)
}

func (p shortcutsPref) encode(cfg *types.Config) (string, bool, error) {
	ks := p.get(cfg)
	if ks == nil {
		return "", false, nil
	}
	text, err := json.Marshal(ks)
	if err != nil {
		return "", false, err
	}
	return string(text), true, nil
}

func (p shortcutsPref) merge(u *configUpdates, cfg *types.Config) {
	var ks types.KeyboardShortcuts
	if u.field(p.key, &ks) {
		p.set(cfg, &ks)
	}
}

// The table. Each entry is the only place its key is written, and the three ways its value crosses are
// the three paths a preference takes: configRowKeys lists the rows a full read fetches, GetConfig()
// decodes them, UpdateConfig() writes them, and HandleConfig() merges a PUT over them. A preference has
// one entry to add.
var (
	prefWorkerProvider = stringPref{
		key: "worker_provider",
		get: func(c *types.Config) string { return c.WorkerProvider },
		set: func(c *types.Config, v string) { c.WorkerProvider = v },
	}
	prefWorkerModel = stringPref{
		key: "worker_model",
		get: func(c *types.Config) string { return c.WorkerModel },
		set: func(c *types.Config, v string) { c.WorkerModel = v },
	}
	prefUtilityProvider = stringPref{
		key: "utility_provider",
		get: func(c *types.Config) string { return c.UtilityProvider },
		set: func(c *types.Config, v string) { c.UtilityProvider = v },
	}
	prefUtilityModel = stringPref{
		key: "utility_model",
		get: func(c *types.Config) string { return c.UtilityModel },
		set: func(c *types.Config, v string) { c.UtilityModel = v },
	}
	prefDefaultAutoCommit = flagPref{
		key: "default_auto_commit",
		get: func(c *types.Config) *bool { return c.DefaultAutoCommit },
		set: func(c *types.Config, v *bool) { c.DefaultAutoCommit = v },
	}
	prefDefaultAutoMerge = flagPref{
		key: "default_auto_merge",
		get: func(c *types.Config) *bool { return c.DefaultAutoMerge },
		set: func(c *types.Config, v *bool) { c.DefaultAutoMerge = v },
	}
	prefDefaultSubtasksAutoMerge = flagPref{
		key: "default_subtasks_auto_merge",
		get: func(c *types.Config) *bool { return c.DefaultSubtasksAutoMerge },
		set: func(c *types.Config, v *bool) { c.DefaultSubtasksAutoMerge = v },
	}
	prefDefaultOperatingMode = modePref{
		key: "default_operating_mode",
		get: func(c *types.Config) *types.OperatingMode { return c.DefaultOperatingMode },
		set: func(c *types.Config, v *types.OperatingMode) { c.DefaultOperatingMode = v },
	}
	prefDefaultMaxFailedTurns = intPref{
		key: "default_max_failed_turns",
		get: func(c *types.Config) *int { return c.DefaultMaxFailedTurns },
		set: func(c *types.Config, v *int) { c.DefaultMaxFailedTurns = v },
	}
	prefDefaultAutoSummarizeThreshold = intPref{
		key: "default_auto_summarize_threshold",
		get: func(c *types.Config) *int { return c.DefaultAutoSummarizeThreshold },
		set: func(c *types.Config, v *int) { c.DefaultAutoSummarizeThreshold = v },
	}
	prefHideModelBadge = flagPref{
		key: "hide_model_badge",
		get: func(c *types.Config) *bool { return c.HideModelBadge },
		set: func(c *types.Config, v *bool) { c.HideModelBadge = v },
	}
	prefUpdateCheckEnabled = flagPref{
		key: "update_check_enabled",
		get: func(c *types.Config) *bool { return c.UpdateCheckEnabled },
		set: func(c *types.Config, v *bool) { c.UpdateCheckEnabled = v },
	}
	prefRunSiblingsParallel = flagPref{
		key: "run_siblings_parallel",
		get: func(c *types.Config) *bool { return c.RunSiblingsParallel },
		set: func(c *types.Config, v *bool) { c.RunSiblingsParallel = v },
	}
	prefSortOldestFirst = flagPref{
		key: "sort_oldest_first",
		get: func(c *types.Config) *bool { return c.SortOldestFirst },
		set: func(c *types.Config, v *bool) { c.SortOldestFirst = v },
	}
	prefAutoMaximizeChatMobile = flagPref{
		key: "auto_maximize_chat_mobile",
		get: func(c *types.Config) *bool { return c.AutoMaximizeChatMobile },
		set: func(c *types.Config, v *bool) { c.AutoMaximizeChatMobile = v },
	}
	prefReclaimIdleSandbox = flagPref{
		key: "reclaim_idle_sandbox",
		get: func(c *types.Config) *bool { return c.ReclaimIdleSandbox },
		set: func(c *types.Config, v *bool) { c.ReclaimIdleSandbox = v },
	}
	prefLoopBreaker = flagPref{
		key: "loop_breaker",
		get: func(c *types.Config) *bool { return c.LoopBreaker },
		set: func(c *types.Config, v *bool) { c.LoopBreaker = v },
	}
	prefShortcuts = shortcutsPref{
		key: "keyboard_shortcuts",
		get: func(c *types.Config) *types.KeyboardShortcuts { return c.KeyboardShortcuts },
		set: func(c *types.Config, v *types.KeyboardShortcuts) { c.KeyboardShortcuts = v },
	}

	// configPrefs is the whole table, in the order a full read asks for its rows. The write ladder and
	// the PUT merge follow that order; every entry moves a different field, so the order decides only
	// which row a save touches first. TestPreferenceTableCoversEveryPreference fails when a preference
	// joins types.Config and is left out here.
	configPrefs = []configPref{
		prefWorkerProvider, prefWorkerModel, prefUtilityProvider, prefUtilityModel,
		prefDefaultAutoCommit, prefDefaultAutoMerge, prefDefaultSubtasksAutoMerge,
		prefDefaultOperatingMode, prefDefaultMaxFailedTurns, prefDefaultAutoSummarizeThreshold,
		prefHideModelBadge, prefUpdateCheckEnabled, prefRunSiblingsParallel, prefSortOldestFirst,
		prefAutoMaximizeChatMobile, prefReclaimIdleSandbox, prefLoopBreaker, prefShortcuts,
	}

	// configRowKeys are the rows a full config read wants, in one query. Derived from the table, so the
	// read list cannot disagree with the write list.
	configRowKeys = rowKeys(configPrefs)
)

func rowKeys(prefs []configPref) []string {
	keys := make([]string, 0, len(prefs))
	for _, p := range prefs {
		keys = append(keys, p.rowKey())
	}
	return keys
}

// storedFlag reads one flag row straight from the table. A preference read that needs one value and
// not the whole view takes its key from a table entry rather than a literal, so a row rename cannot
// leave it behind. No row, an unreadable row and an unanswered row all read as off.
func (cs *ConfigStore) storedFlag(p flagPref) bool {
	raw, _ := cs.db.GetConfigValue(p.key)
	v, err := parseBoolPtr(raw)
	return err == nil && v != nil && *v
}

// storedInt reads one int row. ok=false covers both the absent row and the row that is not a number,
// and what to answer with then is the caller's, not this reader's.
func (cs *ConfigStore) storedInt(p intPref) (int, bool) {
	raw, err := cs.db.GetConfigValue(p.key)
	if err != nil {
		return 0, false
	}
	v, err := parseIntPtr(raw)
	if err != nil || v == nil {
		return 0, false
	}
	return *v, true
}
