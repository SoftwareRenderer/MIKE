package config

import (
	"encoding/json"
	"fmt"

	"mike/backend/types"
)

// configUpdates is the body of PUT /api/config: the fields still encoded, so both "was this key
// sent" and "what type did the client send" stay answerable, and a type mismatch becomes a 400
// naming the field.
//
// A JSON null reads as absent, so a client sending "providers": null keeps the provider list.
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
