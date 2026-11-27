package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestHumanModel(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"gemini-4-neon-xhigh", "Gemini 4 Neon (XHigh)"},
		{"gemini-2.5-pro-high", "Gemini 2.5 Pro (High)"},
		{"gemini-2.5-flash", "Gemini 2.5 Flash"},
		{"gemini-3-flash-lite", "Gemini 3 Flash (Lite)"},
		{"GEMINI-2.5-FLASH-MEDIUM", "Gemini 2.5 Flash (Medium)"},
		{"claude-sonnet-4", "Claude Sonnet 4"},
		{"", "Unknown"},
	}
	for _, tc := range cases {
		if got := humanModel(tc.in); got != tc.want {
			t.Errorf("humanModel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDefaultWindow(t *testing.T) {
	if got := defaultWindow("claude-3-7-sonnet"); got != 200000 {
		t.Fatalf("claude window = %d, want 200000", got)
	}
	if got := defaultWindow("gemini-4-neon-xhigh"); got != 1048576 {
		t.Fatalf("gemini window = %d, want 1048576", got)
	}
}

func TestFileURI(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"/tmp/session/transcript.jsonl", "file:///tmp/session/transcript.jsonl"},
		{"C:/Users/dev/.gemini/antigravity/brain/s1/task.md", "file:///C:/Users/dev/.gemini/antigravity/brain/s1/task.md"},
		{"//fileserver/share/transcript.jsonl", "file://fileserver/share/transcript.jsonl"},
	}
	for _, tc := range cases {
		if got := fileURI(tc.in); got != tc.want {
			t.Errorf("fileURI(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	if got, ok := pathFromFileURI("file:///tmp/session/transcript.jsonl"); !ok || filepath.ToSlash(got) != "/tmp/session/transcript.jsonl" {
		t.Errorf("pathFromFileURI unix = (%q, %v)", got, ok)
	}
	if got, ok := pathFromFileURI("file:///C:/Users/dev/task.md"); !ok || filepath.ToSlash(got) != "C:/Users/dev/task.md" {
		t.Errorf("pathFromFileURI windows = (%q, %v)", got, ok)
	}
	if _, ok := pathFromFileURI("https://example.com/file"); ok {
		t.Errorf("expected pathFromFileURI to reject non-file URI")
	}
}

func TestDownsampleTimeline(t *testing.T) {
	points := make([]TimelinePoint, 120)
	for i := range points {
		points[i] = TimelinePoint{
			N:     i + 1,
			Step:  i * 2,
			Total: (i + 1) * 1000,
		}
	}
	points[40].Total = 900000 // peak
	points[70].CompactedBefore = true
	points[70].Total = 20000

	got := downsampleTimeline(points, 55)
	if len(got) > 55 {
		t.Fatalf("downsampled len = %d, want <= 55", len(got))
	}
	if got[0].N != 1 || got[len(got)-1].N != 120 {
		t.Fatalf("endpoints not preserved: first=%d last=%d", got[0].N, got[len(got)-1].N)
	}

	have := make(map[int]bool, len(got))
	for i, pt := range got {
		if i > 0 && pt.N <= got[i-1].N {
			t.Fatalf("timeline not strictly increasing at %d", i)
		}
		have[pt.N] = true
	}
	for _, required := range []int{1, 41, 70, 71, 120} {
		if !have[required] {
			t.Errorf("required point N=%d missing after downsample", required)
		}
	}
}

func TestAnalyzeTranscriptNoCompaction(t *testing.T) {
	jsonl := strings.Join([]string{
		`{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-26T10:00:00Z","content":"<USER_REQUEST>\nInspect project layout\n</USER_REQUEST>"}`,
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-26T10:00:05Z","model":"gemini-2.5-pro-high","input_tokens":10000,"cache_read_tokens":0,"output_tokens":500,"thinking":"plan","tool_calls":[{"name":"read_file"},{"name":"invoke_subagent"}]}`,
		`{"step_index":2,"type":"GENERIC","created_at":"2026-11-26T10:00:10Z","content":"` + strings.Repeat("x", 400) + `"}`,
		`{"step_index":3,"type":"PLANNER_RESPONSE","created_at":"2026-11-26T10:05:00Z","model":"gemini-2.5-pro-high","input_tokens":2000,"cache_read_tokens":18000,"output_tokens":500,"content":"done","thinking":"done","tool_calls":[]}`,
	}, "\n") + "\n"

	snap, err := analyzeTranscriptReader(strings.NewReader(jsonl), int64(len(jsonl)), 0, nil)
	if err != nil {
		t.Fatalf("analyzeTranscriptReader error: %v", err)
	}
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if snap.Model != "Gemini 2.5 Pro (High)" {
		t.Errorf("model = %q", snap.Model)
	}
	if snap.CurrentTokens != 20500 || snap.PeakTokens != 20500 {
		t.Errorf("current=%d peak=%d, want 20500", snap.CurrentTokens, snap.PeakTokens)
	}
	if snap.Compactions != 0 || snap.Status != "ok" {
		t.Errorf("compactions=%d status=%q", snap.Compactions, snap.Status)
	}
	if snap.LastTurn.Prompt != 20000 || snap.LastTurn.CacheHitPct != 90.0 {
		t.Errorf("last_turn = %+v", snap.LastTurn)
	}
	if snap.Cumulative.PromptTotal != 30000 || snap.Cumulative.Output != 1000 {
		t.Errorf("cumulative = %+v", snap.Cumulative)
	}
	if snap.Counts.UserTurns != 1 || snap.Counts.LLMSteps != 2 || snap.Counts.ToolCalls != 2 || snap.Counts.Subagents != 1 {
		t.Errorf("counts = %+v", snap.Counts)
	}
	if snap.Counts.DurationMin == nil || *snap.Counts.DurationMin != 5.0 {
		t.Errorf("duration_min = %v, want 5.0", snap.Counts.DurationMin)
	}
	if snap.Breakdown[0].ID != "baseline" || snap.Breakdown[0].Tokens != 10000 || snap.Breakdown[0].Label != "System, rules & startup" {
		t.Errorf("baseline breakdown = %+v", snap.Breakdown[0])
	}
	if snap.Breakdown[1].ID != "tools" || snap.Breakdown[1].Label != "Tool outputs" ||
		snap.Breakdown[2].ID != "assistant" || snap.Breakdown[2].Label != "Assistant & thinking" ||
		snap.Breakdown[3].ID != "user_sys" || snap.Breakdown[3].Label != "User & subagents" {
		t.Errorf("unexpected breakdown labels: %+v", snap.Breakdown)
	}
	sumBreakdown := 0
	for _, b := range snap.Breakdown {
		sumBreakdown += b.Tokens
	}
	if sumBreakdown != snap.CurrentTokens {
		t.Errorf("breakdown sum = %d, want %d", sumBreakdown, snap.CurrentTokens)
	}
}

func TestAnalyzeTranscriptWithCompaction(t *testing.T) {
	jsonl := strings.Join([]string{
		`{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-26T10:00:00Z","content":"start"}`,
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-26T10:01:00Z","model":"gemini-4-neon-xhigh","input_tokens":500000,"cache_read_tokens":0,"output_tokens":2000,"content":"big"}`,
		`{"step_index":2,"type":"GENERIC","created_at":"2026-11-26T10:02:00Z","content":"` + strings.Repeat("old", 10000) + `"}`,
		`{"step_index":3,"type":"CHECKPOINT","created_at":"2026-11-26T10:03:00Z","input_tokens":50000,"cache_read_tokens":450000,"output_tokens":3000}`,
		`{"step_index":4,"type":"PLANNER_RESPONSE","created_at":"2026-11-26T10:04:00Z","model":"gemini-4-neon-xhigh","input_tokens":20000,"cache_read_tokens":0,"output_tokens":500,"content":"post-cp"}`,
		`{"step_index":5,"type":"GENERIC","created_at":"2026-11-26T10:05:00Z","content":"` + strings.Repeat("t", 1000) + `"}`,
		`{"step_index":6,"type":"PLANNER_RESPONSE","created_at":"2026-11-26T10:06:00Z","model":"gemini-4-neon-xhigh","input_tokens":5000,"cache_read_tokens":25000,"output_tokens":1000,"content":"after"}`,
	}, "\n") + "\n"

	snap, err := analyzeTranscriptReader(strings.NewReader(jsonl), int64(len(jsonl)), 0, nil)
	if err != nil {
		t.Fatalf("analyzeTranscriptReader error: %v", err)
	}
	if snap.Compactions != 1 {
		t.Fatalf("compactions = %d, want 1", snap.Compactions)
	}
	if snap.PeakTokens != 502000 {
		t.Errorf("peak = %d, want 502000", snap.PeakTokens)
	}
	if snap.CurrentTokens != 31000 {
		t.Errorf("current = %d, want 31000", snap.CurrentTokens)
	}
	// Baseline must come from post-CHECKPOINT planner (20000), not pre-CHECKPOINT (500000).
	if snap.Breakdown[0].Tokens != 20000 || snap.Breakdown[0].Label != "System, rules & summary" {
		t.Errorf("baseline = %+v, want 20000 post-checkpoint", snap.Breakdown[0])
	}
	// Cumulative includes CHECKPOINT billed tokens too: 500k + 500k + 20k + 30k = 1050k.
	if snap.Cumulative.PromptTotal != 1050000 || snap.Cumulative.Output != 6500 {
		t.Errorf("cumulative = %+v", snap.Cumulative)
	}
	if len(snap.Timeline) != 3 || !snap.Timeline[1].CompactedBefore || snap.Timeline[0].CompactedBefore {
		t.Errorf("unexpected timeline compaction flags: %+v", snap.Timeline)
	}
}

func TestValidateConversationID(t *testing.T) {
	valid := []string{"", "auto", "a1b2c3d4-e5f6-7890-abcd-ef1234567890", "session_123"}
	for _, id := range valid {
		if err := validateConversationID(id); err != nil {
			t.Errorf("validateConversationID(%q) unexpected err: %v", id, err)
		}
	}
	invalid := []string{"../etc/passwd", "..", ".", "a/b", `a\b`, "abc/../def", "id\x00x"}
	for _, id := range invalid {
		if err := validateConversationID(id); err == nil {
			t.Errorf("validateConversationID(%q) expected error, got nil", id)
		}
	}
}

func TestTitleExtractionAndTruncation(t *testing.T) {
	longText := strings.Repeat("Проверка ", 20)
	jsonl := fmt.Sprintf(`{"type":"USER_INPUT","content":"<USER_REQUEST>\n  %s \n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nsecret meta\n</ADDITIONAL_METADATA>"}`+"\n", longText)
	got := extractTitleFromReader(strings.NewReader(jsonl), "12345678-aaaa-bbbb")
	if strings.Contains(got, "USER_REQUEST") || strings.Contains(got, "ADDITIONAL_METADATA") || strings.Contains(got, "secret") {
		t.Errorf("title leaked tags/metadata: %q", got)
	}
	if utf8.RuneCountInString(got) > maxTitleRunes {
		t.Errorf("title rune length = %d, want <= %d", utf8.RuneCountInString(got), maxTitleRunes)
	}

	// Fallback to short ID when no USER_INPUT exists.
	noUser := `{"type":"SYSTEM_MESSAGE","content":"hi"}` + "\n"
	if fallback := extractTitleFromReader(strings.NewReader(noUser), "abcdef12-3456-7890"); fallback != "abcdef12" {
		t.Errorf("fallback title = %q, want abcdef12", fallback)
	}
}

func TestCacheAndEndToEndJSONRPC(t *testing.T) {
	tmp := t.TempDir()
	brainDir := filepath.Join(tmp, "brain")
	htmlPath := filepath.Join(tmp, "app.html")
	if err := os.WriteFile(htmlPath, []byte("<!doctype html><html><body>OK</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	sessID := "11111111-2222-3333-4444-555555555555"
	logDir := filepath.Join(brainDir, sessID, ".system_generated", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(logDir, "transcript_full.jsonl")
	transcriptBody := strings.Join([]string{
		`{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-26T12:00:00Z","content":"<USER_REQUEST>\nHelp configure the server\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nmeta\n</ADDITIONAL_METADATA>"}`,
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-26T12:01:00Z","model":"gemini-4-neon-xhigh","input_tokens":15000,"cache_read_tokens":45000,"output_tokens":500,"content":"ok"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(transcriptPath, []byte(transcriptBody), 0o644); err != nil {
		t.Fatal(err)
	}
	fixedTime := time.Date(2026, 11, 26, 12, 1, 0, 0, time.UTC)
	_ = os.Chtimes(transcriptPath, fixedTime, fixedTime)

	requests := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":4,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":5,"method":"resources/read","params":{"uri":"ui://agy-info/app.html"}}`,
		`{"jsonrpc":"2.0","id":6,"method":"resources/read","params":{"uri":"ui://wrong"}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"get_context_snapshot","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"get_context_snapshot","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"open_agy_info","arguments":{"conversation_id":"../etc/passwd"}}}`,
	}, "\n") + "\n"

	srv := NewServer(brainDir, htmlPath)
	var outBuf, errBuf bytes.Buffer
	if err := srv.Serve(strings.NewReader(requests), &outBuf, &errBuf); err != nil {
		t.Fatalf("Serve error: %v (stderr=%s)", err, errBuf.String())
	}

	lines := strings.Split(strings.TrimSpace(outBuf.String()), "\n")
	// 10 requests minus 1 notification = 9 responses.
	if len(lines) != 9 {
		t.Fatalf("response count = %d, want 9\noutput:\n%s", len(lines), outBuf.String())
	}

	var resp map[string]any
	// 1) initialize
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatal(err)
	}
	res := resp["result"].(map[string]any)
	if res["protocolVersion"] != "2025-06-18" {
		t.Errorf("initialize protocolVersion = %v", res["protocolVersion"])
	}
	sInfo := res["serverInfo"].(map[string]any)
	if sInfo["name"] != "agy-info" || sInfo["version"] != "1.0.0" {
		t.Errorf("unexpected serverInfo = %v", sInfo)
	}

	// 2) ping -> id 2
	resp = nil
	_ = json.Unmarshal([]byte(lines[1]), &resp)
	if resp["id"].(float64) != 2 {
		t.Errorf("expected id=2 for ping (notification must be silent), got %v", resp["id"])
	}

	// 3) tools/list -> 2 tools
	resp = nil
	_ = json.Unmarshal([]byte(lines[2]), &resp)
	tools := resp["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools count = %d, want 2", len(tools))
	}
	tool0 := tools[0].(map[string]any)
	if tool0["name"] != "open_agy_info" || tool0["title"] != "AGY Info" {
		t.Errorf("unexpected primary tool: %v", tool0)
	}

	// 4) resources/list -> 1 resource
	resp = nil
	_ = json.Unmarshal([]byte(lines[3]), &resp)
	resources := resp["result"].(map[string]any)["resources"].([]any)
	if len(resources) != 1 {
		t.Fatalf("resources count = %d, want 1", len(resources))
	}
	res0 := resources[0].(map[string]any)
	if res0["uri"] != "ui://agy-info/app.html" || res0["name"] != "AGY Info UI" {
		t.Errorf("unexpected resource: %v", res0)
	}

	// 5) resources/read valid
	resp = nil
	_ = json.Unmarshal([]byte(lines[4]), &resp)
	contents := resp["result"].(map[string]any)["contents"].([]any)
	if !strings.Contains(contents[0].(map[string]any)["text"].(string), "<body>OK</body>") {
		t.Errorf("unexpected html content: %v", contents[0])
	}

	// 6) resources/read invalid -> -32002
	resp = nil
	_ = json.Unmarshal([]byte(lines[5]), &resp)
	errObj := resp["error"].(map[string]any)
	if int(errObj["code"].(float64)) != -32002 {
		t.Errorf("wrong URI error code = %v, want -32002", errObj["code"])
	}

	// 7) tools/call 1st -> from_cache: false
	var call1 struct {
		Result toolCallResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[6]), &call1); err != nil {
		t.Fatal(err)
	}
	if call1.Result.IsError || call1.Result.StructuredContent == nil || call1.Result.StructuredContent.Snapshot == nil {
		t.Fatalf("unexpected call1 result: %+v", call1.Result)
	}
	if call1.Result.StructuredContent.FromCache {
		t.Errorf("call1 from_cache = true, want false")
	}
	if !call1.Result.StructuredContent.AutoSelected || call1.Result.StructuredContent.ConversationID != sessID {
		t.Errorf("call1 selection mismatch: %+v", call1.Result.StructuredContent)
	}
	if call1.Result.StructuredContent.Sessions[0].Title != "Help configure the server" {
		t.Errorf("unexpected title: %q", call1.Result.StructuredContent.Sessions[0].Title)
	}
	wantSummary := "Context: 60.5K / 1.05M (5.8%, ok), cache 75.0%, compactions: 0, model: Gemini 4 Neon (XHigh)."
	if len(call1.Result.Content) != 1 || call1.Result.Content[0].Text != wantSummary {
		t.Errorf("summary = %+v, want %q", call1.Result.Content, wantSummary)
	}

	// 8) tools/call 2nd -> from_cache: true
	var call2 struct {
		Result toolCallResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[7]), &call2); err != nil {
		t.Fatal(err)
	}
	if !call2.Result.StructuredContent.FromCache {
		t.Errorf("call2 from_cache = false, want true")
	}

	// 9) path traversal rejected with -32602
	resp = nil
	_ = json.Unmarshal([]byte(lines[8]), &resp)
	errObj = resp["error"].(map[string]any)
	if int(errObj["code"].(float64)) != -32602 {
		t.Errorf("traversal error code = %v, want -32602", errObj["code"])
	}
}

func TestAutoSelectPrefersUserSessionOverSubagent(t *testing.T) {
	tmp := t.TempDir()
	brainDir := filepath.Join(tmp, "brain")

	writeSess := func(id string, body string, mtime time.Time) {
		dir := filepath.Join(brainDir, id, ".system_generated", "logs")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "transcript_full.jsonl")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, mtime, mtime)
	}

	tOlder := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	tNewer := time.Date(2026, 11, 26, 12, 5, 0, 0, time.UTC)

	writeSess("user-sess-1111", strings.Join([]string{
		`{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-26T12:00:00Z","content":"main task"}`,
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-26T12:00:05Z","model":"gemini-4-neon-xhigh","input_tokens":10000,"cache_read_tokens":20000,"output_tokens":300}`,
	}, "\n")+"\n", tOlder)

	// Subagent session is newer, has PLANNER_RESPONSE, but zero USER_INPUT.
	writeSess("subagent-2222", strings.Join([]string{
		`{"step_index":0,"type":"SYSTEM_MESSAGE","created_at":"2026-11-26T12:05:00Z","content":"subagent prompt"}`,
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-26T12:05:05Z","model":"gemini-4-neon-xhigh","input_tokens":5000,"cache_read_tokens":0,"output_tokens":100}`,
	}, "\n")+"\n", tNewer)

	srv := NewServer(brainDir, filepath.Join(tmp, "app.html"))
	var errBuf bytes.Buffer
	p, err := srv.buildPayload("", &errBuf)
	if err != nil {
		t.Fatalf("buildPayload error: %v", err)
	}
	if !p.AutoSelected || p.ConversationID != "user-sess-1111" {
		t.Fatalf("expected user-sess-1111, got %+v", p)
	}
}

func TestEmptyBrainDirGraceful(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(filepath.Join(tmp, "nonexistent-brain"), filepath.Join(tmp, "app.html"))

	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_context_snapshot"}}` + "\n"
	var outBuf, errBuf bytes.Buffer
	if err := srv.Serve(strings.NewReader(req), &outBuf, &errBuf); err != nil {
		t.Fatalf("Serve error: %v", err)
	}

	var resp struct {
		Result toolCallResult `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(outBuf.Bytes()), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Result.IsError {
		t.Errorf("expected isError=false on empty brain dir")
	}
	if resp.Result.StructuredContent == nil || resp.Result.StructuredContent.Snapshot != nil {
		t.Errorf("expected nil snapshot, got %+v", resp.Result.StructuredContent)
	}
	if resp.Result.StructuredContent.Sessions == nil || len(resp.Result.StructuredContent.Sessions) != 0 {
		t.Errorf("expected non-nil empty sessions slice, got %+v", resp.Result.StructuredContent.Sessions)
	}
	if resp.Result.StructuredContent.Subagents == nil || len(resp.Result.StructuredContent.Subagents) != 0 {
		t.Errorf("expected non-nil empty subagents slice, got %+v", resp.Result.StructuredContent.Subagents)
	}
	if resp.Result.StructuredContent.Plan.Source != "none" || resp.Result.StructuredContent.Plan.Steps == nil {
		t.Errorf("expected plan source=none and non-nil steps, got %+v", resp.Result.StructuredContent.Plan)
	}
}

func writeTestTranscript(t *testing.T, brainDir, id, filename, body string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(brainDir, id, ".system_generated", "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, filename)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		_ = os.Chtimes(p, mtime, mtime)
	}
	return p
}

func TestSubagentParallelInvokeAndCompactArgs(t *testing.T) {
	t.Setenv("ANTIGRAVITY_LS_ADDRESS", "")
	t.Setenv("ANTIGRAVITY_CSRF_TOKEN", "")

	tmp := t.TempDir()
	brainDir := filepath.Join(tmp, "brain")
	now := time.Date(2026, 11, 27, 16, 0, 0, 0, time.UTC)

	parentID := "aaaa1111-0000-0000-0000-000000000001"
	sub1ID := "bbbb2222-0000-0000-0000-000000000002"
	sub2ID := "cccc3333-0000-0000-0000-000000000003"
	sub3ID := "dddd4444-0000-0000-0000-000000000004"

	parentLines := []string{
		`{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-27T15:55:00Z","content":"Run parallel agents"}`,
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-27T15:55:05Z","model":"gemini-4-neon-xhigh","input_tokens":10000,"cache_read_tokens":0,"output_tokens":300,"tool_calls":[{"name":"invoke_subagent","args":{"Role":"Code Researcher","TypeName":"research","Model":"flash"}},{"name":"invoke_subagent","args":{"role":"UI Builder","typename":"self","model":"inherit"}}]}`,
		`{"step_index":2,"type":"GENERIC","created_at":"2026-11-27T15:55:06Z","content":"Created At: 2026-11-27T15:55:06Z\nCreated the following subagent:\n{\n  \"conversationId\": \"` + sub1ID + `\",\n  \"logAbsoluteUri\": \"file:///tmp/fallback1\"\n}\nTrailing text."}`,
		`{"step_index":3,"type":"GENERIC","created_at":"2026-11-27T15:55:07Z","content":"Created the following subagent:\n{\n  \"conversationId\": \"` + sub2ID + `\"\n}"}`,
		`{"step_index":4,"type":"SYSTEM_MESSAGE","created_at":"2026-11-27T15:56:00Z","content":"[Message] sender=` + sub1ID + ` content=Research complete"}`,
		`{"step_index":5,"type":"PLANNER_RESPONSE","created_at":"2026-11-27T15:57:00Z","model":"gemini-4-neon-xhigh","input_tokens":1000,"cache_read_tokens":12000,"output_tokens":200,"tool_calls":[{"name":"run_command","args":{"CommandLine":"echo log"}}]}`,
		// Unrelated run_command output containing fake marker must be ignored because pendingInvokes is empty.
		`{"step_index":6,"type":"GENERIC","created_at":"2026-11-27T15:57:01Z","content":"Command exited with code 0.\nCreated the following subagent:\n{\"conversationId\":\"eeeeeeee-0000-0000-0000-000000000099\"}"}`,
		// Compact double-encoded args for sub3 (no transcript file yet, recently launched).
		`{"step_index":7,"type":"PLANNER_RESPONSE","created_at":"2026-11-27T15:59:30Z","model":"gemini-4-neon-xhigh","input_tokens":1000,"cache_read_tokens":14000,"output_tokens":200,"tool_calls":[{"name":"invoke_subagent","args":{"Role":"\"Compact Worker\"","TypeName":"\"self\"","Model":"\"pro\""}}]}`,
		`{"step_index":8,"type":"GENERIC","created_at":"2026-11-27T15:59:31Z","content":"Created the following subagent:\n{\"conversationId\":\"` + sub3ID + `\"}"}`,
	}
	writeTestTranscript(t, brainDir, parentID, "transcript_full.jsonl", strings.Join(parentLines, "\n")+"\n", now.Add(-10*time.Second))
	writeTestTranscript(t, brainDir, parentID, "transcript.jsonl", strings.Join(parentLines, "\n")+"\n", now.Add(-10*time.Second))

	// sub1: done, has update_focus + read_file -> Focus set, LastTool empty.
	sub1Lines := []string{
		`{"step_index":0,"type":"SYSTEM_MESSAGE","created_at":"2026-11-27T15:55:06Z","content":"task"}`,
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-27T15:55:10Z","model":"gemini-2.5-flash","input_tokens":8000,"cache_read_tokens":4000,"output_tokens":500,"tool_calls":[{"name":"update_focus","args":{"focus":"\"Scanning repository files\""}},{"name":"read_file","args":{}}]}`,
	}
	writeTestTranscript(t, brainDir, sub1ID, "transcript_full.jsonl", strings.Join(sub1Lines, "\n")+"\n", now.Add(-4*time.Minute))
	sub1Compact := writeTestTranscript(t, brainDir, sub1ID, "transcript.jsonl", strings.Join(sub1Lines, "\n")+"\n", now.Add(-4*time.Minute))

	// sub2: running (updated 20s ago), no update_focus -> Focus empty, LastTool set to run_command.
	sub2Lines := []string{
		`{"step_index":0,"type":"SYSTEM_MESSAGE","created_at":"2026-11-27T15:55:07Z","content":"task"}`,
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-27T15:59:40Z","model":"gemini-4-neon-xhigh","input_tokens":20000,"cache_read_tokens":30000,"output_tokens":1000,"tool_calls":[{"name":"run_command","args":{"CommandLine":"npm run build"}}]}`,
	}
	writeTestTranscript(t, brainDir, sub2ID, "transcript_full.jsonl", strings.Join(sub2Lines, "\n")+"\n", now.Add(-20*time.Second))

	srv := NewServer(brainDir, filepath.Join(tmp, "app.html"))
	srv.nowFunc = func() time.Time { return now }

	var errBuf bytes.Buffer
	p, err := srv.buildPayload("", &errBuf)
	if err != nil {
		t.Fatalf("buildPayload error: %v", err)
	}
	if len(p.Subagents) != 3 {
		t.Fatalf("subagents len = %d, want 3: %+v", len(p.Subagents), p.Subagents)
	}
	// Running subagents must appear before done ones.
	if p.Subagents[0].Status != "running" || p.Subagents[1].Status != "running" || p.Subagents[2].Status != "done" {
		t.Fatalf("unexpected status order: %+v", p.Subagents)
	}
	if p.Subagents[0].ID != sub2ID || p.Subagents[0].Role != "UI Builder" || p.Subagents[0].Focus != "" || p.Subagents[0].LastTool != "run_command" {
		t.Errorf("sub2 mismatch: %+v", p.Subagents[0])
	}
	if p.Subagents[0].CurrentTokens != 51000 || p.Subagents[0].Model != "Gemini 4 Neon (XHigh)" || p.Subagents[0].LLMSteps != 1 || p.Subagents[0].ToolCalls != 1 {
		t.Errorf("sub2 metrics mismatch: %+v", p.Subagents[0])
	}
	if p.Subagents[1].ID != sub3ID || p.Subagents[1].Role != "Compact Worker" || p.Subagents[1].Model != "pro" || p.Subagents[1].LastTool != "" {
		t.Errorf("sub3 mismatch: %+v", p.Subagents[1])
	}
	if p.Subagents[2].ID != sub1ID || p.Subagents[2].Role != "Code Researcher" || p.Subagents[2].Focus != "Scanning repository files" || p.Subagents[2].LastTool != "" {
		t.Errorf("sub1 mismatch: %+v", p.Subagents[2])
	}
	if p.Subagents[2].LogURI != fileURI(sub1Compact) {
		t.Errorf("sub1 log_uri = %q, want %q", p.Subagents[2].LogURI, fileURI(sub1Compact))
	}
	if !strings.HasSuffix(p.LogURI, "/transcript.jsonl") {
		t.Errorf("parent log_uri = %q, want transcript.jsonl", p.LogURI)
	}
}

func TestSubagentSendMessageKillAndServerRestart(t *testing.T) {
	t.Setenv("ANTIGRAVITY_LS_ADDRESS", "")
	t.Setenv("ANTIGRAVITY_CSRF_TOKEN", "")

	tmp := t.TempDir()
	brainDir := filepath.Join(tmp, "brain")
	now := time.Date(2026, 11, 27, 16, 10, 0, 0, time.UTC)

	parentID := "1111aaaa-0000-0000-0000-000000000001"
	subWake := "2222bbbb-0000-0000-0000-000000000002"
	subKill := "3333cccc-0000-0000-0000-000000000003"
	subRestart := "4444dddd-0000-0000-0000-000000000004"

	lines := []string{
		`{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-27T16:00:00Z","content":"manage subagents"}`,
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-27T16:00:01Z","model":"gemini-4-neon-xhigh","input_tokens":1000,"cache_read_tokens":0,"output_tokens":100,"tool_calls":[{"name":"invoke_subagent","args":{"Role":"WakeMe","TypeName":"self","Model":"inherit"}},{"name":"invoke_subagent","args":{"Role":"KillMe","TypeName":"self","Model":"inherit"}},{"name":"invoke_subagent","args":{"Role":"RestartVictim","TypeName":"self","Model":"inherit"}}]}`,
		`{"step_index":2,"type":"GENERIC","content":"Created the following subagent:\n{\"conversationId\":\"` + subWake + `\"}"}`,
		`{"step_index":3,"type":"GENERIC","content":"Created the following subagent:\n{\"conversationId\":\"` + subKill + `\"}"}`,
		`{"step_index":4,"type":"GENERIC","content":"Created the following subagent:\n{\"conversationId\":\"` + subRestart + `\"}"}`,
		`{"step_index":5,"type":"SYSTEM_MESSAGE","created_at":"2026-11-27T16:02:00Z","content":"[Message] sender=` + subWake + ` content=first reply"}`,
		`{"step_index":6,"type":"SYSTEM_MESSAGE","created_at":"2026-11-27T16:05:00Z","content":"[Message] sender=system content=[Notice] All your subagents and background tasks have been stopped due to server restart"}`,
		`{"step_index":7,"type":"PLANNER_RESPONSE","created_at":"2026-11-27T16:09:30Z","model":"gemini-4-neon-xhigh","input_tokens":500,"cache_read_tokens":1500,"output_tokens":100,"tool_calls":[{"name":"manage_subagents","args":{"Action":"kill","ConversationIds":["` + subKill + `"]}},{"name":"send_message","args":{"Recipient":"` + subWake + `","Message":"do round 2"}}]}`,
	}
	writeTestTranscript(t, brainDir, parentID, "transcript_full.jsonl", strings.Join(lines, "\n")+"\n", now)
	writeTestTranscript(t, brainDir, subWake, "transcript_full.jsonl", `{"step_index":1,"type":"PLANNER_RESPONSE","model":"gemini-4-neon-xhigh","input_tokens":100,"output_tokens":10}`+"\n", now.Add(-15*time.Second))
	writeTestTranscript(t, brainDir, subKill, "transcript_full.jsonl", `{"step_index":1,"type":"PLANNER_RESPONSE","model":"gemini-4-neon-xhigh","input_tokens":100,"output_tokens":10}`+"\n", now.Add(-10*time.Second))
	writeTestTranscript(t, brainDir, subRestart, "transcript_full.jsonl", `{"step_index":1,"type":"PLANNER_RESPONSE","model":"gemini-4-neon-xhigh","input_tokens":100,"output_tokens":10}`+"\n", now.Add(-6*time.Minute))

	srv := NewServer(brainDir, filepath.Join(tmp, "app.html"))
	srv.nowFunc = func() time.Time { return now }

	var errBuf bytes.Buffer
	p, err := srv.buildPayload("", &errBuf)
	if err != nil {
		t.Fatal(err)
	}
	statusByID := make(map[string]string)
	for _, sa := range p.Subagents {
		statusByID[sa.ID] = sa.Status
	}
	if statusByID[subWake] != "running" {
		t.Errorf("subWake status = %q, want running", statusByID[subWake])
	}
	if statusByID[subKill] != "stopped" {
		t.Errorf("subKill status = %q, want stopped", statusByID[subKill])
	}
	if statusByID[subRestart] != "stopped" {
		t.Errorf("subRestart status = %q, want stopped", statusByID[subRestart])
	}

	// Verify kill_all stops remaining running subagents too.
	lines = append(lines, `{"step_index":8,"type":"PLANNER_RESPONSE","created_at":"2026-11-27T16:09:50Z","model":"gemini-4-neon-xhigh","input_tokens":500,"cache_read_tokens":2000,"output_tokens":50,"tool_calls":[{"name":"manage_subagents","args":{"Action":"kill_all"}}]}`)
	writeTestTranscript(t, brainDir, parentID, "transcript_full.jsonl", strings.Join(lines, "\n")+"\n", now.Add(time.Second))

	p2, err := srv.buildPayload("", &errBuf)
	if err != nil {
		t.Fatal(err)
	}
	for _, sa := range p2.Subagents {
		if sa.Status != "stopped" {
			t.Errorf("after kill_all, %s status = %q, want stopped", sa.ID, sa.Status)
		}
	}
}

func TestSubagentIdleTimeoutFlipsRevision(t *testing.T) {
	t.Setenv("ANTIGRAVITY_LS_ADDRESS", "")
	t.Setenv("ANTIGRAVITY_CSRF_TOKEN", "")

	tmp := t.TempDir()
	brainDir := filepath.Join(tmp, "brain")
	curTime := time.Date(2026, 11, 27, 16, 0, 0, 0, time.UTC)

	parentID := "aaaa0000-1111-2222-3333-444455556666"
	subID := "bbbb0000-1111-2222-3333-444455556666"

	parentLines := []string{
		`{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-27T15:59:00Z","content":"watch timeout"}`,
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-27T15:59:10Z","model":"gemini-4-neon-xhigh","input_tokens":1000,"output_tokens":50,"tool_calls":[{"name":"invoke_subagent","args":{"Role":"Worker","TypeName":"self","Model":"inherit"}}]}`,
		`{"step_index":2,"type":"GENERIC","content":"Created the following subagent:\n{\"conversationId\":\"` + subID + `\"}"}`,
	}
	writeTestTranscript(t, brainDir, parentID, "transcript_full.jsonl", strings.Join(parentLines, "\n")+"\n", curTime)
	writeTestTranscript(t, brainDir, subID, "transcript_full.jsonl", `{"step_index":1,"type":"PLANNER_RESPONSE","model":"gemini-4-neon-xhigh","input_tokens":500,"output_tokens":50}`+"\n", curTime)

	srv := NewServer(brainDir, filepath.Join(tmp, "app.html"))
	srv.nowFunc = func() time.Time { return curTime }

	var errBuf bytes.Buffer
	p1, err := srv.buildPayloadWithOptions(payloadOptions{AllowUnchanged: true}, &errBuf)
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Subagents) != 1 || p1.Subagents[0].Status != "running" {
		t.Fatalf("expected running subagent, got %+v", p1.Subagents)
	}

	// 10s later -> still running -> unchanged: true
	curTime = curTime.Add(10 * time.Second)
	p2, err := srv.buildPayloadWithOptions(payloadOptions{LastRevision: p1.Revision, AllowUnchanged: true}, &errBuf)
	if err != nil {
		t.Fatal(err)
	}
	if !p2.Unchanged || p2.Revision != p1.Revision {
		t.Fatalf("expected unchanged=true within idle timeout, got %+v", p2)
	}

	// 130s after start (> 120s) -> flips to idle -> revision changes -> full payload
	curTime = curTime.Add(120 * time.Second)
	p3, err := srv.buildPayloadWithOptions(payloadOptions{LastRevision: p1.Revision, AllowUnchanged: true}, &errBuf)
	if err != nil {
		t.Fatal(err)
	}
	if p3.Unchanged {
		t.Fatalf("expected unchanged=false after running->idle transition")
	}
	if p3.Revision == p1.Revision {
		t.Errorf("expected revision to change after running->idle transition")
	}
	if len(p3.Subagents) != 1 || p3.Subagents[0].Status != "idle" {
		t.Errorf("expected idle status, got %+v", p3.Subagents)
	}
}

func TestPlanUpdateStepsFocusAndMarkdownFallback(t *testing.T) {
	t.Setenv("ANTIGRAVITY_LS_ADDRESS", "")
	t.Setenv("ANTIGRAVITY_CSRF_TOKEN", "")

	tmp := t.TempDir()
	brainDir := filepath.Join(tmp, "brain")
	now := time.Date(2026, 11, 27, 16, 0, 0, 0, time.UTC)

	// 1) Transcript with update_steps (double-encoded compact format) + update_focus
	s1 := "plan-0001-0000-0000-000000000001"
	s1Lines := []string{
		`{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-27T15:50:00Z","content":"plan test"}`,
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-27T15:51:00Z","model":"gemini-4-neon-xhigh","input_tokens":1000,"output_tokens":50,"tool_calls":[{"name":"update_steps","args":{"steps":"[{\"title\":\"Step A\",\"status\":\"done\"},{\"title\":\"Step B\",\"status\":\"in_progress\"},{\"title\":\"Step C\",\"status\":\"pending\"}]"}},{"name":"update_focus","args":{"focus":"\"Working on Step B\""}}]}`,
	}
	writeTestTranscript(t, brainDir, s1, "transcript.jsonl", strings.Join(s1Lines, "\n")+"\n", now)

	// 2) Transcript with NO update_steps, falls back to root task.md checklist
	s2 := "plan-0002-0000-0000-000000000002"
	s2Lines := []string{
		`{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-27T15:50:00Z","content":"artifact plan test"}`,
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-27T15:51:00Z","model":"gemini-4-neon-xhigh","input_tokens":1000,"output_tokens":50,"tool_calls":[{"name":"update_focus","args":{"focus":"Reviewing checklist"}}]}`,
	}
	writeTestTranscript(t, brainDir, s2, "transcript_full.jsonl", strings.Join(s2Lines, "\n")+"\n", now.Add(-time.Minute))
	taskMD := strings.Join([]string{
		"# Task",
		"- [x] First done",
		"- [/] Second active",
		"* [ ] Third pending",
		"```markdown",
		"- [ ] Inside code fence must be skipped",
		"```",
		"- [X] Fourth uppercase done",
	}, "\n") + "\n"
	taskPath := filepath.Join(brainDir, s2, "task.md")
	if err := os.WriteFile(taskPath, []byte(taskMD), 0o644); err != nil {
		t.Fatal(err)
	}

	// 3) Transcript with only update_focus and nested subdir/task.md (must not recurse)
	s3 := "plan-0003-0000-0000-000000000003"
	s3Lines := []string{
		`{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-27T15:50:00Z","content":"focus only"}`,
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-27T15:52:00Z","model":"gemini-4-neon-xhigh","input_tokens":1000,"output_tokens":50,"tool_calls":[{"name":"update_focus","args":{"focus":"Just investigating"}}]}`,
	}
	writeTestTranscript(t, brainDir, s3, "transcript_full.jsonl", strings.Join(s3Lines, "\n")+"\n", now.Add(-2*time.Minute))
	nestedDir := filepath.Join(brainDir, s3, "nested")
	_ = os.MkdirAll(nestedDir, 0o755)
	_ = os.WriteFile(filepath.Join(nestedDir, "task.md"), []byte("- [ ] Nested must be ignored\n"), 0o644)

	srv := NewServer(brainDir, filepath.Join(tmp, "app.html"))
	srv.nowFunc = func() time.Time { return now }
	var errBuf bytes.Buffer

	p1, err := srv.buildPayload(s1, &errBuf)
	if err != nil {
		t.Fatal(err)
	}
	if p1.Plan.Source != "update_steps" || p1.Plan.DoneCount != 1 || p1.Plan.TotalCount != 3 || p1.Plan.Focus != "Working on Step B" || p1.Plan.FocusUpdatedAt != "2026-11-27T15:51:00Z" {
		t.Errorf("s1 plan mismatch: %+v", p1.Plan)
	}

	p2, err := srv.buildPayload(s2, &errBuf)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Plan.Source != "artifact" || p2.Plan.DoneCount != 2 || p2.Plan.TotalCount != 4 || p2.Plan.ArtifactURI != fileURI(taskPath) {
		t.Errorf("s2 plan mismatch: %+v", p2.Plan)
	}
	if p2.Plan.Steps[1].Status != "in_progress" || p2.Plan.Steps[3].Title != "Fourth uppercase done" {
		t.Errorf("s2 steps mismatch: %+v", p2.Plan.Steps)
	}

	// Updating task.md changes revision and refreshes steps.
	if err := os.WriteFile(taskPath, []byte("- [x] Only one done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(taskPath, now.Add(time.Second), now.Add(time.Second))
	p2Updated, err := srv.buildPayloadWithOptions(payloadOptions{RequestedID: s2, LastRevision: p2.Revision, AllowUnchanged: true}, &errBuf)
	if err != nil {
		t.Fatal(err)
	}
	if p2Updated.Unchanged || p2Updated.Revision == p2.Revision || p2Updated.Plan.TotalCount != 1 {
		t.Errorf("expected task.md update to invalidate revision, got %+v", p2Updated)
	}

	p3, err := srv.buildPayload(s3, &errBuf)
	if err != nil {
		t.Fatal(err)
	}
	if p3.Plan.Source != "focus_only" || p3.Plan.ArtifactURI != "" || p3.Plan.Focus != "Just investigating" {
		t.Errorf("s3 plan mismatch: %+v", p3.Plan)
	}
}

func TestLastRevisionUnchangedForceRescanAndOpenTool(t *testing.T) {
	t.Setenv("ANTIGRAVITY_LS_ADDRESS", "")
	t.Setenv("ANTIGRAVITY_CSRF_TOKEN", "")

	tmp := t.TempDir()
	brainDir := filepath.Join(tmp, "brain")
	htmlPath := filepath.Join(tmp, "app.html")
	_ = os.WriteFile(htmlPath, []byte("<html></html>"), 0o644)

	sessID := "11111111-2222-3333-4444-555555555555"
	now := time.Date(2026, 11, 27, 16, 0, 0, 0, time.UTC)
	body := strings.Join([]string{
		`{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-27T15:59:00Z","content":"hello"}`,
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-27T15:59:05Z","model":"gemini-4-neon-xhigh","input_tokens":10000,"cache_read_tokens":20000,"output_tokens":300}`,
	}, "\n") + "\n"
	writeTestTranscript(t, brainDir, sessID, "transcript_full.jsonl", body, now)

	srv := NewServer(brainDir, htmlPath)
	srv.nowFunc = func() time.Time { return now }

	// Step 1: initial full call
	var out1, errBuf bytes.Buffer
	req1 := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_context_snapshot","arguments":{}}}` + "\n"
	if err := srv.Serve(strings.NewReader(req1), &out1, &errBuf); err != nil {
		t.Fatal(err)
	}
	var res1 struct {
		Result toolCallResult `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out1.Bytes()), &res1); err != nil {
		t.Fatal(err)
	}
	rev := res1.Result.StructuredContent.Revision
	if rev == "" || res1.Result.StructuredContent.Unchanged {
		t.Fatalf("expected non-empty revision and unchanged=false, got %+v", res1.Result.StructuredContent)
	}

	// Step 2: polling with matching last_revision -> minimal unchanged response < 250 bytes
	var out2 bytes.Buffer
	req2 := fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_context_snapshot","arguments":{"last_revision":%q}}}`+"\n", rev)
	if err := srv.Serve(strings.NewReader(req2), &out2, &errBuf); err != nil {
		t.Fatal(err)
	}
	rawLine2 := bytes.TrimSpace(out2.Bytes())
	if len(rawLine2) >= 250 {
		t.Errorf("unchanged response too large: %d bytes (%s)", len(rawLine2), string(rawLine2))
	}
	var rawMap2 map[string]any
	if err := json.Unmarshal(rawLine2, &rawMap2); err != nil {
		t.Fatal(err)
	}
	sc2 := rawMap2["result"].(map[string]any)["structuredContent"].(map[string]any)
	if sc2["unchanged"] != true || sc2["from_cache"] != true || sc2["revision"] != rev || sc2["conversation_id"] != sessID {
		t.Errorf("unexpected unchanged structuredContent: %v", sc2)
	}
	for _, forbidden := range []string{"snapshot", "sessions", "subagents", "plan"} {
		if _, exists := sc2[forbidden]; exists {
			t.Errorf("unchanged response must omit %q: %v", forbidden, sc2)
		}
	}

	// Step 3: force_rescan: true bypasses unchanged short-circuit
	var out3 bytes.Buffer
	req3 := fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_context_snapshot","arguments":{"last_revision":%q,"force_rescan":true}}}`+"\n", rev)
	if err := srv.Serve(strings.NewReader(req3), &out3, &errBuf); err != nil {
		t.Fatal(err)
	}
	var res3 struct {
		Result toolCallResult `json:"result"`
	}
	_ = json.Unmarshal(bytes.TrimSpace(out3.Bytes()), &res3)
	if res3.Result.StructuredContent.Unchanged || res3.Result.StructuredContent.Snapshot == nil {
		t.Errorf("force_rescan must return full snapshot, got %+v", res3.Result.StructuredContent)
	}

	// Step 4: open_agy_info ALWAYS returns full snapshot even if last_revision matches
	var out4 bytes.Buffer
	req4 := fmt.Sprintf(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"open_agy_info","arguments":{"last_revision":%q}}}`+"\n", rev)
	if err := srv.Serve(strings.NewReader(req4), &out4, &errBuf); err != nil {
		t.Fatal(err)
	}
	var res4 struct {
		Result toolCallResult `json:"result"`
	}
	_ = json.Unmarshal(bytes.TrimSpace(out4.Bytes()), &res4)
	if res4.Result.StructuredContent.Unchanged || res4.Result.StructuredContent.Snapshot == nil {
		t.Errorf("open_agy_info must always return full snapshot, got %+v", res4.Result.StructuredContent)
	}
}

func TestParentConversationIDColdStartAndSessionsFiltering(t *testing.T) {
	t.Setenv("ANTIGRAVITY_LS_ADDRESS", "")
	t.Setenv("ANTIGRAVITY_CSRF_TOKEN", "")

	tmp := t.TempDir()
	brainDir := filepath.Join(tmp, "brain")
	now := time.Date(2026, 11, 27, 16, 0, 0, 0, time.UTC)

	parentID := "parent-1111-2222-3333-444444444444"
	subID := "child-1111-2222-3333-555555555555"

	parentLines := []string{
		`{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-27T15:50:00Z","content":"Main user conversation"}`,
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-27T15:50:05Z","model":"gemini-4-neon-xhigh","input_tokens":5000,"output_tokens":100,"tool_calls":[{"name":"invoke_subagent","args":{"Role":"Helper","TypeName":"self","Model":"inherit"}}]}`,
		`{"step_index":2,"type":"GENERIC","content":"Created the following subagent:\n{\"conversationId\":\"` + subID + `\"}"}`,
	}
	writeTestTranscript(t, brainDir, parentID, "transcript_full.jsonl", strings.Join(parentLines, "\n")+"\n", now.Add(-time.Minute))
	writeTestTranscript(t, brainDir, subID, "transcript_full.jsonl", `{"step_index":1,"type":"PLANNER_RESPONSE","model":"gemini-4-neon-xhigh","input_tokens":2000,"output_tokens":50}`+"\n", now)

	// Cold start jumping directly into child subagent must still resolve parent_conversation_id.
	srv := NewServer(brainDir, filepath.Join(tmp, "app.html"))
	srv.nowFunc = func() time.Time { return now }
	var errBuf bytes.Buffer

	pSub, err := srv.buildPayload(subID, &errBuf)
	if err != nil {
		t.Fatal(err)
	}
	if pSub.ParentConversationID != parentID {
		t.Errorf("parent_conversation_id = %q, want %q", pSub.ParentConversationID, parentID)
	}

	// Auto call must show only user sessions in dropdown (subagent hidden from sessions list).
	pMain, err := srv.buildPayload("", &errBuf)
	if err != nil {
		t.Fatal(err)
	}
	if pMain.ConversationID != parentID {
		t.Errorf("auto conversation_id = %q, want %q", pMain.ConversationID, parentID)
	}
	if len(pMain.Sessions) != 1 || pMain.Sessions[0].ID != parentID || pMain.Sessions[0].IsSubagent {
		t.Errorf("expected only user session in dropdown, got %+v", pMain.Sessions)
	}
}

func TestLiveLSRunStateBonus(t *testing.T) {
	var hits atomic.Int32
	subID := "live-1111-2222-3333-444444444444"
	parentID := "root-1111-2222-3333-444444444444"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/product.v1.ConversationService/ListConversations" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("x-antigravity-csrf-token") != "test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"conversations":[{"id":%q,"state":{"runState":"RUN_STATE_RUNNING"}}]}`, subID)
	}))
	defer ts.Close()

	t.Setenv("ANTIGRAVITY_LS_ADDRESS", strings.TrimPrefix(ts.URL, "http://"))
	t.Setenv("ANTIGRAVITY_CSRF_TOKEN", "test-token")

	tmp := t.TempDir()
	brainDir := filepath.Join(tmp, "brain")
	now := time.Date(2026, 11, 27, 16, 0, 0, 0, time.UTC)

	parentLines := []string{
		`{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-27T15:40:00Z","content":"live test"}`,
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-27T15:40:05Z","model":"gemini-4-neon-xhigh","input_tokens":1000,"output_tokens":50,"tool_calls":[{"name":"invoke_subagent","args":{"Role":"Long Thinker","TypeName":"self","Model":"inherit"}}]}`,
		`{"step_index":2,"type":"GENERIC","content":"Created the following subagent:\n{\"conversationId\":\"` + subID + `\"}"}`,
	}
	writeTestTranscript(t, brainDir, parentID, "transcript_full.jsonl", strings.Join(parentLines, "\n")+"\n", now)
	// Subagent transcript stale by 5 minutes -> would be "idle" without LS RUN_STATE_RUNNING.
	writeTestTranscript(t, brainDir, subID, "transcript_full.jsonl", `{"step_index":1,"type":"PLANNER_RESPONSE","model":"gemini-4-neon-xhigh","input_tokens":500,"output_tokens":10}`+"\n", now.Add(-5*time.Minute))

	srv := NewServer(brainDir, filepath.Join(tmp, "app.html"))
	srv.nowFunc = func() time.Time { return now }
	var errBuf bytes.Buffer

	p1, err := srv.buildPayload("", &errBuf)
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Subagents) != 1 || p1.Subagents[0].Status != "running" {
		t.Fatalf("expected LS override to running, got %+v", p1.Subagents)
	}
	// Second call within 2s must hit liveStateCache (only 1 HTTP call total).
	_, _ = srv.buildPayloadWithOptions(payloadOptions{LastRevision: p1.Revision, AllowUnchanged: true}, &errBuf)
	if hits.Load() != 1 {
		t.Errorf("expected 1 cached LS call, got %d", hits.Load())
	}
}

func TestConcurrentBuildPayloadRace(t *testing.T) {
	t.Setenv("ANTIGRAVITY_LS_ADDRESS", "")
	t.Setenv("ANTIGRAVITY_CSRF_TOKEN", "")

	tmp := t.TempDir()
	brainDir := filepath.Join(tmp, "brain")
	now := time.Date(2026, 11, 27, 16, 0, 0, 0, time.UTC)

	parentID := "conc-1111-2222-3333-444444444444"
	subID := "conc-sub1-2222-3333-555555555555"
	parentLines := []string{
		`{"step_index":0,"type":"USER_INPUT","created_at":"2026-11-27T15:59:00Z","content":"race check"}`,
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-27T15:59:05Z","model":"gemini-4-neon-xhigh","input_tokens":1000,"output_tokens":50,"tool_calls":[{"name":"invoke_subagent","args":{"Role":"Racer","TypeName":"self","Model":"inherit"}},{"name":"update_steps","args":{"steps":[{"title":"S1","status":"done"}]}}]}`,
		`{"step_index":2,"type":"GENERIC","content":"Created the following subagent:\n{\"conversationId\":\"` + subID + `\"}"}`,
	}
	writeTestTranscript(t, brainDir, parentID, "transcript_full.jsonl", strings.Join(parentLines, "\n")+"\n", now)
	writeTestTranscript(t, brainDir, subID, "transcript_full.jsonl", `{"step_index":1,"type":"PLANNER_RESPONSE","model":"gemini-4-neon-xhigh","input_tokens":500,"output_tokens":50}`+"\n", now)

	srv := NewServer(brainDir, filepath.Join(tmp, "app.html"))
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			var errBuf bytes.Buffer
			var rev string
			for iter := 0; iter < 15; iter++ {
				reqID := ""
				if id%2 == 1 {
					reqID = subID
				}
				p, err := srv.buildPayloadWithOptions(payloadOptions{
					RequestedID:    reqID,
					LastRevision:   rev,
					ForceRescan:    iter%5 == 0,
					AllowUnchanged: true,
				}, &errBuf)
				if err != nil {
					t.Errorf("goroutine %d err: %v", id, err)
					return
				}
				rev = p.Revision
			}
		}(g)
	}
	wg.Wait()
}
