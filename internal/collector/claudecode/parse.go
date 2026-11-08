package claudecode

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
)

// syntheticModel marks assistant entries Claude Code writes without calling
// the API (e.g. interruption notices). They carry no real usage.
const syntheticModel = "<synthetic>"

// unknownModel is used when an API response has no model name.
const unknownModel = "unknown"

type logLine struct {
	Type        string          `json:"type"`
	UUID        string          `json:"uuid"`
	RequestID   string          `json:"requestId"`
	Timestamp   string          `json:"timestamp"`
	CWD         string          `json:"cwd"`
	SessionID   string          `json:"sessionId"`
	IsSidechain bool            `json:"isSidechain"`
	Message     json.RawMessage `json:"message"`
}

type logMessage struct {
	ID    string    `json:"id"`
	Model string    `json:"model"`
	Usage *logUsage `json:"usage"`
}

type logUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheCreation            *struct {
		Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	OutputTokensDetails *struct {
		ThinkingTokens int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

// parseLine converts one Claude Code log line into a usage record.
//
// ok is false for lines that legitimately carry no usage (user turns, tool
// results, synthetic entries). A non-nil err means the line looked like a
// usage line but could not be used; the caller reports it and moves on.
// Error text never includes line content (it may hold prompts).
func parseLine(line []byte, deviceID string) (rec model.UsageRecord, ok bool, err error) {
	var l logLine
	if err := json.Unmarshal(line, &l); err != nil {
		return rec, false, fmt.Errorf("invalid JSON: %w", scrub(err))
	}
	if l.Type != "assistant" || len(l.Message) == 0 {
		return rec, false, nil
	}
	var m logMessage
	if err := json.Unmarshal(l.Message, &m); err != nil {
		return rec, false, fmt.Errorf("invalid assistant message: %w", scrub(err))
	}
	if m.Usage == nil || m.Model == syntheticModel {
		return rec, false, nil
	}

	id := m.ID
	if id == "" {
		id = l.RequestID
	}
	if id == "" {
		id = l.UUID
	}
	if id == "" {
		return rec, false, errors.New("usage entry has no message id, requestId or uuid")
	}
	ts, perr := time.Parse(time.RFC3339Nano, l.Timestamp)
	if perr != nil {
		return rec, false, errors.New("usage entry has a missing or invalid timestamp")
	}

	u := m.Usage
	rec = model.UsageRecord{
		Source:          model.SourceClaudeCode,
		ExternalID:      id,
		DeviceID:        deviceID,
		Timestamp:       ts.UTC(),
		Service:         model.ServiceAnthropic,
		Model:           m.Model,
		Project:         l.CWD,
		Session:         l.SessionID,
		InputTokens:     u.InputTokens,
		OutputTokens:    u.OutputTokens,
		CacheReadTokens: u.CacheReadInputTokens,
		RequestCount:    1,
		IsSidechain:     l.IsSidechain,
	}
	if rec.Model == "" {
		rec.Model = unknownModel
	}
	// Use the 5m/1h breakdown when it carries data; otherwise the whole
	// cache creation total counts as 5m.
	if cc := u.CacheCreation; cc != nil && cc.Ephemeral5m+cc.Ephemeral1h > 0 {
		rec.CacheWrite5mTokens = cc.Ephemeral5m
		rec.CacheWrite1hTokens = cc.Ephemeral1h
	} else {
		rec.CacheWrite5mTokens = u.CacheCreationInputTokens
	}
	if d := u.OutputTokensDetails; d != nil {
		rec.ThinkingTokens = d.ThinkingTokens
	}
	if verr := rec.Validate(); verr != nil {
		return model.UsageRecord{}, false, fmt.Errorf("invalid usage entry: %w", verr)
	}
	return rec, true, nil
}

// scrub reduces json errors to a message that cannot quote line content.
func scrub(err error) error {
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return fmt.Errorf("syntax error at byte %d", se.Offset)
	}
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) {
		return fmt.Errorf("field %q has type %s, want %s", te.Field, te.Value, te.Type)
	}
	return errors.New("decode error")
}
