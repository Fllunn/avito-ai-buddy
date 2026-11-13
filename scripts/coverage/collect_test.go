package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestUnimportedPackageIncluded(t *testing.T) {
	dir := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module fixture\n\ngo 1.27.0\n")
	write("used/used.go", "package used\nfunc F() int { return 1 }\n")
	write("used/used_test.go", "package used\nimport \"testing\"\nfunc TestF(t *testing.T) { F() }\n")
	write("unused/unused.go", "package unused\nfunc F() int { return 2 }\n")
	profile := filepath.Join(dir, "cover.out")
	cmd := exec.Command("go", "test", "-coverpkg=./...", "-coverprofile="+profile, "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go test: %v\n%s", err, out)
	}
	got, err := collectGo(dir, profile)
	if err != nil || got != (counts{1, 2}) {
		t.Fatalf("collectGo = %+v, %v", got, err)
	}
}

func TestMissingProfileDoesNotHideSource(t *testing.T) {
	if ok, err := hasExecutable([]byte("package p\nfunc F() { println(1) }\n")); err != nil || !ok {
		t.Fatalf("hasExecutable = %v, %v", ok, err)
	}
	if ok, err := hasExecutable([]byte("package p\ntype T struct{}\n")); err != nil || ok {
		t.Fatalf("hasExecutable = %v, %v", ok, err)
	}
}
