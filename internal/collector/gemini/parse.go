package gemini

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
)

// unknownModel is used when a message carries tokens but no model name.
const unknownModel = "unknown"

const (
	messageTypeGemini = "gemini"
	kindSubagent      = "subagent"
)

// markers are the substrings a session line must contain to be worth
// decoding: the metadata line, model messages, and legacy full-history
// checkpoints. User messages (prompts, often large) never match.
var markers = [][]byte{
	[]byte(`"sessionId"`),
	[]byte(`"type":"gemini"`),
	[]byte(`"messages"`),
}

func interesting(line []byte) bool {
	for _, m := range markers {
		if bytes.Contains(line, m) {
			return true
		}
	}
	return false
}

// tokens mirrors TokensSummary in gemini-cli chatRecordingTypes.ts. The
// fields are the Gemini API usageMetadata counters: input is promptTokenCount
// (cached content included), output is candidatesTokenCount, thoughts and
// tool are reported separately from output and input respectively.
type tokens struct {
	Input    int64 `json:"input"`
	Output   int64 `json:"output"`
	Cached   int64 `json:"cached"`
	Thoughts int64 `json:"thoughts"`
	Tool     int64 `json:"tool"`
	Total    int64 `json:"total"`
}

func (t tokens) isZero() bool {
	return t.Input == 0 && t.Output == 0 && t.Cached == 0 && t.Thoughts == 0 && t.Tool == 0
}

// message is one MessageRecord of a session file. Content fields are left
// out on purpose: prompts and answers are never decoded.
type message struct {
	ID        string  `json:"id"`
	Timestamp string  `json:"timestamp"`
	Type      string  `json:"type"`
	Model     string  `json:"model"`
	Tokens    *tokens `json:"tokens"`
}

// sessionLine is any line of a session file, or a whole legacy .json file.
// A JSONL line is a metadata record (sessionId), a message, a $set / $patch /
// $rewindTo record; a legacy file is a metadata record with messages.
type sessionLine struct {
	message
	SessionID string    `json:"sessionId"`
	Kind      string    `json:"kind"`
	Messages  []message `json:"messages"`
	Set       *struct {
		Kind     string    `json:"kind"`
		Messages []message `json:"messages"`
	} `json:"$set"`
}

// fileContext is what a session file says about itself outside its messages.
// It is rebuilt on every scan (see primeContext), never persisted.
type fileContext struct {
	sessionID string
	metaSeen  bool // the first metadata record has been applied
	sidechain bool
	project   string
}

// result is the outcome for one model message: Rec is nil when the message
// carries no (or only zero) token usage.
type result struct {
	ID  string
	Rec *model.UsageRecord
}

// observe applies the first metadata record of the file.
func (c *fileContext) observe(l sessionLine) {
	if l.SessionID == "" || c.metaSeen {
		return
	}
	c.metaSeen = true
	c.sessionID = l.SessionID
	if l.Kind == kindSubagent {
		c.sidechain = true
	}
}

// handle processes one decoded line and returns the model messages found in
// it (a line holds one message, or many in legacy checkpoints). A non-nil err
// is the first message-level problem; the other results are still returned.
// Error text never includes line content (it may hold prompts).
//
// Identity (see docs/design.md): session id + message id. A message is re-appended whole
// when its tokens arrive later, and legacy checkpoints repeat every message,
// so the store's upsert keeps exactly one record per message. Rewinds and
// patches are ignored on purpose: the tokens of a rewound message were spent.
func (c *fileContext) handle(l sessionLine, deviceID string) (out []result, err error) {
	c.observe(l)
	add := func(m message) {
		if m.Type != messageTypeGemini {
			return
		}
		rec, merr := c.record(m, deviceID)
		if merr != nil && err == nil {
			err = merr
		}
		out = append(out, result{ID: m.ID, Rec: rec})
	}
	if l.ID != "" {
		add(l.message)
	}
	for _, m := range l.Messages {
		add(m)
	}
	if l.Set != nil {
		for _, m := range l.Set.Messages {
			add(m)
		}
	}
	return out, err
}

// record converts one model message. It returns nil, nil for a message
// without token usage.
func (c *fileContext) record(m message, deviceID string) (*model.UsageRecord, error) {
	if m.Tokens == nil || m.Tokens.isZero() {
		return nil, nil
	}
	if m.ID == "" {
		return nil, errors.New("message with token usage has no id")
	}
	ts, perr := time.Parse(time.RFC3339Nano, m.Timestamp)
	if perr != nil {
		return nil, errors.New("message with token usage has a missing or invalid timestamp")
	}
	t := m.Tokens
	// Cached content is a subset of the prompt tokens; thoughts are billed
	// as output but reported next to it; tool-use prompt tokens are extra
	// input. Total = input + output + thoughts + tool, so the stored
	// TotalTokens (thinking not added) matches the reported total.
	cached := min(max(t.Cached, 0), max(t.Input, 0))
	rec := model.UsageRecord{
		Source:          model.SourceGeminiCLI,
		ExternalID:      c.sessionID + ":" + m.ID,
		DeviceID:        deviceID,
		Timestamp:       ts.UTC(),
		Service:         model.ServiceGoogle,
		Model:           m.Model,
		Project:         c.project,
		Session:         c.sessionID,
		InputTokens:     t.Input - cached + t.Tool,
		OutputTokens:    t.Output + t.Thoughts,
		CacheReadTokens: cached,
		ThinkingTokens:  t.Thoughts,
		RequestCount:    1,
		IsSidechain:     c.sidechain,
	}
	if rec.Model == "" {
		rec.Model = unknownModel
	}
	if verr := rec.Validate(); verr != nil {
		return nil, fmt.Errorf("invalid usage entry: %w", verr)
	}
	return &rec, nil
}

// parseLine decodes one JSONL line of a session file.
func parseLine(line []byte, c *fileContext, deviceID string) ([]result, error) {
	if !interesting(line) {
		return nil, nil
	}
	var l sessionLine
	if err := json.Unmarshal(line, &l); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", scrub(err))
	}
	return c.handle(l, deviceID)
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
