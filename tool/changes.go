package tool

import (
	"encoding/json"
	"sync"
)

// Shared is a tool's shared state, as interfaces report it: the change
// log's revision, and the changes after a revision (already in the form a
// caller sees them), and whether the capped log still reaches back that far.
// See opinionated/layering.md, "Shared state".
type Shared interface {
	Revision() int64
	Since(revision int64) (changes any, truncated bool)
}

// Reporter remembers the last revision one caller (an MCP session, REST
// callers) was told about, so each result can carry the changes since.
type Reporter struct {
	shared Shared
	mu     sync.Mutex
	last   int64
}

// NewReporter starts at revision `from` (0: everything so far is news).
func NewReporter(s Shared, from int64) *Reporter {
	return &Reporter{shared: s, last: from}
}

// Since returns the changes after rev (or, with rev < 0, after the last
// report), and marks them reported.
func (r *Reporter) Since(rev int64) (changes any, truncated bool, now int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rev < 0 {
		rev = r.last
	}
	now = r.shared.Revision()
	changes, truncated = r.shared.Since(rev)
	r.last = now
	return changes, truncated, now
}

// Envelope keys interfaces add to every result.
var Envelope = []string{"revision", "changes_since_last_call", "changes_truncated"}

// WithChanges is a command's outcome as MCP and REST send it: the result's
// fields (or {"error": ...} and any Extra fields), plus the revision and the
// changes since the reporter's last report. A result field that collides
// with those keys panics: that's a bug in the tool.
func WithChanges(rep *Reporter, out any, err error, i Interface) map[string]any {
	body := Body(out, err, i)
	if rep == nil {
		return body
	}
	for _, key := range Envelope {
		if _, taken := body[key]; taken {
			panic("tool: result field " + key + " collides with the shared-state fields")
		}
	}
	changes, truncated, rev := rep.Since(-1)
	body["revision"] = rev
	body["changes_since_last_call"] = changes
	if truncated {
		body["changes_truncated"] = true
	}
	return body
}

// Body is a command's outcome as a JSON object: the result's fields, or
// {"error": ...} (rendered for the interface) with any Extra fields.
func Body(out any, err error, i Interface) map[string]any {
	body := map[string]any{}
	if err != nil {
		e := *AsError(err)
		e.Message = Render(e.Message, i)
		body["error"] = e
		if x, ok := err.(Extra); ok {
			for k, v := range x.Extra() {
				body[k] = v
			}
		}
		return body
	}
	data, _ := json.Marshal(out)
	if json.Unmarshal(data, &body) != nil || body == nil {
		body = map[string]any{"result": out}
	}
	return body
}
