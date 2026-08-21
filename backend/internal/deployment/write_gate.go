package deployment

import (
	"net/http"
	"sync"
)

// WriteGate is a local first-version ingress gate. It deliberately has no
// distributed coordination semantics; external deployment controls must block
// every other writer before protected migration.
type WriteGate struct {
	mu      sync.RWMutex
	blocked bool
}

func NewWriteGate(blocked bool) *WriteGate { return &WriteGate{blocked: blocked} }

func (g *WriteGate) Block() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.blocked = true
	g.mu.Unlock()
}

func (g *WriteGate) Reopen() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.blocked = false
	g.mu.Unlock()
}

func (g *WriteGate) Blocked() bool {
	if g == nil {
		return true
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.blocked
}

// Middleware blocks only HTTP mutation methods. Readiness and bounded smoke
// callers can remain explicit, rather than accidentally bypassing the gate.
func (g *WriteGate) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.Blocked() && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			http.Error(w, "writes are temporarily blocked", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}
