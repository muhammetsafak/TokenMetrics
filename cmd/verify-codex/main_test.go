package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/klauspost/compress/zstd"
)

const sampleRollout = `{"timestamp":"2026-09-01T10:00:00.000Z","type":"session_meta","payload":{"id":"11111111-0000-4000-8000-000000000001"}}
{"timestamp":"2026-09-01T10:00:01.000Z","type":"turn_context","payload":{"model":"gpt-test"}}
{"timestamp":"2026-09-01T10:00:02.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110},"last_token_usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110}}}}
`

func TestCompressedRolloutReadsLikePlain(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "rollout-2026-09-01T10-00-00-11111111-0000-4000-8000-000000000001.jsonl")
	if err := os.WriteFile(plain, []byte(sampleRollout), 0o600); err != nil {
		t.Fatal(err)
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	packed := plain + ".zst"
	if err := os.WriteFile(packed, enc.EncodeAll([]byte(sampleRollout), nil), 0o600); err != nil {
		t.Fatal(err)
	}

	read := func(path string, limit int64) (int64, map[string]candidate) {
		byKey := map[string]candidate{}
		st := logStats{ModelSources: map[string]int{}}
		off, _, err := readFile(context.Background(), path, limit, byKey, &st, nil)
		if err != nil {
			t.Fatalf("%s: %v", filepath.Base(path), err)
		}
		return off, byKey
	}
	offP, keysP := read(plain, int64(len(sampleRollout)))
	offZ, keysZ := read(packed, 0) // the limit is ignored for compressed files
	if offP != int64(len(sampleRollout)) || offZ != offP {
		t.Fatalf("offsets: plain %d, compressed %d, want %d", offP, offZ, len(sampleRollout))
	}
	if len(keysP) != 1 {
		t.Fatalf("plain keys = %d, want 1", len(keysP))
	}
	for k, c := range keysP {
		z, ok := keysZ[k]
		if !ok || !reflect.DeepEqual(c.u, z.u) || c.model != z.model || c.sess != z.sess || !c.ts.Equal(z.ts) {
			t.Errorf("key %s: plain %+v, compressed %+v", k, c, z)
		}
	}

	files, err := listRollouts(dir)
	if err != nil || len(files) != 2 || !isCompressed(files[1]) {
		t.Fatalf("listRollouts = %q, %v", files, err)
	}
}

func TestFallbackSessionStripsCompressedSuffix(t *testing.T) {
	for in, want := range map[string]string{
		"rollout-2026-09-01T10-00-00-abc-123.jsonl.zst": "abc-123",
		"rollout-2026-09-01T10-00-00-abc-123.jsonl":     "abc-123",
		"odd.jsonl.zst": "odd",
		"odd.jsonl":     "odd",
	} {
		if got := fallbackSession(in); got != want {
			t.Errorf("fallbackSession(%q) = %q, want %q", in, got, want)
		}
	}
}
