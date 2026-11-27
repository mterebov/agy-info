package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	subagentIdleTimeout    = 120 * time.Second
	subagentPendingTimeout = 60 * time.Second
	liveStateTTL           = 2 * time.Second
	liveStateTimeout       = 250 * time.Millisecond
)

var (
	reSenderID = regexp.MustCompile(`sender=([0-9a-fA-F-]+)`)
)

type SubagentInfo struct {
	ID            string  `json:"id"`
	Role          string  `json:"role"`
	TypeName      string  `json:"type_name"`
	Model         string  `json:"model"`
	Status        string  `json:"status"` // "running" | "done" | "idle" | "stopped"
	Focus         string  `json:"focus"`
	LastTool      string  `json:"last_tool"`
	CurrentTokens int     `json:"current_tokens"`
	WindowTokens  int     `json:"window_tokens"`
	FillPct       float64 `json:"fill_pct"`
	LLMSteps      int     `json:"llm_steps"`
	ToolCalls     int     `json:"tool_calls"`
	UpdatedAt     string  `json:"updated_at"`
	LogURI        string  `json:"log_uri"`
}

type pendingInvoke struct {
	Role     string
	TypeName string
	Model    string
	StepSeq  int
	StepTime time.Time
}

type subagentRef struct {
	ID             string
	Role           string
	TypeName       string
	RequestedModel string
	LastStartStep  int
	LastStartTime  time.Time
	LastReplyStep  int
	KilledAtStep   int
	CreatedOrder   int
	LogURI         string
}

type subagentMetrics struct {
	LastModel     string
	CurrentTokens int
	LLMSteps      int
	ToolCalls     int
	LastFocus     string
	LastTool      string
}

type subagentCacheEntry struct {
	modTimeNano int64
	size        int64
	metrics     subagentMetrics
}

type liveStateCacheEntry struct {
	fetchedAt time.Time
	states    map[string]string
}

type subagentStat struct {
	ref       subagentRef
	path      string
	exists    bool
	modTime   time.Time
	mtimeNano int64
	size      int64
	status    string
}

type toolCallEntry struct {
	Name string                     `json:"name"`
	Args map[string]json.RawMessage `json:"args"`
}

type subagentTracker struct {
	pending     []pendingInvoke
	byID        map[string]*subagentRef
	order       []string
	nextOrder   int
	restartStep int
	restartTime time.Time
}

func newSubagentTracker() *subagentTracker {
	return &subagentTracker{
		byID:        make(map[string]*subagentRef),
		restartStep: -1,
	}
}

func (t *subagentTracker) onPlanner(seq int, stepTime time.Time, calls []toolCallEntry) {
	// Only GENERIC outputs immediately following a PLANNER_RESPONSE can resolve its invokes.
	t.pending = t.pending[:0]

	for _, tc := range calls {
		switch tc.Name {
		case "invoke_subagent":
			t.pending = append(t.pending, pendingInvoke{
				Role:     decodeArgString(getArgRaw(tc.Args, "Role")),
				TypeName: decodeArgString(getArgRaw(tc.Args, "TypeName")),
				Model:    decodeArgString(getArgRaw(tc.Args, "Model")),
				StepSeq:  seq,
				StepTime: stepTime,
			})
		case "send_message":
			recipient := strings.TrimSpace(decodeArgString(getArgRaw(tc.Args, "Recipient")))
			if recipient != "" {
				if ref, ok := t.byID[recipient]; ok {
					ref.LastStartStep = seq
					if !stepTime.IsZero() {
						ref.LastStartTime = stepTime
					}
				}
			}
		case "manage_subagents":
			action := strings.ToLower(strings.TrimSpace(decodeArgString(getArgRaw(tc.Args, "Action"))))
			switch action {
			case "kill_all":
				for _, ref := range t.byID {
					ref.KilledAtStep = seq
				}
			case "kill":
				ids := decodeArgStringSlice(getArgRaw(tc.Args, "ConversationIds"))
				for _, id := range ids {
					if ref, ok := t.byID[strings.TrimSpace(id)]; ok {
						ref.KilledAtStep = seq
					}
				}
			}
		}
	}
}

func (t *subagentTracker) onGeneric(rawContent json.RawMessage) {
	if len(t.pending) == 0 {
		return
	}
	content := decodeRawJSONString(rawContent)
	if content == "" {
		return
	}
	convID, logURI, ok := extractCreatedSubagent(content)
	if !ok {
		return
	}
	inv := t.pending[0]
	t.pending = t.pending[1:]

	if existing, exists := t.byID[convID]; exists {
		existing.Role = inv.Role
		existing.TypeName = inv.TypeName
		existing.RequestedModel = inv.Model
		existing.LastStartStep = inv.StepSeq
		existing.LastStartTime = inv.StepTime
		if logURI != "" {
			existing.LogURI = logURI
		}
		return
	}

	t.nextOrder++
	ref := &subagentRef{
		ID:             convID,
		Role:           inv.Role,
		TypeName:       inv.TypeName,
		RequestedModel: inv.Model,
		LastStartStep:  inv.StepSeq,
		LastStartTime:  inv.StepTime,
		LastReplyStep:  -1,
		KilledAtStep:   -1,
		CreatedOrder:   t.nextOrder,
		LogURI:         logURI,
	}
	t.byID[convID] = ref
	t.order = append(t.order, convID)
}

func (t *subagentTracker) onSystemMessage(seq int, stepTime time.Time, rawContent json.RawMessage) {
	content := decodeRawJSONString(rawContent)
	if content == "" {
		return
	}
	if strings.Contains(content, "All your subagents and background tasks have been stopped due to server restart") {
		t.restartStep = seq
		t.restartTime = stepTime
	}
	matches := reSenderID.FindAllStringSubmatch(content, -1)
	for _, m := range matches {
		if len(m) >= 2 {
			if ref, ok := t.byID[m[1]]; ok {
				ref.LastReplyStep = seq
			}
		}
	}
}

func (t *subagentTracker) refs() []subagentRef {
	if len(t.order) == 0 {
		return nil
	}
	out := make([]subagentRef, 0, len(t.order))
	for _, id := range t.order {
		if ref, ok := t.byID[id]; ok {
			out = append(out, *ref)
		}
	}
	return out
}

func extractCreatedSubagent(content string) (convID, logURI string, ok bool) {
	const marker = "Created the following subagent:"
	search := content
	for {
		idx := strings.Index(search, marker)
		if idx < 0 {
			return "", "", false
		}
		// Require marker at line start to ignore shell logs mentioning it inline.
		atLineStart := idx == 0 || search[idx-1] == '\n' || search[idx-1] == '\r'
		rest := search[idx+len(marker):]
		if atLineStart {
			trimmed := strings.TrimSpace(rest)
			if strings.HasPrefix(trimmed, "{") {
				var parsed struct {
					ConversationID string `json:"conversationId"`
					LogAbsoluteURI string `json:"logAbsoluteUri"`
				}
				dec := json.NewDecoder(strings.NewReader(trimmed))
				if err := dec.Decode(&parsed); err == nil && parsed.ConversationID != "" && validateConversationID(parsed.ConversationID) == nil {
					return parsed.ConversationID, parsed.LogAbsoluteURI, true
				}
			}
		}
		search = rest
	}
}

func getArgRaw(args map[string]json.RawMessage, key string) json.RawMessage {
	if len(args) == 0 {
		return nil
	}
	if v, ok := args[key]; ok {
		return v
	}
	for k, v := range args {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return nil
}

// decodeArgString handles native JSON strings and double-encoded compact transcript args.
func decodeArgString(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return ""
		}
		if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
			var inner string
			if err := json.Unmarshal([]byte(s), &inner); err == nil {
				return inner
			}
		}
		return s
	}
	return string(trimmed)
}

func decodeArgStringSlice(raw json.RawMessage) []string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return nil
		}
		trimmed = bytes.TrimSpace([]byte(s))
	}
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil
	}
	var out []string
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return nil
	}
	return out
}

func decodeRawJSONString(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return s
		}
	}
	return string(trimmed)
}

func parseToolCalls(raw json.RawMessage) []toolCallEntry {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var calls []toolCallEntry
	if err := json.Unmarshal(trimmed, &calls); err != nil {
		return nil
	}
	return calls
}

func computeSubagentStatus(
	ref subagentRef,
	restartStep int,
	restartTime time.Time,
	subExists bool,
	subMtime time.Time,
	now time.Time,
	liveStates map[string]string,
) string {
	if len(liveStates) > 0 {
		if st, ok := liveStates[ref.ID]; ok {
			if st == "RUN_STATE_RUNNING" || st == "RUN_STATE_CANCELLING" {
				return "running"
			}
		}
	}

	if ref.KilledAtStep >= 0 && ref.KilledAtStep >= ref.LastStartStep {
		return "stopped"
	}
	if ref.LastReplyStep > ref.LastStartStep {
		return "done"
	}
	if restartStep > ref.LastStartStep {
		if !subExists {
			return "stopped"
		}
		if !restartTime.IsZero() && !subMtime.After(restartTime) {
			return "stopped"
		}
		if now.Sub(subMtime) > subagentIdleTimeout {
			return "stopped"
		}
	}
	if subExists {
		if now.Sub(subMtime) <= subagentIdleTimeout {
			return "running"
		}
		return "idle"
	}
	if !ref.LastStartTime.IsZero() && now.Sub(ref.LastStartTime) <= subagentPendingTimeout {
		return "running"
	}
	return "idle"
}

func (s *Server) statSubagents(
	parentID string,
	refs []subagentRef,
	restartStep int,
	restartTime time.Time,
	now time.Time,
) []subagentStat {
	if len(refs) == 0 {
		return nil
	}
	liveStates := s.getLiveRunStates(parentID, now)
	stats := make([]subagentStat, len(refs))
	for i, ref := range refs {
		path, info, exists := s.resolveSubagentTranscript(ref)
		var mtime time.Time
		var mtimeNano int64
		var size int64
		if exists && info != nil {
			mtime = info.ModTime()
			mtimeNano = mtime.UnixNano()
			size = info.Size()
		}
		status := computeSubagentStatus(ref, restartStep, restartTime, exists, mtime, now, liveStates)
		stats[i] = subagentStat{
			ref:       ref,
			path:      path,
			exists:    exists,
			modTime:   mtime,
			mtimeNano: mtimeNano,
			size:      size,
			status:    status,
		}
	}
	return stats
}

func (s *Server) resolveSubagentTranscript(ref subagentRef) (string, os.FileInfo, bool) {
	if path, info, ok := s.resolveTranscript(ref.ID); ok {
		return path, info, true
	}
	if p, ok := pathFromFileURI(ref.LogURI); ok {
		if rel, err := filepath.Rel(s.brainDir, p); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			if info, serr := os.Stat(p); serr == nil && !info.IsDir() {
				return p, info, true
			}
		}
	}
	return "", nil, false
}

func (s *Server) buildSubagentInfos(stats []subagentStat) []SubagentInfo {
	if len(stats) == 0 {
		return []SubagentInfo{}
	}
	type sortable struct {
		info          SubagentInfo
		lastStartStep int
		createdOrder  int
		modTime       time.Time
	}
	items := make([]sortable, 0, len(stats))
	for _, st := range stats {
		var meta subagentMetrics
		if st.exists {
			meta = s.getSubagentMetrics(st.path, st.mtimeNano, st.size)
		}

		model := st.ref.RequestedModel
		rawModel := st.ref.RequestedModel
		if meta.LastModel != "" {
			model = humanModel(meta.LastModel)
			rawModel = meta.LastModel
		}
		window := defaultWindow(rawModel)
		fillPct := 0.0
		if window > 0 && meta.CurrentTokens > 0 {
			fillPct = round1(float64(meta.CurrentTokens) / float64(window) * 100.0)
		}

		// Populate LastTool only when no update_focus was recorded and the subagent is still active.
		focus := meta.LastFocus
		lastTool := ""
		if focus == "" && st.status != "done" && meta.LastTool != "" {
			lastTool = meta.LastTool
		}

		updatedAt := ""
		if st.exists {
			updatedAt = st.modTime.UTC().Format(time.RFC3339)
		} else if !st.ref.LastStartTime.IsZero() {
			updatedAt = st.ref.LastStartTime.UTC().Format(time.RFC3339)
		}

		items = append(items, sortable{
			info: SubagentInfo{
				ID:            st.ref.ID,
				Role:          st.ref.Role,
				TypeName:      st.ref.TypeName,
				Model:         model,
				Status:        st.status,
				Focus:         focus,
				LastTool:      lastTool,
				CurrentTokens: meta.CurrentTokens,
				WindowTokens:  window,
				FillPct:       fillPct,
				LLMSteps:      meta.LLMSteps,
				ToolCalls:     meta.ToolCalls,
				UpdatedAt:     updatedAt,
				LogURI:        s.resolveLogURI(st.ref.ID, st.ref.LogURI),
			},
			lastStartStep: st.ref.LastStartStep,
			createdOrder:  st.ref.CreatedOrder,
			modTime:       st.modTime,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		ri := items[i].info.Status == "running"
		rj := items[j].info.Status == "running"
		if ri != rj {
			return ri
		}
		if ri && !items[i].modTime.Equal(items[j].modTime) {
			return items[i].modTime.After(items[j].modTime)
		}
		if items[i].lastStartStep != items[j].lastStartStep {
			return items[i].lastStartStep > items[j].lastStartStep
		}
		return items[i].createdOrder > items[j].createdOrder
	})

	out := make([]SubagentInfo, len(items))
	for i := range items {
		out[i] = items[i].info
	}
	return out
}

func (s *Server) getSubagentMetrics(path string, mtimeNano, size int64) subagentMetrics {
	s.mu.Lock()
	if entry, ok := s.subagentCache[path]; ok && entry.modTimeNano == mtimeNano && entry.size == size {
		s.mu.Unlock()
		return entry.metrics
	}
	s.mu.Unlock()

	var metrics subagentMetrics
	if f, err := os.Open(path); err == nil {
		metrics = inspectSubagentReader(f)
		_ = f.Close()
	}

	s.mu.Lock()
	s.subagentCache[path] = subagentCacheEntry{
		modTimeNano: mtimeNano,
		size:        size,
		metrics:     metrics,
	}
	s.mu.Unlock()
	return metrics
}

func inspectSubagentReader(r io.Reader) subagentMetrics {
	br := bufio.NewReaderSize(r, 64*1024)
	var m subagentMetrics
	var fallbackTool string

	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 && bytes.Contains(line, []byte("PLANNER_RESPONSE")) {
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				var row struct {
					Type            string          `json:"type"`
					Model           string          `json:"model"`
					InputTokens     int             `json:"input_tokens"`
					CacheReadTokens int             `json:"cache_read_tokens"`
					OutputTokens    int             `json:"output_tokens"`
					ToolCalls       json.RawMessage `json:"tool_calls"`
				}
				if uerr := json.Unmarshal(trimmed, &row); uerr == nil && row.Type == "PLANNER_RESPONSE" {
					m.LLMSteps++
					if row.Model != "" {
						m.LastModel = row.Model
					}
					m.CurrentTokens = row.InputTokens + row.CacheReadTokens + row.OutputTokens

					calls := parseToolCalls(row.ToolCalls)
					m.ToolCalls += len(calls)
					for _, tc := range calls {
						if tc.Name == "update_focus" {
							if f := strings.TrimSpace(decodeArgString(getArgRaw(tc.Args, "focus"))); f != "" {
								m.LastFocus = f
							}
						} else if tc.Name != "" && tc.Name != "update_steps" {
							m.LastTool = tc.Name
						} else if tc.Name != "" && fallbackTool == "" {
							fallbackTool = tc.Name
						}
					}
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			break
		}
	}
	if m.LastTool == "" {
		m.LastTool = fallbackTool
	}
	return m
}

// getLiveRunStates queries the local Antigravity Language Server when configured.
func (s *Server) getLiveRunStates(parentID string, now time.Time) map[string]string {
	if parentID == "" {
		return nil
	}
	addr := strings.TrimSpace(os.Getenv("ANTIGRAVITY_LS_ADDRESS"))
	token := strings.TrimSpace(os.Getenv("ANTIGRAVITY_CSRF_TOKEN"))
	if addr == "" || token == "" {
		return nil
	}

	s.mu.Lock()
	if entry, ok := s.liveStateCache[parentID]; ok && now.Sub(entry.fetchedAt) < liveStateTTL {
		states := entry.states
		s.mu.Unlock()
		return states
	}
	s.mu.Unlock()

	states := fetchLiveRunStates(addr, token, parentID)

	s.mu.Lock()
	s.liveStateCache[parentID] = liveStateCacheEntry{
		fetchedAt: now,
		states:    states,
	}
	s.mu.Unlock()
	return states
}

func fetchLiveRunStates(addr, csrfToken, parentID string) map[string]string {
	body, err := json.Marshal(map[string]any{
		"parentConversationId": parentID,
		"pageSize":             100,
	})
	if err != nil {
		return nil
	}
	url := addr
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "http://" + url
	}
	url = strings.TrimRight(url, "/") + "/product.v1.ConversationService/ListConversations"

	ctx, cancel := context.WithTimeout(context.Background(), liveStateTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("x-antigravity-csrf-token", csrfToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var parsed struct {
		Conversations []struct {
			ID    string `json:"id"`
			State struct {
				RunState string `json:"runState"`
			} `json:"state"`
		} `json:"conversations"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 512*1024)).Decode(&parsed); err != nil {
		return nil
	}
	if len(parsed.Conversations) == 0 {
		return nil
	}
	out := make(map[string]string, len(parsed.Conversations))
	for _, c := range parsed.Conversations {
		if c.ID != "" && c.State.RunState != "" {
			out[c.ID] = c.State.RunState
		}
	}
	return out
}
