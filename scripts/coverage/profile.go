package main

import (
	"bufio"
	"fmt"
	"go/scanner"
	"go/token"
	"io"
	"strconv"
	"strings"
)

type point struct{ line, column int }
type block struct {
	start, end point
	covered    bool
}

// readProfile retains individual blocks so a line is covered only when every
// block contributing executable tokens to that line was executed.
func readProfile(r io.Reader) (map[string][]block, error) {
	s := bufio.NewScanner(r)
	if !s.Scan() || !strings.HasPrefix(s.Text(), "mode: ") {
		return nil, fmt.Errorf("invalid Go coverage header")
	}
	result := make(map[string][]block)
	for s.Scan() {
		line := s.Text()
		colon := strings.LastIndexByte(line, ':')
		if colon < 1 {
			return nil, fmt.Errorf("invalid Go coverage block")
		}
		var sl, sc, el, ec, statements, hits int
		if _, err := fmt.Sscanf(line[colon+1:], "%d.%d,%d.%d %d %d", &sl, &sc, &el, &ec, &statements, &hits); err != nil || sl < 1 || sc < 1 || el < sl || ec < 1 || statements < 0 || hits < 0 {
			return nil, fmt.Errorf("invalid Go coverage block: %s", line)
		}
		if statements == 0 {
			continue
		}
		key := line[:colon]
		current := block{point{sl, sc}, point{el, ec}, hits > 0}
		merged := false
		for i, existing := range result[key] {
			if existing.start == current.start && existing.end == current.end {
				result[key][i].covered = existing.covered || current.covered
				merged = true
				break
			}
		}
		if !merged {
			result[key] = append(result[key], current)
		}
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// goLines counts source lines containing tokens within instrumentation blocks.
// Delimiters alone are not executable. A shared line is covered only if all
// blocks that contain a token on that line were executed.
func goLines(source []byte, blocks []block) (counts, error) {
	fset := token.NewFileSet()
	file := fset.AddFile("source.go", -1, len(source))
	var scan scanner.Scanner
	var scanErr error
	scan.Init(file, source, func(_ token.Position, msg string) { scanErr = fmt.Errorf("scan Go source: %s", msg) }, 0)
	lines := make(map[int]bool)
	for {
		pos, tok, literal := scan.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.SEMICOLON || tok == token.LBRACE || tok == token.RBRACE || tok == token.LPAREN || tok == token.RPAREN || tok == token.LBRACK || tok == token.RBRACK || tok == token.COMMA || tok == token.PERIOD || tok == token.COLON {
			continue
		}
		p := file.Position(pos)
		for _, b := range blocks {
			if before(point{p.Line, p.Column}, b.start) || !before(point{p.Line, p.Column}, b.end) {
				continue
			}
			if covered, exists := lines[p.Line]; exists {
				lines[p.Line] = covered && b.covered
			} else {
				lines[p.Line] = b.covered
			}
		}
		_ = literal
	}
	if scanErr != nil {
		return counts{}, scanErr
	}
	result := counts{Total: int64(len(lines))}
	for _, covered := range lines {
		if covered {
			result.Covered++
		}
	}
	return result, nil
}

func before(a, b point) bool { return a.line < b.line || a.line == b.line && a.column < b.column }

func profileKey(module, relative string) string {
	return strings.TrimSuffix(module, "/") + "/" + strings.TrimPrefix(relative, "/")
}

func parseInt(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }
