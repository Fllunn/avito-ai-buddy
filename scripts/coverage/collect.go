package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

type goPackage struct {
	Dir, ImportPath   string
	GoFiles, CgoFiles []string
}

// collectGo requires every included source with executable code to appear in
// the profile. Missing files are an error rather than silently disappearing.
func collectGo(dir, profile string) (counts, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return counts{}, err
	}
	f, err := os.Open(profile)
	if err != nil {
		return counts{}, err
	}
	defer f.Close()
	blocks, err := readProfile(f)
	if err != nil {
		return counts{}, err
	}
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return counts{}, fmt.Errorf("go list: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	var total counts
	for {
		var pkg goPackage
		if err := decoder.Decode(&pkg); err == io.EOF {
			break
		} else if err != nil {
			return counts{}, err
		}
		for _, name := range append(pkg.GoFiles, pkg.CgoFiles...) {
			rel, err := filepath.Rel(dir, filepath.Join(pkg.Dir, name))
			if err != nil {
				return counts{}, err
			}
			source, err := os.ReadFile(filepath.Join(pkg.Dir, name))
			if err != nil {
				return counts{}, err
			}
			if excluded(rel, source) {
				continue
			}
			key := profileKey(pkg.ImportPath, name)
			fileBlocks, ok := blocks[key]
			if !ok {
				executable, err := hasExecutable(source)
				if err != nil {
					return counts{}, fmt.Errorf("%s: %w", key, err)
				}
				if executable {
					return counts{}, fmt.Errorf("source missing from Go profile: %s", key)
				}
				continue
			}
			lines, err := goLines(source, fileBlocks)
			if err != nil {
				return counts{}, fmt.Errorf("%s: %w", key, err)
			}
			total.Covered += lines.Covered
			total.Total += lines.Total
		}
	}
	return total, nil
}

func hasExecutable(source []byte) (bool, error) {
	f, err := parser.ParseFile(token.NewFileSet(), "source.go", source, 0)
	if err != nil {
		return false, err
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncDecl); ok {
			return true
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return true
		}
		if _, ok := n.(*ast.BlockStmt); ok {
			return true
		}
		if _, ok := n.(ast.Stmt); ok {
			found = true
			return false
		}
		return true
	})
	return found, nil
}

func collectFrontend(path string) (counts, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return counts{}, err
	}
	var summary struct {
		Total struct {
			Lines counts `json:"lines"`
		} `json:"total"`
	}
	if err := json.Unmarshal(data, &summary); err != nil {
		return counts{}, err
	}
	return summary.Total.Lines, nil
}
