package config

import (
	"encoding/json"
	"fmt"
	"strconv"

	"mike/backend/types"
)

// configUpdates is the body of PUT /api/config: the fields still encoded, so both "was this key
// sent" and "what type did the client send" stay answerable, and a type mismatch becomes a 400
// naming the field. It used to be a `map[string]any` read behind assertions, where four fields
// panicked on a wrong type and the rest were dropped silently.
//
// A JSON null reads as absent, which is what kept "providers": null from wiping the provider list.
type configUpdates struct {
	fields map[string]json.RawMessage
	// err is the first field whose type did not match; the handler answers 400 with it. Recording it
	// rather than returning keeps the handler a linear list of assignments.
	err error
}

func newConfigUpdates(body []byte) (*configUpdates, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	return &configUpdates{fields: fields}, nil
}

// field decodes key into target and reports whether it was sent. Absent and null are both
// "not sent"; anything else that will not decode is recorded as this request's error.
func (u *configUpdates) field(key string, target any) bool {
	raw, ok := u.fields[key]
	if !ok || string(raw) == "null" {
		return false
	}
	if err := json.Unmarshal(raw, target); err != nil {
		u.note(key)
		return false
	}
	return true
}

// note records the first bad field. The message names the field, which is the useful part; the raw
// JSON error is not repeated back.
func (u *configUpdates) note(key string) {
	if u.err == nil {
		u.err = fmt.Errorf("invalid value for %s", key)
	}
}

// strOr returns the field's value, or cur when the field was absent or null.
func (u *configUpdates) strOr(key, cur string) string {
	var v string
	if u.field(key, &v) {
		return v
	}
	return cur
}

// boolPtrOr returns a pointer to the field's value, or cur when the field was absent or null. It
// stays a pointer so a stored "no opinion" survives a save that never mentions the field.
func (u *configUpdates) boolPtrOr(key string, cur *bool) *bool {
	var v bool
	if u.field(key, &v) {
		return &v
	}
	return cur
}

// intPtrOr returns a pointer to the field's value, or cur when absent or null.
func (u *configUpdates) intPtrOr(key string, cur *int) *int {
	var v int
	if u.field(key, &v) {
		return &v
	}
	return cur
}

// modeOr returns a pointer to the field's operating mode, or cur when absent or null.
//
// An unrecognised mode is refused rather than coerced: the UI would echo it back and no task would
// ever run in it. Reading one out of the database is still coerced (GetConfig).
func (u *configUpdates) modeOr(key string, cur *types.OperatingMode) *types.OperatingMode {
	var s string
	if !u.field(key, &s) {
		return cur
	}
	mode, valid := types.ParseOperatingMode(s)
	if !valid {
		u.note(key)
		return cur
	}
	return &mode
}

// operatingModePtr converts a mode to the plain string the API response carries. The Go type exists
// to stop a bad mode being stored, not to change the wire format.
func operatingModePtr(m *types.OperatingMode) *string {
	if m == nil {
		return nil
	}
	s := string(*m)
	return &s
}

// clamp constrains a client-supplied count to the range the reader accepts. Out of range is a range
// problem, not a type problem, so it stays a clamp: AutoSummarizeThreshold treats a stored value out
// of range as unset anyway, and clamping keeps what the UI shows equal to what is stored.
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// mergeProviderKeys preserves stored API keys for providers the payload did not carry one for.
// nil = not provided (preserve), "" = explicitly clear, non-empty = new value.
func mergeProviderKeys(incoming []types.Provider, existing []types.Provider) []types.Provider {
	existingByKey := make(map[string]string)
	for _, p := range existing {
		if p.APIKey != nil && p.ID != 0 {
			existingByKey[strconv.Itoa(p.ID)] = *p.APIKey
		}
	}
	for i := range incoming {
		if incoming[i].APIKey == nil && existingByKey[strconv.Itoa(incoming[i].ID)] != "" {
			key := existingByKey[strconv.Itoa(incoming[i].ID)]
			incoming[i].APIKey = &key
		}
	}
	return incoming
}
