package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	maxArtifactPlanSteps = 20
	maxArtifactReadBytes = 256 * 1024
)

var (
	artifactPlanCandidates = []string{"task.md", "implementation_plan.md", "plan.md"}
	reMarkdownChecklist    = regexp.MustCompile(`^\s*[-*]\s+\[([ xX/])\]\s+(.+)$`)
)

type PlanStep struct {
	Title  string `json:"title"`
	Status string `json:"status"` // "pending" | "in_progress" | "done" | "skipped" | "failed"
}

type PlanInfo struct {
	Source         string     `json:"source"` // "update_steps" | "artifact" | "focus_only" | "none"
	Focus          string     `json:"focus"`
	FocusUpdatedAt string     `json:"focus_updated_at"`
	ArtifactURI    string     `json:"artifact_uri"`
	DoneCount      int        `json:"done_count"`
	TotalCount     int        `json:"total_count"`
	Steps          []PlanStep `json:"steps"`
}

type artifactPlanStat struct {
	path      string
	uri       string
	mtimeNano int64
	size      int64
	exists    bool
}

type artifactPlanCacheEntry struct {
	modTimeNano int64
	size        int64
	steps       []PlanStep
}

func normalizePlanStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "pending":
		return "pending"
	case "in_progress":
		return "in_progress"
	case "done":
		return "done"
	case "skipped":
		return "skipped"
	case "failed":
		return "failed"
	default:
		return "pending"
	}
}

// decodeArgSteps handles native JSON arrays and string-encoded compact transcript args.
func decodeArgSteps(raw json.RawMessage) []PlanStep {
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
	var parsed []PlanStep
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		return nil
	}
	out := make([]PlanStep, 0, len(parsed))
	for _, st := range parsed {
		title := strings.TrimSpace(st.Title)
		if title == "" {
			continue
		}
		out = append(out, PlanStep{
			Title:  title,
			Status: normalizePlanStatus(st.Status),
		})
	}
	return out
}

func countDoneSteps(steps []PlanStep) int {
	done := 0
	for _, st := range steps {
		if st.Status == "done" {
			done++
		}
	}
	return done
}

// statArtifactPlan checks only the conversation root folder for markdown plan artifacts.
func (s *Server) statArtifactPlan(convID string) artifactPlanStat {
	if convID == "" || validateConversationID(convID) != nil {
		return artifactPlanStat{}
	}
	dir := filepath.Join(s.brainDir, convID)
	rel, err := filepath.Rel(s.brainDir, dir)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return artifactPlanStat{}
	}
	for _, name := range artifactPlanCandidates {
		p := filepath.Join(dir, name)
		info, serr := os.Stat(p)
		if serr == nil && !info.IsDir() {
			uriPath := p
			if abs, aerr := filepath.Abs(p); aerr == nil {
				uriPath = abs
			}
			return artifactPlanStat{
				path:      p,
				uri:       fileURI(uriPath),
				mtimeNano: info.ModTime().UnixNano(),
				size:      info.Size(),
				exists:    true,
			}
		}
	}
	return artifactPlanStat{}
}

func (s *Server) getArtifactPlanSteps(st artifactPlanStat) []PlanStep {
	if !st.exists {
		return nil
	}
	s.mu.Lock()
	if entry, ok := s.artifactPlanCache[st.path]; ok && entry.modTimeNano == st.mtimeNano && entry.size == st.size {
		steps := entry.steps
		s.mu.Unlock()
		return steps
	}
	s.mu.Unlock()

	var steps []PlanStep
	if f, err := os.Open(st.path); err == nil {
		steps = parseMarkdownChecklist(f)
		_ = f.Close()
	}

	s.mu.Lock()
	s.artifactPlanCache[st.path] = artifactPlanCacheEntry{
		modTimeNano: st.mtimeNano,
		size:        st.size,
		steps:       steps,
	}
	s.mu.Unlock()
	return steps
}

func parseMarkdownChecklist(r io.Reader) []PlanStep {
	sc := bufio.NewScanner(io.LimitReader(r, maxArtifactReadBytes))
	buf := make([]byte, 0, 16*1024)
	sc.Buffer(buf, 64*1024)

	steps := make([]PlanStep, 0, 8)
	inCodeFence := false
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inCodeFence = !inCodeFence
			continue
		}
		if inCodeFence {
			continue
		}
		m := reMarkdownChecklist.FindStringSubmatch(line)
		if len(m) != 3 {
			continue
		}
		title := strings.TrimSpace(m[2])
		if title == "" {
			continue
		}
		status := "pending"
		switch m[1] {
		case "x", "X":
			status = "done"
		case "/":
			status = "in_progress"
		}
		steps = append(steps, PlanStep{
			Title:  title,
			Status: status,
		})
		if len(steps) >= maxArtifactPlanSteps {
			break
		}
	}
	return steps
}

func (s *Server) resolvePlanInfo(fromTranscript PlanInfo, art artifactPlanStat) PlanInfo {
	plan := PlanInfo{
		Focus:          fromTranscript.Focus,
		FocusUpdatedAt: fromTranscript.FocusUpdatedAt,
		ArtifactURI:    art.uri,
		Steps:          []PlanStep{},
	}

	if len(fromTranscript.Steps) > 0 {
		plan.Steps = make([]PlanStep, len(fromTranscript.Steps))
		copy(plan.Steps, fromTranscript.Steps)
		plan.Source = "update_steps"
		plan.TotalCount = len(plan.Steps)
		plan.DoneCount = countDoneSteps(plan.Steps)
		return plan
	}

	if art.exists {
		if artSteps := s.getArtifactPlanSteps(art); len(artSteps) > 0 {
			plan.Steps = make([]PlanStep, len(artSteps))
			copy(plan.Steps, artSteps)
			plan.Source = "artifact"
			plan.TotalCount = len(plan.Steps)
			plan.DoneCount = countDoneSteps(plan.Steps)
			return plan
		}
	}

	if plan.Focus != "" {
		plan.Source = "focus_only"
	} else {
		plan.Source = "none"
	}
	return plan
}
