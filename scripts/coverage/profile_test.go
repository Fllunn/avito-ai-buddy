package main

import (
	"strings"
	"testing"
)

func TestGoLinesSharedLineRequiresBothBlocks(t *testing.T) {
	source := []byte("package p\nfunc f() { a(); b() }\n")
	profile := "mode: set\np/file.go:2.12,2.16 1 1\np/file.go:2.17,2.20 1 0\n"
	blocks, err := readProfile(strings.NewReader(profile))
	if err != nil {
		t.Fatal(err)
	}
	got, err := goLines(source, blocks["p/file.go"])
	if err != nil || got != (counts{Covered: 0, Total: 1}) {
		t.Fatalf("goLines = %+v, %v", got, err)
	}
}

func TestGoLinesIgnoreDelimitersAndComments(t *testing.T) {
	source := []byte("package p\nfunc f() {\n // comment\n a()\n}\n")
	blocks := []block{{start: point{2, 10}, end: point{5, 2}, covered: true}}
	got, err := goLines(source, blocks)
	if err != nil || got != (counts{Covered: 1, Total: 1}) {
		t.Fatalf("goLines = %+v, %v", got, err)
	}
}

func TestReadProfileRejectsInvalidData(t *testing.T) {
	for _, input := range []string{"", "bad\n", "mode: set\ninvalid\n", "mode: set\np/f.go:0.1,1.2 1 0\n"} {
		if _, err := readProfile(strings.NewReader(input)); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}

func TestRepeatedBlocksMergeAcrossTestBinaries(t *testing.T) {
	profile := "mode: atomic\np/f.go:2.12,2.16 1 0\np/f.go:2.12,2.16 1 1\np/f.go:2.12,2.16 1 0\n"
	blocks, err := readProfile(strings.NewReader(profile))
	if err != nil {
		t.Fatal(err)
	}
	got, err := goLines([]byte("package p\nfunc f() { a() }\n"), blocks["p/f.go"])
	if err != nil || got != (counts{1, 1}) {
		t.Fatal(got, err)
	}
}
