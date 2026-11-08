// Package model holds the shared usage data model that every collector writes
// and the store persists.
package model

import (
	"errors"
	"fmt"
	"time"
)

// Source identifies where a usage record came from.
type Source string

// Known sources.
const (
	SourceClaudeCode   Source = "claude_code"
	SourceCodexCLI     Source = "codex_cli"
	SourceGeminiCLI    Source = "gemini_cli"
	SourceAnthropicAPI Source = "anthropic_api"
	SourceOpenAIAPI    Source = "openai_api"
	SourceManual       Source = "manual"
	SourceCSVImport    Source = "csv_import"
)

// Common service names. Service is a free-form string; these are the values
// the built-in collectors use.
const (
	ServiceAnthropic = "anthropic"
	ServiceOpenAI    = "openai"
	ServiceGoogle    = "google"
)

var allSources = []Source{
	SourceClaudeCode,
	SourceCodexCLI,
	SourceGeminiCLI,
	SourceAnthropicAPI,
	SourceOpenAIAPI,
	SourceManual,
	SourceCSVImport,
}

// AllSources returns every known source. The returned slice is a copy.
func AllSources() []Source {
	out := make([]Source, len(allSources))
	copy(out, allSources)
	return out
}

// Valid reports whether s is a known source.
func (s Source) Valid() bool {
	for _, k := range allSources {
		if s == k {
			return true
		}
	}
	return false
}

// String implements fmt.Stringer.
func (s Source) String() string { return string(s) }

// UsageRecord is one unit of token usage, normalized across all sources.
// (Source, ExternalID) uniquely identifies a record; writing the same pair
// again replaces the earlier values instead of adding to them.
type UsageRecord struct {
	Source     Source    `json:"source"`
	ExternalID string    `json:"external_id"`
	DeviceID   string    `json:"device_id"`
	Timestamp  time.Time `json:"timestamp"` // stored with millisecond precision, normalized to UTC

	Service string `json:"service"`
	Model   string `json:"model"`
	Project string `json:"project"`
	Session string `json:"session"`

	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	CacheReadTokens    int64 `json:"cache_read_tokens"`
	CacheWrite5mTokens int64 `json:"cache_write_5m_tokens"`
	CacheWrite1hTokens int64 `json:"cache_write_1h_tokens"`
	ThinkingTokens     int64 `json:"thinking_tokens"`
	RequestCount       int64 `json:"request_count"`

	// TotalOnly marks a record whose source reported only a combined token
	// count, with no input/output split (newer Codex CLI logs). The count is
	// kept in TotalOnlyTokens and the split counters stay zero. Such a record
	// is never priced: a cost needs the split, and guessing one would
	// misstate it.
	TotalOnly       bool  `json:"total_only"`
	TotalOnlyTokens int64 `json:"total_only_tokens"`

	IsSidechain bool `json:"is_sidechain"`

	// ReportedCostUSD is the cost the source itself reported, if any.
	// nil means "not reported", which is different from a reported zero.
	ReportedCostUSD *float64 `json:"reported_cost_usd"`
}

// TotalTokens returns input + output + cache read + cache write tokens, plus
// the combined count of a total-only record. ThinkingTokens are not added:
// providers usually already count them inside the output tokens, so adding
// them would double count.
func (r UsageRecord) TotalTokens() int64 {
	return r.InputTokens + r.OutputTokens + r.CacheReadTokens +
		r.CacheWrite5mTokens + r.CacheWrite1hTokens + r.TotalOnlyTokens
}

// Validate checks the invariants the store relies on.
func (r UsageRecord) Validate() error {
	if !r.Source.Valid() {
		return fmt.Errorf("invalid source %q", r.Source)
	}
	if r.ExternalID == "" {
		return errors.New("external id is empty")
	}
	if r.Timestamp.IsZero() || r.Timestamp.UnixMilli() < 0 {
		return errors.New("timestamp is zero or before 1970")
	}
	counters := []struct {
		name string
		v    int64
	}{
		{"input_tokens", r.InputTokens},
		{"output_tokens", r.OutputTokens},
		{"cache_read_tokens", r.CacheReadTokens},
		{"cache_write_5m_tokens", r.CacheWrite5mTokens},
		{"cache_write_1h_tokens", r.CacheWrite1hTokens},
		{"thinking_tokens", r.ThinkingTokens},
		{"request_count", r.RequestCount},
		{"total_only_tokens", r.TotalOnlyTokens},
	}
	for _, c := range counters {
		if c.v < 0 {
			return fmt.Errorf("%s is negative", c.name)
		}
	}
	if !r.TotalOnly && r.TotalOnlyTokens != 0 {
		return errors.New("total_only_tokens is set on a record that is not total-only")
	}
	if r.TotalOnly {
		if r.TotalOnlyTokens == 0 {
			return errors.New("total-only record has no total_only_tokens")
		}
		if r.InputTokens != 0 || r.OutputTokens != 0 || r.CacheReadTokens != 0 ||
			r.CacheWrite5mTokens != 0 || r.CacheWrite1hTokens != 0 {
			return errors.New("total-only record must not carry split token counters")
		}
	}
	if r.ReportedCostUSD != nil && *r.ReportedCostUSD < 0 {
		return errors.New("reported_cost_usd is negative")
	}
	return nil
}
