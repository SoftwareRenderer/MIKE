package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"mike/backend/types"
)

// A model row's sampler overrides: a value nobody typed is stored as nil and never reaches the
// request body, and a typed value is sent exactly as stored. So every field is a pointer, and the
// only judgement here is whether a typed value is one a provider can accept.

// ErrInvalidConfig refuses a config whose own values are impossible. The handler answers it as a 400
// carrying the sentence, because whoever typed the value is the one who can fix it.
var ErrInvalidConfig = errors.New("invalid configuration")

// samplerBound is one field's accepted range. loOpen makes the lower bound exclusive where 0 means
// something else to the provider: top_p of 0 admits no token, repetition_penalty of 0 mutes output.
type samplerBound struct {
	name   string
	lo, hi float64
	loOpen bool
	pick   func(*types.ModelSampling) *float64
}

func (b samplerBound) String() string {
	if b.loOpen {
		return fmt.Sprintf("greater than %s and at most %s", trimNum(b.lo), trimNum(b.hi))
	}
	return fmt.Sprintf("between %s and %s", trimNum(b.lo), trimNum(b.hi))
}

// accepts is negated so a NaN, which compares false against every bound, is refused.
func (b samplerBound) accepts(v float64) bool {
	if b.loOpen {
		return v > b.lo && v <= b.hi
	}
	return v >= b.lo && v <= b.hi
}

// The accepted ranges, and the same numbers the Settings field hints print (settings.sampling.*Hint).
// A provider that takes a wider range still gets the narrower one.
var samplerFloatBounds = []samplerBound{
	{name: "temperature", lo: 0, hi: 2, pick: func(s *types.ModelSampling) *float64 { return s.Temperature }},
	{name: "top_p", lo: 0, hi: 1, loOpen: true, pick: func(s *types.ModelSampling) *float64 { return s.TopP }},
	{name: "min_p", lo: 0, hi: 1, pick: func(s *types.ModelSampling) *float64 { return s.MinP }},
	{name: "frequency_penalty", lo: -2, hi: 2, pick: func(s *types.ModelSampling) *float64 { return s.FrequencyPenalty }},
	{name: "presence_penalty", lo: -2, hi: 2, pick: func(s *types.ModelSampling) *float64 { return s.PresencePenalty }},
	{name: "repetition_penalty", lo: 0, hi: 2, loOpen: true, pick: func(s *types.ModelSampling) *float64 { return s.RepetitionPenalty }},
}

var samplerIntBounds = []struct {
	name string
	lo   int
	pick func(*types.ModelSampling) *int
}{
	{name: "top_k", lo: 0, pick: func(s *types.ModelSampling) *int { return s.TopK }},
	{name: "seed", lo: 0, pick: func(s *types.ModelSampling) *int { return s.Seed }},
}

// validateModelSampling refuses a sampler override outside its range, naming the provider, the model
// and the field — the provider list is one stored array, so "temperature" alone names no row. A field
// nobody typed is nil and is never a refusal.
func validateModelSampling(providers []types.Provider) error {
	for _, p := range providers {
		for _, m := range p.Models {
			if m.Sampling == nil {
				continue
			}
			for _, b := range samplerFloatBounds {
				if v := b.pick(m.Sampling); v != nil && !b.accepts(*v) {
					return fmt.Errorf("%w: %s: %s must be %s, got %s",
						ErrInvalidConfig, samplingRowLabel(p, m.Name), b.name, b.String(), trimNum(*v))
				}
			}
			for _, b := range samplerIntBounds {
				if v := b.pick(m.Sampling); v != nil && *v < b.lo {
					return fmt.Errorf("%w: %s: %s must be %d or more, got %d",
						ErrInvalidConfig, samplingRowLabel(p, m.Name), b.name, b.lo, *v)
				}
			}
		}
	}
	return nil
}

// The row a refusal points at. An LLM provider carries no name, so the URL identifies it.
func samplingRowLabel(p types.Provider, modelName string) string {
	owner := strings.TrimSpace(p.Name)
	if owner == "" {
		owner = p.URL
	}
	return fmt.Sprintf("provider %q model %q", owner, modelName)
}

// trimNum prints 2 as "2", so the refusal sentence reads "between 0 and 2".
func trimNum(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// GetModelSampling returns the overrides stored for a model, or nil for a model, provider or config
// the store does not know — the same answer as "send nothing".
func (cs *ConfigStore) GetModelSampling(modelName, providerURL string) *types.ModelSampling {
	m, ok := cs.providerModel(modelName, providerURL)
	if !ok {
		return nil
	}
	return m.Sampling
}
