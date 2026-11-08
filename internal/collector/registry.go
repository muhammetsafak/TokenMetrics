package collector

import (
	"errors"
	"fmt"
	"sync"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
)

// Registry holds the collectors in registration order. It is safe for
// concurrent use.
type Registry struct {
	mu    sync.RWMutex
	order []model.Source
	byKey map[model.Source]Collector
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byKey: map[model.Source]Collector{}}
}

// Register adds c. It fails for a nil collector, an invalid source name, or a
// source that is already registered.
func (r *Registry) Register(c Collector) error {
	if c == nil {
		return errors.New("collector: nil collector")
	}
	name := c.Name()
	if !name.Valid() {
		return fmt.Errorf("collector: invalid source %q", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byKey[name]; dup {
		return fmt.Errorf("collector: %q already registered", name)
	}
	r.byKey[name] = c
	r.order = append(r.order, name)
	return nil
}

// All returns the collectors in registration order.
func (r *Registry) All() []Collector {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Collector, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.byKey[n])
	}
	return out
}

// Get returns the collector registered for name.
func (r *Registry) Get(name model.Source) (Collector, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.byKey[name]
	return c, ok
}
