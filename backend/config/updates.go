package config

import (
	"encoding/json"
	"fmt"

	"mike/backend/types"
)

// configUpdates handles PUT /api/config. Fields stay encoded so that "was this key sent"
// and "what type was sent" stay answerable, and type mismatches become a 400.
//
// A JSON null reads as absent, so a client sending "providers": null keeps the provider list.
type configUpdates struct {
	fields map[string]json.RawMessage
	// err is reported as a 400. Recording it rather than returning keeps the handler
	// a linear list of assignments.
	err error
}

func newConfigUpdates(body []byte) (*configUpdates, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	return &configUpdates{fields: fields}, nil
}

// field reports whether key was sent. Absent and null are both "not sent";
// anything else that will not decode is recorded as this request's error.
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

// note records the first bad field. The raw JSON error is not repeated back.
func (u *configUpdates) note(key string) {
	if u.err == nil {
		u.err = fmt.Errorf("invalid value for %s", key)
	}
}

func (u *configUpdates) strOr(key, cur string) string {
	var v string
	if u.field(key, &v) {
		return v
	}
	return cur
}

// boolPtrOr stays a pointer so a stored "no opinion" survives a save that never mentions the field.
func (u *configUpdates) boolPtrOr(key string, cur *bool) *bool {
	var v bool
	if u.field(key, &v) {
		return &v
	}
	return cur
}

func (u *configUpdates) intPtrOr(key string, cur *int) *int {
	var v int
	if u.field(key, &v) {
		return &v
	}
	return cur
}

// modeOr refuses unrecognised modes to prevent the UI echoing them back or tasks
// running in them. Reading one out of the database is still coerced (GetConfig).
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

// operatingModePtr ensures a bad mode is not stored, without changing the wire format.
func operatingModePtr(m *types.OperatingMode) *string {
	if m == nil {
		return nil
	}
	s := string(*m)
	return &s
}
