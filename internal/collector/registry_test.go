package collector

import (
	"context"
	"testing"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
)

type fakeCollector struct{ name model.Source }

func (f fakeCollector) Name() model.Source { return f.name }
func (f fakeCollector) Collect(context.Context, Deps) (Result, error) {
	return Result{Files: 1}, nil
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	if len(r.All()) != 0 {
		t.Fatal("new registry not empty")
	}

	if err := r.Register(fakeCollector{model.SourceClaudeCode}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(fakeCollector{model.SourceCodexCLI}); err != nil {
		t.Fatal(err)
	}

	all := r.All()
	if len(all) != 2 || all[0].Name() != model.SourceClaudeCode || all[1].Name() != model.SourceCodexCLI {
		t.Fatalf("All() order wrong: %v", all)
	}

	c, ok := r.Get(model.SourceCodexCLI)
	if !ok || c.Name() != model.SourceCodexCLI {
		t.Fatalf("Get(codex_cli) = %v, %v", c, ok)
	}
	if _, ok := r.Get(model.SourceGeminiCLI); ok {
		t.Fatal("Get of unregistered source succeeded")
	}
}

func TestRegistryRejects(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(nil); err == nil {
		t.Error("nil collector accepted")
	}
	if err := r.Register(fakeCollector{"bogus"}); err == nil {
		t.Error("invalid source accepted")
	}
	if err := r.Register(fakeCollector{model.SourceManual}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(fakeCollector{model.SourceManual}); err == nil {
		t.Error("duplicate source accepted")
	}
}

func TestCollectorInterface(t *testing.T) {
	var c Collector = fakeCollector{model.SourceManual}
	res, err := c.Collect(context.Background(), Deps{})
	if err != nil || res.Files != 1 {
		t.Fatalf("Collect = %+v, %v", res, err)
	}
}
