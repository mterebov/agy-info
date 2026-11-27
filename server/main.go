package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	defaultProtocolVersion = "2025-06-18"
	serverName             = "agy-info"
	serverVersion          = "1.0.0"
	uiResourceURI          = "ui://agy-info/app.html"
	uiResourceMimeType     = "text/html;profile=mcp-app"
	maxSessions            = 15
	maxTimelinePoints      = 55
	maxTitleRunes          = 54
	titleReadBytes         = 64 * 1024
	titleMaxLines          = 15
	dirScanTTL             = 5 * time.Second
)

var (
	reGeminiModel   = regexp.MustCompile(`(?i)^gemini-(\d+(?:\.\d+)?)-([a-z0-9]+)(?:-(xhigh|high|medium|low|lite))?$`)
	reMetaBlock     = regexp.MustCompile(`(?is)<ADDITIONAL_METADATA>.*?</ADDITIONAL_METADATA>`)
	reMetaOpen      = regexp.MustCompile(`(?is)<ADDITIONAL_METADATA>.*$`)
	reUserReqTags   = regexp.MustCompile(`(?i)</?USER_REQUEST>`)
	errInvalidID    = errors.New("invalid conversation_id")
	geminiTierNames = map[string]string{
		"xhigh":  "XHigh",
		"high":   "High",
		"medium": "Medium",
		"low":    "Low",
		"lite":   "Lite",
	}
)

// JSON-RPC 2.0 wire types.

type rpcRequest struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCP protocol payload types.

type initializeParams struct {
	ProtocolVersion string `json:"protocolVersion"`
}

type initializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    serverCapabilities `json:"capabilities"`
	ServerInfo      serverInfo         `json:"serverInfo"`
}

type serverCapabilities struct {
	Tools     struct{} `json:"tools"`
	Resources struct{} `json:"resources"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type uiBorderMeta struct {
	UI struct {
		PrefersBorder bool `json:"prefersBorder"`
	} `json:"ui"`
}

type resourceInfo struct {
	URI         string       `json:"uri"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	MimeType    string       `json:"mimeType"`
	Meta        uiBorderMeta `json:"_meta"`
}

type resourceListResult struct {
	Resources []resourceInfo `json:"resources"`
}

type resourceReadParams struct {
	URI string `json:"uri"`
}

type resourceContent struct {
	URI      string       `json:"uri"`
	MimeType string       `json:"mimeType"`
	Text     string       `json:"text"`
	Meta     uiBorderMeta `json:"_meta"`
}

type resourceReadResult struct {
	Contents []resourceContent `json:"contents"`
}

type toolDef struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
	Meta        any    `json:"_meta"`
}

type toolListResult struct {
	Tools []toolDef `json:"tools"`
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type toolCallArgs struct {
	ConversationID string `json:"conversation_id"`
	LastRevision   string `json:"last_revision"`
	ForceRescan    bool   `json:"force_rescan"`
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolCallResult struct {
	Content           []textContent    `json:"content"`
	StructuredContent *SnapshotPayload `json:"structuredContent"`
	IsError           bool             `json:"isError"`
}

// Domain types.

type SnapshotPayload struct {
	Unchanged            bool             `json:"unchanged"`
	Revision             string           `json:"revision"`
	AutoSelected         bool             `json:"auto_selected"`
	ConversationID       string           `json:"conversation_id"`
	ParentConversationID string           `json:"parent_conversation_id"`
	LogURI               string           `json:"log_uri"`
	UpdatedAt            string           `json:"updated_at"`
	FromCache            bool             `json:"from_cache"`
	Sessions             []SessionSummary `json:"sessions"`
	Snapshot             *Snapshot        `json:"snapshot"`
	Subagents            []SubagentInfo   `json:"subagents"`
	Plan                 PlanInfo         `json:"plan"`
}

// MarshalJSON emits a minimal 5-field payload on unchanged polling ticks
// and guarantees non-nil slices on full snapshots.
func (p SnapshotPayload) MarshalJSON() ([]byte, error) {
	if p.Unchanged {
		return json.Marshal(struct {
			Unchanged      bool   `json:"unchanged"`
			Revision       string `json:"revision"`
			ConversationID string `json:"conversation_id"`
			UpdatedAt      string `json:"updated_at"`
			FromCache      bool   `json:"from_cache"`
		}{
			Unchanged:      true,
			Revision:       p.Revision,
			ConversationID: p.ConversationID,
			UpdatedAt:      p.UpdatedAt,
			FromCache:      p.FromCache,
		})
	}
	type fullPayload SnapshotPayload
	cp := fullPayload(p)
	if cp.Sessions == nil {
		cp.Sessions = []SessionSummary{}
	}
	if cp.Subagents == nil {
		cp.Subagents = []SubagentInfo{}
	}
	if cp.Plan.Steps == nil {
		cp.Plan.Steps = []PlanStep{}
	}
	if cp.Plan.Source == "" {
		cp.Plan.Source = "none"
	}
	return json.Marshal(cp)
}

type SessionSummary struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	UpdatedAt  string  `json:"updated_at"`
	SizeKB     float64 `json:"size_kb"`
	IsActive   bool    `json:"is_active"`
	IsSubagent bool    `json:"is_subagent"`
}

type Snapshot struct {
	Model         string            `json:"model"`
	WindowTokens  int               `json:"window_tokens"`
	CurrentTokens int               `json:"current_tokens"`
	FreeTokens    int               `json:"free_tokens"`
	PeakTokens    int               `json:"peak_tokens"`
	Compactions   int               `json:"compactions"`
	FillPct       float64           `json:"fill_pct"`
	Status        string            `json:"status"`
	Advice        string            `json:"advice"`
	LastTurn      LastTurnMetrics   `json:"last_turn"`
	Cumulative    CumulativeMetrics `json:"cumulative"`
	Breakdown     []BreakdownItem   `json:"breakdown"`
	Counts        SessionCounts     `json:"counts"`
	Timeline      []TimelinePoint   `json:"timeline"`
}

type LastTurnMetrics struct {
	Prompt        int     `json:"prompt"`
	UncachedInput int     `json:"uncached_input"`
	CacheRead     int     `json:"cache_read"`
	Output        int     `json:"output"`
	CacheHitPct   float64 `json:"cache_hit_pct"`
}

type CumulativeMetrics struct {
	PromptTotal   int     `json:"prompt_total"`
	UncachedInput int     `json:"uncached_input"`
	CacheRead     int     `json:"cache_read"`
	Output        int     `json:"output"`
	CacheHitPct   float64 `json:"cache_hit_pct"`
}

type BreakdownItem struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Tokens int    `json:"tokens"`
}

type SessionCounts struct {
	UserTurns    int      `json:"user_turns"`
	LLMSteps     int      `json:"llm_steps"`
	ToolCalls    int      `json:"tool_calls"`
	Subagents    int      `json:"subagents"`
	DurationMin  *float64 `json:"duration_min"`
	TranscriptKB float64  `json:"transcript_kb"`
}

type TimelinePoint struct {
	N               int  `json:"n"`
	Step            int  `json:"step"`
	Total           int  `json:"total"`
	Cache           int  `json:"cache"`
	New             int  `json:"new"`
	Out             int  `json:"out"`
	CompactedBefore bool `json:"compacted_before"`
}

type transcriptRow struct {
	Type            string          `json:"type"`
	StepIndex       int             `json:"step_index"`
	CreatedAt       string          `json:"created_at"`
	Model           string          `json:"model"`
	InputTokens     int             `json:"input_tokens"`
	CacheReadTokens int             `json:"cache_read_tokens"`
	OutputTokens    int             `json:"output_tokens"`
	Content         json.RawMessage `json:"content"`
	Thinking        json.RawMessage `json:"thinking"`
	ToolCalls       json.RawMessage `json:"tool_calls"`
}

type transcriptAnalysis struct {
	snapshot     *Snapshot
	subagentRefs []subagentRef
	restartStep  int
	restartTime  time.Time
	plan         PlanInfo
	hasUserInput bool
}

type analysisCacheEntry struct {
	modTimeNano int64
	size        int64
	analysis    *transcriptAnalysis
}

type titleCacheEntry struct {
	hasData      bool
	title        string
	hasUserInput bool
}

type sessionCandidate struct {
	id      string
	path    string
	modTime time.Time
	size    int64
}

type payloadOptions struct {
	RequestedID    string
	LastRevision   string
	ForceRescan    bool
	AllowUnchanged bool
}

// Server implements the stdio MCP server and in-memory transcript caches.
type Server struct {
	brainDir string
	htmlPath string
	nowFunc  func() time.Time

	mu                sync.Mutex
	analysisCache     map[string]analysisCacheEntry
	subagentCache     map[string]subagentCacheEntry
	titleCache        map[string]titleCacheEntry
	artifactPlanCache map[string]artifactPlanCacheEntry
	parentBySubagent  map[string]string
	liveStateCache    map[string]liveStateCacheEntry

	dirScanAt        time.Time
	dirModTimeNano   int64
	cachedCandidates []sessionCandidate
	cachedAutoID     string
	sessionsVersion  uint64
	sessionsSig      string
}

func NewServer(brainDir, htmlPath string) *Server {
	return &Server{
		brainDir:          brainDir,
		htmlPath:          htmlPath,
		nowFunc:           time.Now,
		analysisCache:     make(map[string]analysisCacheEntry),
		subagentCache:     make(map[string]subagentCacheEntry),
		titleCache:        make(map[string]titleCacheEntry),
		artifactPlanCache: make(map[string]artifactPlanCacheEntry),
		parentBySubagent:  make(map[string]string),
		liveStateCache:    make(map[string]liveStateCacheEntry),
	}
}

func (s *Server) now() time.Time {
	if s.nowFunc != nil {
		return s.nowFunc()
	}
	return time.Now()
}

// Serve processes newline-delimited JSON-RPC 2.0 messages until EOF.
func (s *Server) Serve(in io.Reader, out io.Writer, errOut io.Writer) error {
	reader := bufio.NewReaderSize(in, 64*1024)
	writer := bufio.NewWriterSize(out, 64*1024)

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				if resp, ok := s.handleLine(trimmed, errOut); ok {
					data, merr := json.Marshal(resp)
					if merr != nil {
						fmt.Fprintf(errOut, "agy-info: marshal response: %v\n", merr)
					} else {
						if _, werr := writer.Write(data); werr != nil {
							return werr
						}
						if werr := writer.WriteByte('\n'); werr != nil {
							return werr
						}
						if werr := writer.Flush(); werr != nil {
							return werr
						}
					}
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func (s *Server) handleLine(line []byte, errOut io.Writer) (*rpcResponse, bool) {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		fmt.Fprintf(errOut, "agy-info: invalid json: %v\n", err)
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      json.RawMessage("null"),
			Error:   &rpcError{Code: -32700, Message: "Parse error"},
		}, true
	}

	// JSON-RPC notifications omit id and must not receive a response.
	if req.ID == nil || len(bytes.TrimSpace(*req.ID)) == 0 {
		return nil, false
	}
	id := *req.ID

	if req.Method == "" {
		return errorResp(id, -32600, "Invalid Request"), true
	}

	switch req.Method {
	case "initialize":
		return s.handleInitialize(id, req.Params), true
	case "ping":
		return &rpcResponse{JSONRPC: "2.0", ID: id, Result: struct{}{}}, true
	case "resources/list":
		return s.handleResourcesList(id), true
	case "resources/read":
		return s.handleResourcesRead(id, req.Params), true
	case "tools/list":
		return s.handleToolsList(id), true
	case "tools/call":
		return s.handleToolsCall(id, req.Params, errOut), true
	default:
		return errorResp(id, -32601, fmt.Sprintf("Method not found: %s", req.Method)), true
	}
}

func errorResp(id json.RawMessage, code int, msg string) *rpcResponse {
	return &rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: msg},
	}
}

func (s *Server) handleInitialize(id json.RawMessage, rawParams json.RawMessage) *rpcResponse {
	var p initializeParams
	if len(rawParams) > 0 {
		_ = json.Unmarshal(rawParams, &p)
	}
	proto := p.ProtocolVersion
	if proto == "" {
		proto = defaultProtocolVersion
	}
	return &rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result: initializeResult{
			ProtocolVersion: proto,
			Capabilities:    serverCapabilities{},
			ServerInfo: serverInfo{
				Name:    serverName,
				Version: serverVersion,
			},
		},
	}
}

func (s *Server) handleResourcesList(id json.RawMessage) *rpcResponse {
	return &rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result: resourceListResult{
			Resources: []resourceInfo{
				{
					URI:         uiResourceURI,
					Name:        "AGY Info UI",
					Description: "Live Antigravity context window, subagents, and plan monitor",
					MimeType:    uiResourceMimeType,
					Meta:        uiBorderMeta{},
				},
			},
		},
	}
}

func (s *Server) handleResourcesRead(id json.RawMessage, rawParams json.RawMessage) *rpcResponse {
	var p resourceReadParams
	if err := json.Unmarshal(rawParams, &p); err != nil {
		return errorResp(id, -32602, "Invalid params")
	}
	if p.URI != uiResourceURI {
		return errorResp(id, -32002, fmt.Sprintf("Resource not found: %s", p.URI))
	}
	data, err := os.ReadFile(s.htmlPath)
	if err != nil {
		return errorResp(id, -32002, fmt.Sprintf("Resource unavailable: %v", err))
	}
	return &rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result: resourceReadResult{
			Contents: []resourceContent{
				{
					URI:      uiResourceURI,
					MimeType: uiResourceMimeType,
					Text:     string(data),
					Meta:     uiBorderMeta{},
				},
			},
		},
	}
}

func (s *Server) handleToolsList(id json.RawMessage) *rpcResponse {
	tools := []toolDef{
		{
			Name:        "open_agy_info",
			Title:       "AGY Info",
			Description: "Open the AGY Info panel showing live Antigravity session context window fill, running subagents, current plan, and token activity graph.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"conversation_id": map[string]any{
						"type":        "string",
						"description": "Optional conversation ID; omit for most recently active session.",
					},
				},
			},
			Meta: map[string]any{
				"ui": map[string]any{
					"resourceUri": uiResourceURI,
				},
				"com.google.antigravity/ui": map[string]any{
					"entrypoints": []map[string]any{
						{"type": "aux_pane", "title": "AGY Info"},
						{"type": "global", "title": "AGY Info"},
					},
				},
			},
		},
		{
			Name:        "get_context_snapshot",
			Title:       "Get Context Snapshot",
			Description: "Fetch latest context metrics, active subagents, current plan, and recent sessions list (app-only).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"conversation_id": map[string]any{
						"type":        "string",
						"description": "Optional conversation ID; empty or 'auto' picks the most recently active user session.",
					},
					"last_revision": map[string]any{
						"type":        "string",
						"description": "Revision token from previous snapshot; when unchanged and force_rescan is false, returns a minimal unchanged payload.",
					},
					"force_rescan": map[string]any{
						"type":        "boolean",
						"description": "Force rescanning brain directory and return a full snapshot.",
					},
				},
			},
			Meta: map[string]any{
				"ui": map[string]any{
					"visibility": []string{"app"},
				},
			},
		},
	}
	return &rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  toolListResult{Tools: tools},
	}
}

func (s *Server) handleToolsCall(id json.RawMessage, rawParams json.RawMessage, errOut io.Writer) *rpcResponse {
	var p toolCallParams
	if err := json.Unmarshal(rawParams, &p); err != nil {
		return errorResp(id, -32602, "Invalid params")
	}
	if p.Name != "open_agy_info" && p.Name != "get_context_snapshot" {
		return errorResp(id, -32602, fmt.Sprintf("Unknown tool: %s", p.Name))
	}

	var args toolCallArgs
	if len(p.Arguments) > 0 && !bytes.Equal(bytes.TrimSpace(p.Arguments), []byte("null")) {
		if err := json.Unmarshal(p.Arguments, &args); err != nil {
			return errorResp(id, -32602, "Invalid tool arguments")
		}
	}

	if err := validateConversationID(args.ConversationID); err != nil {
		return errorResp(id, -32602, err.Error())
	}

	// open_agy_info always returns a full snapshot.
	allowUnchanged := p.Name == "get_context_snapshot"
	payload, err := s.buildPayloadWithOptions(payloadOptions{
		RequestedID:    args.ConversationID,
		LastRevision:   args.LastRevision,
		ForceRescan:    args.ForceRescan,
		AllowUnchanged: allowUnchanged,
	}, errOut)
	if err != nil {
		fmt.Fprintf(errOut, "agy-info: buildPayload: %v\n", err)
		return errorResp(id, -32603, err.Error())
	}

	var content []textContent
	if payload.Unchanged {
		content = []textContent{}
	} else {
		content = []textContent{{Type: "text", Text: formatSummary(payload)}}
	}

	return &rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result: toolCallResult{
			Content:           content,
			StructuredContent: payload,
			IsError:           false,
		},
	}
}

// validateConversationID rejects path separators and traversal segments.
func validateConversationID(id string) error {
	if id == "" || id == "auto" {
		return nil
	}
	if id == "." || strings.Contains(id, "..") || strings.ContainsAny(id, "/\\\x00") {
		return errInvalidID
	}
	if filepath.Base(id) != id {
		return errInvalidID
	}
	return nil
}

// fileURI converts an OS path into a cross-platform file:// URI.
func fileURI(path string) string {
	if path == "" {
		return ""
	}
	slashed := filepath.ToSlash(path)
	if strings.HasPrefix(slashed, "//") {
		return "file:" + slashed
	}
	if strings.HasPrefix(slashed, "/") {
		return "file://" + slashed
	}
	return "file:///" + slashed
}

// pathFromFileURI extracts a clean OS path from a file:// URI.
func pathFromFileURI(uri string) (string, bool) {
	if !strings.HasPrefix(uri, "file://") {
		return "", false
	}
	p := strings.TrimPrefix(uri, "file://")
	if len(p) >= 3 && p[0] == '/' && isASCIIAlpha(p[1]) && p[2] == ':' {
		p = p[1:]
	}
	return filepath.Clean(filepath.FromSlash(p)), true
}

func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func (s *Server) buildPayload(requestedID string, errOut io.Writer) (*SnapshotPayload, error) {
	return s.buildPayloadWithOptions(payloadOptions{RequestedID: requestedID}, errOut)
}

func (s *Server) buildPayloadWithOptions(opts payloadOptions, errOut io.Writer) (*SnapshotPayload, error) {
	now := s.now()
	candidates, sessVer, dirCached := s.getCandidates(opts.ForceRescan, now)
	autoMode := opts.RequestedID == "" || opts.RequestedID == "auto"

	var active *sessionCandidate
	var analysis *transcriptAnalysis
	var fromCache bool

	if autoMode {
		// Fast path between directory scans: stat only the previously auto-selected session.
		if dirCached {
			if cachedID := s.getCachedAutoID(); cachedID != "" {
				if path, info, ok := s.resolveTranscript(cachedID); ok {
					active = &sessionCandidate{
						id:      cachedID,
						path:    path,
						modTime: info.ModTime(),
						size:    info.Size(),
					}
					s.refreshActiveCandidate(active, candidates)
					res, cached, err := s.getAnalysis(active.id, active.path, active.modTime, active.size, errOut)
					if err == nil && res != nil && res.snapshot != nil {
						analysis = res
						fromCache = cached
					} else {
						active = nil
					}
				}
			}
		}

		if active == nil {
			active, analysis, fromCache = s.selectAutoSession(candidates, errOut)
			if active != nil {
				s.setCachedAutoID(active.id)
			}
		}
	} else {
		if path, info, ok := s.resolveTranscript(opts.RequestedID); ok {
			active = &sessionCandidate{
				id:      opts.RequestedID,
				path:    path,
				modTime: info.ModTime(),
				size:    info.Size(),
			}
			s.refreshActiveCandidate(active, candidates)
			res, cached, err := s.getAnalysis(active.id, active.path, active.modTime, active.size, errOut)
			if err != nil {
				fmt.Fprintf(errOut, "agy-info: analyze %s: %v\n", active.id, err)
			} else {
				analysis = res
				fromCache = cached
			}
		}
	}

	activeID := ""
	var mainPath string
	var mainMtimeNano int64
	var mainSize int64
	if active != nil {
		activeID = active.id
		mainPath = active.path
		mainMtimeNano = active.modTime.UnixNano()
		mainSize = active.size
	} else if !autoMode {
		activeID = opts.RequestedID
	}

	parentID := s.getParentID(activeID)
	if parentID == "" && active != nil && (analysis == nil || !analysis.hasUserInput) {
		parentID = s.findParentForSubagent(activeID, candidates, errOut)
	}

	var subStats []subagentStat
	if analysis != nil && len(analysis.subagentRefs) > 0 {
		subStats = s.statSubagents(activeID, analysis.subagentRefs, analysis.restartStep, analysis.restartTime, now)
	}

	artStat := s.statArtifactPlan(activeID)
	rev := computeRevision(autoMode, activeID, parentID, mainPath, mainMtimeNano, mainSize, sessVer, artStat, subStats)

	if opts.AllowUnchanged && opts.LastRevision != "" && !opts.ForceRescan && rev == opts.LastRevision {
		return &SnapshotPayload{
			Unchanged:      true,
			Revision:       rev,
			ConversationID: activeID,
			UpdatedAt:      now.UTC().Format(time.RFC3339),
			FromCache:      true,
		}, nil
	}

	sessions := s.buildSessionsList(candidates, active)

	var snap *Snapshot
	var plan PlanInfo
	if analysis != nil {
		snap = analysis.snapshot
		plan = s.resolvePlanInfo(analysis.plan, artStat)
	} else {
		plan = s.resolvePlanInfo(PlanInfo{}, artStat)
	}

	subagents := s.buildSubagentInfos(subStats)

	updatedAt := now.UTC().Format(time.RFC3339)
	if active != nil {
		updatedAt = active.modTime.UTC().Format(time.RFC3339)
	}

	logURI := ""
	if activeID != "" {
		logURI = s.resolveLogURI(activeID, "")
	}

	return &SnapshotPayload{
		Unchanged:            false,
		Revision:             rev,
		AutoSelected:         autoMode,
		ConversationID:       activeID,
		ParentConversationID: parentID,
		LogURI:               logURI,
		UpdatedAt:            updatedAt,
		FromCache:            fromCache,
		Sessions:             sessions,
		Snapshot:             snap,
		Subagents:            subagents,
		Plan:                 plan,
	}, nil
}

func (s *Server) selectAutoSession(candidates []sessionCandidate, errOut io.Writer) (*sessionCandidate, *transcriptAnalysis, bool) {
	// Pass 1: check likely user chats first without parsing heavy subagent transcripts.
	var fallbackCand *sessionCandidate
	var fallbackRes *transcriptAnalysis
	var fallbackCached bool

	for i := range candidates {
		cand := &candidates[i]
		if s.isCandidateSubagent(*cand) {
			continue
		}
		res, cached, err := s.getAnalysis(cand.id, cand.path, cand.modTime, cand.size, errOut)
		if err != nil {
			fmt.Fprintf(errOut, "agy-info: analyze %s: %v\n", cand.id, err)
			continue
		}
		if res == nil || res.snapshot == nil {
			continue
		}
		if res.snapshot.Counts.UserTurns > 0 {
			return cand, res, cached
		}
		if fallbackCand == nil {
			fallbackCand = cand
			fallbackRes = res
			fallbackCached = cached
		}
	}

	// Pass 2: fallback over remaining candidates.
	for i := range candidates {
		cand := &candidates[i]
		if !s.isCandidateSubagent(*cand) {
			continue
		}
		res, cached, err := s.getAnalysis(cand.id, cand.path, cand.modTime, cand.size, errOut)
		if err != nil {
			fmt.Fprintf(errOut, "agy-info: analyze %s: %v\n", cand.id, err)
			continue
		}
		if res == nil || res.snapshot == nil {
			continue
		}
		if res.snapshot.Counts.UserTurns > 0 {
			return cand, res, cached
		}
		if fallbackCand == nil {
			fallbackCand = cand
			fallbackRes = res
			fallbackCached = cached
		}
	}

	return fallbackCand, fallbackRes, fallbackCached
}

func (s *Server) buildSessionsList(candidates []sessionCandidate, active *sessionCandidate) []SessionSummary {
	userCands := make([]sessionCandidate, 0, len(candidates))
	subCands := make([]sessionCandidate, 0, len(candidates))
	for _, c := range candidates {
		if s.isCandidateSubagent(c) {
			subCands = append(subCands, c)
		} else {
			userCands = append(userCands, c)
		}
	}

	primary := userCands
	if len(primary) == 0 {
		primary = subCands
	}

	limit := len(primary)
	if limit > maxSessions {
		limit = maxSessions
	}
	sessions := make([]SessionSummary, 0, limit+1)
	activeInList := false

	for i := 0; i < limit; i++ {
		c := primary[i]
		isAct := active != nil && c.id == active.id
		if isAct {
			activeInList = true
		}
		title, _ := s.getTitleMeta(c.id, c.path, c.size)
		sessions = append(sessions, SessionSummary{
			ID:         c.id,
			Title:      title,
			UpdatedAt:  c.modTime.UTC().Format(time.RFC3339),
			SizeKB:     round1(float64(c.size) / 1024.0),
			IsActive:   isAct,
			IsSubagent: s.isCandidateSubagent(c),
		})
	}

	// Keep an explicitly selected older session or subagent visible within the cap.
	if active != nil && !activeInList {
		title, _ := s.getTitleMeta(active.id, active.path, active.size)
		entry := SessionSummary{
			ID:         active.id,
			Title:      title,
			UpdatedAt:  active.modTime.UTC().Format(time.RFC3339),
			SizeKB:     round1(float64(active.size) / 1024.0),
			IsActive:   true,
			IsSubagent: s.isCandidateSubagent(*active),
		}
		if len(sessions) >= maxSessions {
			sessions[len(sessions)-1] = entry
		} else {
			sessions = append(sessions, entry)
		}
	}

	return sessions
}

func (s *Server) isCandidateSubagent(c sessionCandidate) bool {
	if s.getParentID(c.id) != "" {
		return true
	}
	_, hasUser := s.getTitleMeta(c.id, c.path, c.size)
	return !hasUser
}

func (s *Server) getParentID(convID string) string {
	if convID == "" {
		return ""
	}
	s.mu.Lock()
	pid := s.parentBySubagent[convID]
	s.mu.Unlock()
	return pid
}

func (s *Server) findParentForSubagent(subID string, candidates []sessionCandidate, errOut io.Writer) string {
	const maxScanParents = 20
	checked := 0
	for _, c := range candidates {
		if c.id == subID {
			continue
		}
		_, hasUser := s.getTitleMeta(c.id, c.path, c.size)
		if !hasUser {
			continue
		}
		checked++
		_, _, _ = s.getAnalysis(c.id, c.path, c.modTime, c.size, errOut)
		if pid := s.getParentID(subID); pid != "" {
			return pid
		}
		if checked >= maxScanParents {
			break
		}
	}
	return ""
}

func (s *Server) getCachedAutoID() string {
	s.mu.Lock()
	id := s.cachedAutoID
	s.mu.Unlock()
	return id
}

func (s *Server) setCachedAutoID(id string) {
	s.mu.Lock()
	s.cachedAutoID = id
	s.mu.Unlock()
}

func (s *Server) refreshActiveCandidate(active *sessionCandidate, localCands []sessionCandidate) {
	if active == nil {
		return
	}
	for i := range localCands {
		if localCands[i].id == active.id {
			localCands[i].path = active.path
			localCands[i].modTime = active.modTime
			localCands[i].size = active.size
			break
		}
	}

	s.mu.Lock()
	updated := false
	for i := range s.cachedCandidates {
		if s.cachedCandidates[i].id == active.id {
			if !s.cachedCandidates[i].modTime.Equal(active.modTime) || s.cachedCandidates[i].size != active.size || s.cachedCandidates[i].path != active.path {
				s.cachedCandidates[i].path = active.path
				s.cachedCandidates[i].modTime = active.modTime
				s.cachedCandidates[i].size = active.size
				updated = true
			}
			break
		}
	}
	if updated {
		sortCandidates(s.cachedCandidates)
		s.sessionsSig = hashCandidates(s.cachedCandidates)
	}
	s.mu.Unlock()
}

func (s *Server) getCandidates(forceRescan bool, now time.Time) ([]sessionCandidate, uint64, bool) {
	info, err := os.Stat(s.brainDir)
	if err != nil {
		s.mu.Lock()
		if len(s.cachedCandidates) > 0 || s.sessionsSig != "" {
			s.cachedCandidates = nil
			s.sessionsSig = ""
			s.cachedAutoID = ""
			s.sessionsVersion++
		}
		s.dirScanAt = now
		s.dirModTimeNano = 0
		ver := s.sessionsVersion
		s.mu.Unlock()
		return nil, ver, false
	}

	dirMtime := info.ModTime().UnixNano()

	s.mu.Lock()
	if !forceRescan && !s.dirScanAt.IsZero() && now.Sub(s.dirScanAt) < dirScanTTL && s.dirModTimeNano == dirMtime {
		cp := make([]sessionCandidate, len(s.cachedCandidates))
		copy(cp, s.cachedCandidates)
		ver := s.sessionsVersion
		s.mu.Unlock()
		return cp, ver, true
	}
	s.mu.Unlock()

	cands := s.scanSessions()
	sig := hashCandidates(cands)

	s.mu.Lock()
	s.cachedCandidates = cands
	s.dirScanAt = now
	s.dirModTimeNano = dirMtime
	if sig != s.sessionsSig {
		s.sessionsSig = sig
		s.sessionsVersion++
		s.cachedAutoID = ""
	}
	cp := make([]sessionCandidate, len(s.cachedCandidates))
	copy(cp, s.cachedCandidates)
	ver := s.sessionsVersion
	s.mu.Unlock()
	return cp, ver, false
}

func hashCandidates(cands []sessionCandidate) string {
	h := sha256.New()
	var buf [8]byte
	for _, c := range cands {
		binary.LittleEndian.PutUint64(buf[:], uint64(len(c.id)))
		h.Write(buf[:])
		h.Write([]byte(c.id))
		binary.LittleEndian.PutUint64(buf[:], uint64(c.modTime.UnixNano()))
		h.Write(buf[:])
		binary.LittleEndian.PutUint64(buf[:], uint64(c.size))
		h.Write(buf[:])
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

func sortCandidates(out []sessionCandidate) {
	sort.Slice(out, func(i, j int) bool {
		if !out[i].modTime.Equal(out[j].modTime) {
			return out[i].modTime.After(out[j].modTime)
		}
		return out[i].id > out[j].id
	})
}

func computeRevision(
	autoMode bool,
	activeID string,
	parentID string,
	mainPath string,
	mainMtimeNano int64,
	mainSize int64,
	sessVer uint64,
	art artifactPlanStat,
	subStats []subagentStat,
) string {
	h := sha256.New()
	var buf [8]byte
	writeStr := func(v string) {
		binary.LittleEndian.PutUint64(buf[:], uint64(len(v)))
		h.Write(buf[:])
		h.Write([]byte(v))
	}
	writeInt64 := func(v int64) {
		binary.LittleEndian.PutUint64(buf[:], uint64(v))
		h.Write(buf[:])
	}

	if autoMode {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}
	writeStr(activeID)
	writeStr(parentID)
	writeStr(mainPath)
	writeInt64(mainMtimeNano)
	writeInt64(mainSize)
	writeInt64(int64(sessVer))
	writeStr(art.path)
	writeInt64(art.mtimeNano)
	writeInt64(art.size)
	writeInt64(int64(len(subStats)))
	for _, st := range subStats {
		writeStr(st.ref.ID)
		writeInt64(st.mtimeNano)
		writeInt64(st.size)
		writeStr(st.status)
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:8])
}

func (s *Server) scanSessions() []sessionCandidate {
	entries, err := os.ReadDir(s.brainDir)
	if err != nil {
		return nil
	}
	out := make([]sessionCandidate, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		if validateConversationID(id) != nil {
			continue
		}
		path, info, ok := s.resolveTranscript(id)
		if !ok {
			continue
		}
		out = append(out, sessionCandidate{
			id:      id,
			path:    path,
			modTime: info.ModTime(),
			size:    info.Size(),
		})
	}
	sortCandidates(out)
	return out
}

func (s *Server) resolveTranscript(id string) (string, os.FileInfo, bool) {
	logsDir := filepath.Join(s.brainDir, id, ".system_generated", "logs")
	rel, err := filepath.Rel(s.brainDir, logsDir)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", nil, false
	}
	for _, name := range []string{"transcript_full.jsonl", "transcript.jsonl"} {
		p := filepath.Join(logsDir, name)
		info, serr := os.Stat(p)
		if serr == nil && !info.IsDir() {
			return p, info, true
		}
	}
	return "", nil, false
}

// resolveLogURI prefers compact transcript.jsonl for user viewing, falling back to transcript_full.jsonl.
func (s *Server) resolveLogURI(id, fallbackURI string) string {
	if id == "" || validateConversationID(id) != nil {
		return fallbackURI
	}
	logsDir := filepath.Join(s.brainDir, id, ".system_generated", "logs")
	rel, err := filepath.Rel(s.brainDir, logsDir)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fallbackURI
	}
	for _, name := range []string{"transcript.jsonl", "transcript_full.jsonl"} {
		p := filepath.Join(logsDir, name)
		info, serr := os.Stat(p)
		if serr == nil && !info.IsDir() {
			if abs, aerr := filepath.Abs(p); aerr == nil {
				p = abs
			}
			return fileURI(p)
		}
	}
	return fallbackURI
}

func (s *Server) getAnalysis(convID, path string, modTime time.Time, size int64, errOut io.Writer) (*transcriptAnalysis, bool, error) {
	mtimeNano := modTime.UnixNano()

	s.mu.Lock()
	if entry, ok := s.analysisCache[path]; ok && entry.modTimeNano == mtimeNano && entry.size == size {
		s.mu.Unlock()
		return entry.analysis, true, nil
	}
	s.mu.Unlock()

	res, err := analyzeTranscriptFull(path, size, 0, errOut)
	if err != nil {
		return nil, false, err
	}

	s.mu.Lock()
	s.analysisCache[path] = analysisCacheEntry{
		modTimeNano: mtimeNano,
		size:        size,
		analysis:    res,
	}
	if convID != "" && res != nil {
		for _, ref := range res.subagentRefs {
			s.parentBySubagent[ref.ID] = convID
		}
	}
	s.mu.Unlock()
	return res, false, nil
}

func (s *Server) getTitleMeta(id, path string, size int64) (string, bool) {
	hasData := size > 0

	s.mu.Lock()
	if entry, ok := s.titleCache[id]; ok && entry.hasData == hasData {
		s.mu.Unlock()
		return entry.title, entry.hasUserInput
	}
	s.mu.Unlock()

	title := shortID(id)
	hasUser := false
	if hasData {
		if f, err := os.Open(path); err == nil {
			title, hasUser = extractTitleMetaFromReader(f, id)
			_ = f.Close()
		}
	}

	s.mu.Lock()
	s.titleCache[id] = titleCacheEntry{hasData: hasData, title: title, hasUserInput: hasUser}
	s.mu.Unlock()
	return title, hasUser
}

func extractTitleFromReader(r io.Reader, id string) string {
	title, _ := extractTitleMetaFromReader(r, id)
	return title
}

func extractTitleMetaFromReader(r io.Reader, id string) (string, bool) {
	lr := io.LimitReader(r, titleReadBytes)
	br := bufio.NewReaderSize(lr, 8*1024)

	for i := 0; i < titleMaxLines; i++ {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 && bytes.Contains(line, []byte("USER_INPUT")) {
			var row struct {
				Type    string          `json:"type"`
				Content json.RawMessage `json:"content"`
			}
			if uerr := json.Unmarshal(line, &row); uerr == nil {
				if row.Type == "USER_INPUT" {
					var text string
					if len(row.Content) > 0 && row.Content[0] == '"' {
						_ = json.Unmarshal(row.Content, &text)
					}
					return cleanUserTitle(text, id), true
				}
			} else if text, ok := extractTruncatedContent(line); ok {
				return cleanUserTitle(text, id), true
			}
		}
		if err != nil {
			break
		}
	}
	return shortID(id), false
}

// extractTruncatedContent salvages the prefix of "content" when a USER_INPUT line exceeds 64KB.
func extractTruncatedContent(line []byte) (string, bool) {
	if !bytes.Contains(line, []byte(`"USER_INPUT"`)) {
		return "", false
	}
	idx := bytes.Index(line, []byte(`"content"`))
	if idx < 0 {
		return "", false
	}
	rest := line[idx+len(`"content"`):]
	colon := bytes.IndexByte(rest, ':')
	if colon < 0 {
		return "", false
	}
	rest = bytes.TrimSpace(rest[colon+1:])
	if len(rest) == 0 || rest[0] != '"' {
		return "", false
	}
	var b strings.Builder
	escaped := false
	for i := 1; i < len(rest) && b.Len() < 2048; i++ {
		c := rest[i]
		if escaped {
			switch c {
			case 'n', 'r', 't':
				b.WriteByte(' ')
			case '"', '\\', '/':
				b.WriteByte(c)
			case 'u':
				if i+4 < len(rest) {
					var r rune
					if err := json.Unmarshal([]byte(`"\`+string(rest[i:i+5])+`"`), &r); err == nil {
						b.WriteRune(r)
					}
					i += 4
				}
			}
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			break
		}
		b.WriteByte(c)
	}
	return b.String(), b.Len() > 0
}

func cleanUserTitle(raw, id string) string {
	s := reMetaBlock.ReplaceAllString(raw, " ")
	s = reMetaOpen.ReplaceAllString(s, " ")
	s = reUserReqTags.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return shortID(id)
	}
	runes := []rune(s)
	if len(runes) > maxTitleRunes {
		return string(runes[:maxTitleRunes-1]) + "…"
	}
	return s
}

func shortID(id string) string {
	runes := []rune(id)
	if len(runes) > 8 {
		return string(runes[:8])
	}
	return id
}

// humanModel converts a model slug like "gemini-4-neon-xhigh" into "Gemini 4 Neon (XHigh)".
func humanModel(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "Unknown"
	}
	if m := reGeminiModel.FindStringSubmatch(raw); m != nil {
		ver, fam, tier := m[1], m[2], m[3]
		base := fmt.Sprintf("Gemini %s %s", ver, capitalizeWord(fam))
		if tier != "" {
			if label, ok := geminiTierNames[strings.ToLower(tier)]; ok {
				return fmt.Sprintf("%s (%s)", base, label)
			}
		}
		return base
	}
	parts := strings.Fields(strings.ReplaceAll(raw, "-", " "))
	for i, p := range parts {
		parts[i] = capitalizeWord(p)
	}
	return strings.Join(parts, " ")
}

func capitalizeWord(s string) string {
	runes := []rune(strings.ToLower(s))
	if len(runes) == 0 {
		return ""
	}
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

func defaultWindow(raw string) int {
	if strings.Contains(strings.ToLower(raw), "claude") {
		return 200000
	}
	return 1048576
}

// downsampleTimeline preserves start, end, peak, and compaction boundary points.
func downsampleTimeline(points []TimelinePoint, maxPoints int) []TimelinePoint {
	n := len(points)
	if n <= maxPoints || maxPoints <= 0 {
		out := make([]TimelinePoint, n)
		copy(out, points)
		return out
	}

	keep := make(map[int]struct{}, maxPoints+4)
	keep[0] = struct{}{}
	keep[n-1] = struct{}{}

	peakIdx := 0
	for i := 1; i < n; i++ {
		if points[i].Total > points[peakIdx].Total {
			peakIdx = i
		}
	}
	keep[peakIdx] = struct{}{}

	for i, pt := range points {
		if pt.CompactedBefore {
			keep[i] = struct{}{}
			if i > 0 {
				keep[i-1] = struct{}{}
			}
		}
	}

	remaining := maxPoints - len(keep)
	if remaining > 0 {
		step := float64(n-1) / float64(remaining+1)
		for k := 1; k <= remaining; k++ {
			idx := int(math.Round(float64(k) * step))
			if idx < 0 {
				idx = 0
			} else if idx >= n {
				idx = n - 1
			}
			keep[idx] = struct{}{}
		}
	}

	indices := make([]int, 0, len(keep))
	for idx := range keep {
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	out := make([]TimelinePoint, 0, len(indices))
	for _, idx := range indices {
		out = append(out, points[idx])
	}
	return out
}

func analyzeTranscriptFull(path string, fileSize int64, windowOverride int, errOut io.Writer) (*transcriptAnalysis, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fileSize < 0 {
		if info, serr := f.Stat(); serr == nil {
			fileSize = info.Size()
		}
	}
	return analyzeTranscriptFullReader(f, fileSize, windowOverride, errOut)
}

func analyzeTranscriptReader(r io.Reader, fileSize int64, windowOverride int, errOut io.Writer) (*Snapshot, error) {
	res, err := analyzeTranscriptFullReader(r, fileSize, windowOverride, errOut)
	if err != nil || res == nil {
		return nil, err
	}
	return res.snapshot, nil
}

func analyzeTranscriptFullReader(r io.Reader, fileSize int64, windowOverride int, errOut io.Writer) (*transcriptAnalysis, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	var rows []transcriptRow

	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				var row transcriptRow
				if uerr := json.Unmarshal(trimmed, &row); uerr != nil {
					if errOut != nil {
						fmt.Fprintf(errOut, "agy-info: skip malformed transcript line: %v\n", uerr)
					}
				} else {
					rows = append(rows, row)
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
	}

	var plannerIndices []int
	var checkpointIndices []int
	userTurns := 0
	for i := range rows {
		switch rows[i].Type {
		case "USER_INPUT":
			userTurns++
		case "PLANNER_RESPONSE":
			plannerIndices = append(plannerIndices, i)
		case "CHECKPOINT":
			checkpointIndices = append(checkpointIndices, i)
		}
	}

	if len(plannerIndices) == 0 {
		return &transcriptAnalysis{
			snapshot:     nil,
			restartStep:  -1,
			hasUserInput: userTurns > 0,
		}, nil
	}

	lastCPIdx := -1
	if len(checkpointIndices) > 0 {
		lastCPIdx = checkpointIndices[len(checkpointIndices)-1]
	}

	last := &rows[plannerIndices[len(plannerIndices)-1]]
	window := windowOverride
	if window <= 0 {
		window = defaultWindow(last.Model)
	}

	lastInput := last.InputTokens
	lastCache := last.CacheReadTokens
	lastOutput := last.OutputTokens
	lastPrompt := lastInput + lastCache
	currentTokens := lastPrompt + lastOutput

	// Active window starts strictly after the last CHECKPOINT.
	activeStart := 0
	if lastCPIdx >= 0 {
		activeStart = lastCPIdx + 1
	}
	baselineIdx := plannerIndices[0]
	for i := activeStart; i < len(rows); i++ {
		if rows[i].Type == "PLANNER_RESPONSE" {
			baselineIdx = i
			break
		}
	}
	baselineRow := &rows[baselineIdx]
	baselineTokens := baselineRow.InputTokens + baselineRow.CacheReadTokens
	growthTokens := currentTokens - baselineTokens
	if growthTokens < 0 {
		growthTokens = 0
	}

	// Whole-session counters, cumulative billed usage, subagent chronology, and plan tracking.
	toolCallsCount := 0
	subagentsCount := 0
	cumInput := 0
	cumCache := 0
	cumOutput := 0

	rawTimeline := make([]TimelinePoint, 0, len(plannerIndices))
	peakTokens := 0
	sawCP := false
	llmIdx := 0

	tracker := newSubagentTracker()
	var planSteps []PlanStep
	planFocus := ""
	planFocusUpdatedAt := ""

	for i := range rows {
		row := &rows[i]
		seq := i + 1
		rowTime, _ := parseTS(row.CreatedAt)

		switch row.Type {
		case "CHECKPOINT":
			cumInput += row.InputTokens
			cumCache += row.CacheReadTokens
			cumOutput += row.OutputTokens
			sawCP = true
		case "PLANNER_RESPONSE":
			cumInput += row.InputTokens
			cumCache += row.CacheReadTokens
			cumOutput += row.OutputTokens

			calls := parseToolCalls(row.ToolCalls)
			toolCallsCount += len(calls)
			for _, tc := range calls {
				switch tc.Name {
				case "invoke_subagent":
					subagentsCount++
				case "update_steps":
					if steps := decodeArgSteps(getArgRaw(tc.Args, "steps")); steps != nil {
						planSteps = steps
					}
				case "update_focus":
					if f := strings.TrimSpace(decodeArgString(getArgRaw(tc.Args, "focus"))); f != "" {
						planFocus = f
						planFocusUpdatedAt = row.CreatedAt
					}
				}
			}
			tracker.onPlanner(seq, rowTime, calls)

			llmIdx++
			total := row.InputTokens + row.CacheReadTokens + row.OutputTokens
			if total > peakTokens {
				peakTokens = total
			}
			rawTimeline = append(rawTimeline, TimelinePoint{
				N:               llmIdx,
				Step:            row.StepIndex,
				Total:           total,
				Cache:           row.CacheReadTokens,
				New:             row.InputTokens,
				Out:             row.OutputTokens,
				CompactedBefore: sawCP,
			})
			sawCP = false
		case "GENERIC":
			tracker.onGeneric(row.Content)
		case "SYSTEM_MESSAGE":
			tracker.onSystemMessage(seq, rowTime, row.Content)
		}
	}

	// Proportional breakdown inside the current active window starting at baselineRow.
	var toolsChars, assistantChars, userSysChars int
	pastBaseline := false
	for i := activeStart; i < len(rows); i++ {
		row := &rows[i]
		if !pastBaseline {
			if i == baselineIdx {
				pastBaseline = true
				assistantChars += rawValueRunes(row.Content) + rawValueRunes(row.Thinking) + toolCallsRunes(row.ToolCalls)
			}
			continue
		}
		switch row.Type {
		case "GENERIC":
			toolsChars += rawValueRunes(row.Content)
		case "PLANNER_RESPONSE":
			assistantChars += rawValueRunes(row.Content) + rawValueRunes(row.Thinking) + toolCallsRunes(row.ToolCalls)
		case "USER_INPUT", "SYSTEM_MESSAGE":
			userSysChars += rawValueRunes(row.Content)
		}
	}

	totalChars := toolsChars + assistantChars + userSysChars
	var toolsTok, asstTok, userTok int
	if growthTokens > 0 && totalChars > 0 {
		toolsTok = int(math.Round(float64(growthTokens) * float64(toolsChars) / float64(totalChars)))
		asstTok = int(math.Round(float64(growthTokens) * float64(assistantChars) / float64(totalChars)))
		userTok = growthTokens - toolsTok - asstTok
		if userTok < 0 {
			userTok = 0
		}
	}

	cumPrompt := cumInput + cumCache
	timeline := downsampleTimeline(rawTimeline, maxTimelinePoints)

	var durationMin *float64
	if t0, ok0 := parseTS(rows[0].CreatedAt); ok0 {
		if t1, ok1 := parseTS(rows[len(rows)-1].CreatedAt); ok1 {
			d := round1(t1.Sub(t0).Seconds() / 60.0)
			durationMin = &d
		}
	}

	compactions := len(checkpointIndices)
	fillPct := round1(float64(currentTokens) / float64(window) * 100.0)

	var status, advice string
	switch {
	case fillPct < 50:
		status = "ok"
		if compactions > 0 {
			advice = fmt.Sprintf("Context was compacted (%dx) and has plenty of headroom — /compact is not needed.", compactions)
		} else {
			advice = "Plenty of headroom left — keep going, /compact is not needed."
		}
	case fillPct < 75:
		status = "warn"
		advice = "Context window is over half full — consider /compact or a new chat before starting a large task."
	default:
		status = "high"
		advice = "Context window is nearly full — run /compact or start a new chat soon."
	}

	baselineLabel := "System, rules & startup"
	if compactions > 0 {
		baselineLabel = "System, rules & summary"
	}

	freeTokens := window - currentTokens
	if freeTokens < 0 {
		freeTokens = 0
	}

	lastCacheHit := 0.0
	if lastPrompt > 0 {
		lastCacheHit = round1(float64(lastCache) / float64(lastPrompt) * 100.0)
	}
	cumCacheHit := 0.0
	if cumPrompt > 0 {
		cumCacheHit = round1(float64(cumCache) / float64(cumPrompt) * 100.0)
	}

	snap := &Snapshot{
		Model:         humanModel(last.Model),
		WindowTokens:  window,
		CurrentTokens: currentTokens,
		FreeTokens:    freeTokens,
		PeakTokens:    peakTokens,
		Compactions:   compactions,
		FillPct:       fillPct,
		Status:        status,
		Advice:        advice,
		LastTurn: LastTurnMetrics{
			Prompt:        lastPrompt,
			UncachedInput: lastInput,
			CacheRead:     lastCache,
			Output:        lastOutput,
			CacheHitPct:   lastCacheHit,
		},
		Cumulative: CumulativeMetrics{
			PromptTotal:   cumPrompt,
			UncachedInput: cumInput,
			CacheRead:     cumCache,
			Output:        cumOutput,
			CacheHitPct:   cumCacheHit,
		},
		Breakdown: []BreakdownItem{
			{ID: "baseline", Label: baselineLabel, Tokens: baselineTokens},
			{ID: "tools", Label: "Tool outputs", Tokens: toolsTok},
			{ID: "assistant", Label: "Assistant & thinking", Tokens: asstTok},
			{ID: "user_sys", Label: "User & subagents", Tokens: userTok},
		},
		Counts: SessionCounts{
			UserTurns:    userTurns,
			LLMSteps:     len(plannerIndices),
			ToolCalls:    toolCallsCount,
			Subagents:    subagentsCount,
			DurationMin:  durationMin,
			TranscriptKB: round1(float64(fileSize) / 1024.0),
		},
		Timeline: timeline,
	}

	return &transcriptAnalysis{
		snapshot:     snap,
		subagentRefs: tracker.refs(),
		restartStep:  tracker.restartStep,
		restartTime:  tracker.restartTime,
		plan: PlanInfo{
			Focus:          planFocus,
			FocusUpdatedAt: planFocusUpdatedAt,
			Steps:          planSteps,
		},
		hasUserInput: userTurns > 0,
	}, nil
}

func rawValueRunes(raw json.RawMessage) int {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return utf8.RuneCountInString(s)
		}
	}
	return utf8.RuneCount(trimmed)
}

func toolCallsRunes(raw json.RawMessage) int {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 2
	}
	return utf8.RuneCount(trimmed)
}

func parseTS(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func round1(v float64) float64 {
	r := math.Round(v*10) / 10
	if r == 0 {
		return 0
	}
	return r
}

func formatSummary(p *SnapshotPayload) string {
	if p == nil || p.Snapshot == nil {
		if p != nil && p.ConversationID != "" {
			return fmt.Sprintf("Session %s not found or contains no LLM steps yet.", shortID(p.ConversationID))
		}
		return "No active Antigravity sessions with context data found."
	}
	sn := p.Snapshot
	return fmt.Sprintf(
		"Context: %s / %s (%.1f%%, %s), cache %.1f%%, compactions: %d, model: %s.",
		formatTokens(sn.CurrentTokens),
		formatTokens(sn.WindowTokens),
		sn.FillPct,
		sn.Status,
		sn.LastTurn.CacheHitPct,
		sn.Compactions,
		sn.Model,
	)
}

func formatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000.0)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000.0)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func defaultBrainDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".gemini", "antigravity", "brain")
	}
	return filepath.Join(home, ".gemini", "antigravity", "brain")
}

func defaultHTMLPath() string {
	exe, err := os.Executable()
	if err != nil {
		return filepath.Join("..", "assets", "app.html")
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	return filepath.Clean(filepath.Join(filepath.Dir(exe), "..", "assets", "app.html"))
}

func main() {
	brainDir := flag.String("brain-dir", defaultBrainDir(), "Path to Antigravity brain directory")
	htmlPath := flag.String("html-path", defaultHTMLPath(), "Path to MCP app HTML file")
	flag.Parse()

	srv := NewServer(*brainDir, *htmlPath)
	if err := srv.Serve(os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "agy-info: fatal: %v\n", err)
		os.Exit(1)
	}
}
