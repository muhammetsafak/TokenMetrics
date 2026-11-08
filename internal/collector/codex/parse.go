package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
)

// unknownModel is used when no line of the file names a model.
const unknownModel = "unknown"

// Rollout line types: the "type" of the JSONL envelope and of event_msg
// payloads (codex-rs/history RolloutItemWire, codex-rs/protocol EventMsg).
const (
	lineSessionMeta      = "session_meta"
	lineTurnContext      = "turn_context"
	lineEventMsg         = "event_msg"
	lineTokenUsageRecord = "token_usage_record"
	eventTokenCount      = "token_count"
	eventThreadSettings  = "thread_settings_applied"
)

// responseKeyPrefix starts the ExternalID of a token_usage_record.
const responseKeyPrefix = "r:"

// markers are the substrings a line must contain to be worth decoding.
// Rollout lines can be megabytes (tool output); this keeps them out of the
// JSON decoder.
var markers = [][]byte{
	[]byte(lineSessionMeta),
	[]byte(lineTurnContext),
	[]byte(lineTokenUsageRecord),
	[]byte(eventTokenCount),
	[]byte(eventThreadSettings),
}

func interesting(line []byte) bool {
	for _, m := range markers {
		if bytes.Contains(line, m) {
			return true
		}
	}
	return false
}

type envelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// tokenUsage mirrors codex_protocol::protocol::TokenUsage. input_tokens
// includes cached_input_tokens and cache_write_input_tokens; output_tokens
// includes reasoning_output_tokens; total_tokens = input + output.
type tokenUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

// splitZero reports whether every counter except total_tokens is zero, i.e.
// the entry has no input/output split.
func (u tokenUsage) splitZero() bool {
	return u.InputTokens == 0 && u.CachedInputTokens == 0 && u.CacheWriteInputTokens == 0 &&
		u.OutputTokens == 0 && u.ReasoningOutputTokens == 0
}

func (u tokenUsage) isZero() bool { return u.splitZero() && u.TotalTokens == 0 }

// isTotalOnly reports whether the entry carries only total_tokens (newer Codex
// versions write no split in last_token_usage).
func (u tokenUsage) isTotalOnly() bool { return u.splitZero() && u.TotalTokens > 0 }

type eventPayload struct {
	Type           string `json:"type"`
	ThreadSettings *struct {
		Model string `json:"model"`
	} `json:"thread_settings"`
	Info *struct {
		Total *tokenUsage `json:"total_token_usage"`
		Last  *tokenUsage `json:"last_token_usage"`
	} `json:"info"`
}

type usageRecordPayload struct {
	ResponseID string      `json:"response_id"`
	Usage      *tokenUsage `json:"usage"`
}

type sessionMetaPayload struct {
	ID     string          `json:"id"`
	CWD    string          `json:"cwd"`
	Source json.RawMessage `json:"source"`
	// BaseInstructions is decoded leniently (provenanceModel): its shape varies
	// between Codex versions and a surprise must not drop the session_meta.
	BaseInstructions json.RawMessage `json:"base_instructions"`
}

// provenanceModel returns base_instructions.provenance.model, or "" when the
// field is absent or shaped differently.
func provenanceModel(raw json.RawMessage) string {
	var bi struct {
		Provenance struct {
			Model string `json:"model"`
		} `json:"provenance"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &bi) != nil {
		return ""
	}
	return bi.Provenance.Model
}

type turnContextPayload struct {
	Model string `json:"model"`
	CWD   string `json:"cwd"`
}

// sessionContext is what a rollout file says about itself outside the usage
// lines: which session it is, the working directory and the current model.
// It is rebuilt on every scan (see primeContext), never persisted.
type sessionContext struct {
	sessionID string
	metaSeen  bool // the first session_meta line has been applied
	cwd       string
	sidechain bool
	// The model comes from the first of these that is set, in this order
	// (see modelName): newer Codex versions write no turn_context line.
	turnModel     string // turn_context.model
	settingsModel string // event_msg/thread_settings_applied.thread_settings.model
	metaModel     string // session_meta.base_instructions.provenance.model
	// hasUsageRecords is set once a token_usage_record line was seen. From
	// then on token_count events are ignored: newer Codex versions write
	// both for every response, and the record is the exact one.
	hasUsageRecords bool
}

// observe updates the context from a session_meta or turn_context line and
// reports whether the line was one of those. Only the first session_meta
// counts: a forked or resumed rollout may repeat its parent's.
func (c *sessionContext) observe(env envelope) (handled bool, err error) {
	switch env.Type {
	case lineSessionMeta:
		var p sessionMetaPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return true, fmt.Errorf("invalid session_meta: %w", scrub(err))
		}
		if c.metaSeen {
			return true, nil
		}
		c.metaSeen = true
		if p.ID != "" {
			c.sessionID = p.ID
		}
		if p.CWD != "" {
			c.cwd = p.CWD
		}
		c.sidechain = isSubAgentSource(p.Source)
		if m := provenanceModel(p.BaseInstructions); m != "" {
			c.metaModel = m
		}
		return true, nil
	case lineTurnContext:
		var p turnContextPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return true, fmt.Errorf("invalid turn_context: %w", scrub(err))
		}
		if p.Model != "" {
			c.turnModel = p.Model
		}
		if p.CWD != "" {
			c.cwd = p.CWD
		}
		return true, nil
	}
	return false, nil
}

// modelName returns the model of the file: turn_context, else the thread
// settings, else the session_meta provenance, else "unknown".
func (c *sessionContext) modelName() string {
	for _, m := range []string{c.turnModel, c.settingsModel, c.metaModel} {
		if m != "" {
			return m
		}
	}
	return unknownModel
}

// isSubAgentSource reports whether session_meta.source is the sub-agent
// variant, serialized as an object keyed by the variant name (a plain string
// such as "cli" is any other source).
func isSubAgentSource(raw json.RawMessage) bool {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return false
	}
	for k := range obj {
		if k = strings.ToLower(k); k == "subagent" || k == "sub_agent" {
			return true
		}
	}
	return false
}

// parseLine handles one rollout line. Context lines update ctx; usage lines
// yield a record. ok is false for everything else. A non-nil err means the
// line looked relevant but could not be used; the caller reports it and moves
// on. Error text never includes line content (it may hold prompts).
//
// Identity (see docs/design.md): a token_usage_record is keyed by its response_id; a
// token_count event, which has no response id, by the session plus the
// cumulative counters after it. Both are stable across re-reads and repeated
// events (Codex re-emits an unchanged token_count when only the rate limits
// refresh), so the store's upsert never counts a response twice. The
// per-response last_token_usage is stored, never the running total. An entry
// whose last_token_usage holds only total_tokens becomes a total-only record.
func parseLine(line []byte, ctx *sessionContext, deviceID string) (rec model.UsageRecord, ok bool, err error) {
	if !interesting(line) {
		return rec, false, nil
	}
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return rec, false, fmt.Errorf("invalid JSON: %w", scrub(err))
	}
	if handled, cerr := ctx.observe(env); handled {
		return rec, false, cerr
	}

	var (
		usage *tokenUsage
		extID string
	)
	switch env.Type {
	case lineTokenUsageRecord:
		var p usageRecordPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return rec, false, fmt.Errorf("invalid token_usage_record: %w", scrub(err))
		}
		ctx.hasUsageRecords = true
		if p.Usage == nil {
			return rec, false, nil
		}
		if p.ResponseID == "" {
			return rec, false, errors.New("token_usage_record has no response_id")
		}
		usage, extID = p.Usage, responseKeyPrefix+p.ResponseID
	case lineEventMsg:
		var p eventPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return rec, false, fmt.Errorf("invalid event_msg: %w", scrub(err))
		}
		if p.Type == eventThreadSettings {
			if p.ThreadSettings != nil && p.ThreadSettings.Model != "" {
				ctx.settingsModel = p.ThreadSettings.Model
			}
			return rec, false, nil
		}
		if p.Type != eventTokenCount || p.Info == nil || p.Info.Last == nil || ctx.hasUsageRecords {
			return rec, false, nil
		}
		usage = p.Info.Last
		var total tokenUsage
		if p.Info.Total != nil {
			total = *p.Info.Total
		}
		extID = fmt.Sprintf("%s:c:%d.%d.%d", ctx.sessionID, total.TotalTokens, total.InputTokens, total.OutputTokens)
	default:
		return rec, false, nil
	}
	if usage.isZero() {
		return rec, false, nil
	}
	if ctx.sessionID == "" {
		return rec, false, errors.New("usage entry has no session id")
	}
	ts, perr := time.Parse(time.RFC3339Nano, env.Timestamp)
	if perr != nil {
		return rec, false, errors.New("usage entry has a missing or invalid timestamp")
	}

	totalOnly := usage.isTotalOnly()
	cached := clamp(usage.CachedInputTokens, usage.InputTokens)
	written := clamp(usage.CacheWriteInputTokens, usage.InputTokens-cached)
	rec = model.UsageRecord{
		Source:             model.SourceCodexCLI,
		ExternalID:         extID,
		DeviceID:           deviceID,
		Timestamp:          ts.UTC(),
		Service:            model.ServiceOpenAI,
		Model:              ctx.modelName(),
		Project:            ctx.cwd,
		Session:            ctx.sessionID,
		InputTokens:        usage.InputTokens - cached - written,
		OutputTokens:       usage.OutputTokens,
		CacheReadTokens:    cached,
		CacheWrite5mTokens: written, // Codex does not report the cache TTL
		ThinkingTokens:     usage.ReasoningOutputTokens,
		RequestCount:       1,
		IsSidechain:        ctx.sidechain,
	}
	if totalOnly {
		// No split to price: keep the combined count and mark the record so
		// that no cost is computed for it (see model.UsageRecord.TotalOnly).
		rec.TotalOnly = true
		rec.TotalOnlyTokens = usage.TotalTokens
	}
	if verr := rec.Validate(); verr != nil {
		return model.UsageRecord{}, false, fmt.Errorf("invalid usage entry: %w", verr)
	}
	return rec, true, nil
}

// clamp limits v to [0, max(hi, 0)]. Cached and cache-write counts are
// subsets of input_tokens; a malformed line must not produce negative input.
func clamp(v, hi int64) int64 {
	return min(max(v, 0), max(hi, 0))
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
