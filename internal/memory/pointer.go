package memory

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	PointerThresholdChars = 1200 // Intercept tool output > 1200 runes
	PointerSummaryChars   = 150  // Max summary runes kept in main context
	MaxReadLinesSpan      = 200  // Max lines per read_file_lines call
)

// Pointerizer intercepts oversized tool outputs (>1200 chars), writes the full body
// to workspace/tmp_<id>.txt, summarizes it via a stateless light-model call (<=150 chars),
// and returns a compact pointer stub for the main LLM context.
type Pointerizer struct {
	Workspace  string
	Threshold  int
	SummaryMax int
	Summarizer Summarizer
}

func NewPointerizer(workspace string, sum Summarizer) *Pointerizer {
	return &Pointerizer{
		Workspace:  workspace,
		Threshold:  PointerThresholdChars,
		SummaryMax: PointerSummaryChars,
		Summarizer: sum,
	}
}

// Intercept checks raw output length. If <= Threshold runes, returns (raw, "", false).
// Otherwise saves to workspace/tmp_<id>.txt and returns the pointer stub + filename.
func (p *Pointerizer) Intercept(ctx context.Context, raw string) (output string, relFile string, pointerized bool) {
	thresh := p.Threshold
	if thresh <= 0 {
		thresh = PointerThresholdChars
	}
	runeCount := utf8.RuneCountInString(raw)
	if runeCount <= thresh {
		return raw, "", false
	}
	if err := os.MkdirAll(p.Workspace, 0o755); err != nil {
		return raw, "", false
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	name := "tmp_" + hex.EncodeToString(b[:]) + ".txt"
	fullPath := filepath.Join(p.Workspace, name)
	if err := os.WriteFile(fullPath, []byte(raw), 0o600); err != nil {
		return raw, "", false
	}

	lines := strings.Count(raw, "\n") + 1
	maxSum := p.SummaryMax
	if maxSum <= 0 {
		maxSum = PointerSummaryChars
	}
	var summary string
	if p.Summarizer != nil {
		if s, err := p.Summarizer(ctx, truncateRunes(raw, 4000)); err == nil && strings.TrimSpace(s) != "" {
			summary = truncateRunes(strings.Join(strings.Fields(s), " "), maxSum)
		}
	}
	if summary == "" {
		summary = fallbackSummary(raw, maxSum)
	}
	stub := fmt.Sprintf("[已存盘至 %s（共 %d 行 / %d 字），摘要：%s。如需核查细节请调用 read_file_lines(path=%q, start=1, end=60)。]",
		name, lines, runeCount, summary, name)
	return stub, name, true
}

// ReadFileLines reads a workspace-jailed line range (1-indexed, inclusive) for pointerized files.
func ReadFileLines(workspace, relPath string, startLine, endLine int) (string, error) {
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return "", errors.New("path 不能为空")
	}
	wsAbs, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	target := relPath
	if !filepath.IsAbs(target) {
		target = filepath.Join(wsAbs, relPath)
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(wsAbs, targetAbs)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", errors.New("路径超出工作区范围")
	}
	if startLine <= 0 {
		startLine = 1
	}
	if endLine < startLine {
		endLine = startLine + 59
	}
	if endLine-startLine+1 > MaxReadLinesSpan {
		endLine = startLine + MaxReadLinesSpan - 1
	}

	f, err := os.Open(targetAbs)
	if err != nil {
		return "", err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 2*1024*1024)
	var sb strings.Builder
	lineNo := 0
	emitted := 0
	for sc.Scan() {
		lineNo++
		if lineNo < startLine {
			continue
		}
		if lineNo > endLine {
			break
		}
		fmt.Fprintf(&sb, "%d: %s\n", lineNo, sc.Text())
		emitted++
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if emitted == 0 {
		return fmt.Sprintf("（%s 共 %d 行，区间 %d-%d 无内容）", rel, lineNo, startLine, endLine), nil
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}
