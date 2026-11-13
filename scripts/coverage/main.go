package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func excluded(path string, source []byte) bool {
	if strings.HasPrefix(string(source), "// Code generated ") && strings.Contains(strings.SplitN(string(source), "\n", 2)[0], " DO NOT EDIT.") {
		return true
	}
	if filepath.ToSlash(path) != "cmd/server/main.go" {
		return false
	}
	normalize := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	return normalize(string(source)) == normalize(apiEntrypoint) || normalize(string(source)) == normalize(mocksEntrypoint)
}

const apiEntrypoint = `package main
import (
"ai-buddy/backend/internal/app"
"ai-buddy/backend/platform/process"
)
func main() { process.Main("api", app.Factory) }`
const mocksEntrypoint = `package main
import (
"ai-buddy/backend/platform/process"
"ai-buddy/mocks/internal/app"
)
func main() { process.Main("mocks", app.Factory) }`

func run(root string) error {
	values := map[string]counts{}
	for _, module := range []string{"backend", "mocks"} {
		value, err := collectGo(filepath.Join(root, module), filepath.Join(root, "coverage", module+".out"))
		if err != nil {
			return err
		}
		values[module] = value
	}
	value, err := collectFrontend(filepath.Join(root, "frontend/coverage/coverage-summary.json"))
	if err != nil {
		return err
	}
	values["frontend"] = value
	total, err := aggregate(values)
	if err != nil {
		return err
	}
	report := struct {
		Modules map[string]counts `json:"modules"`
		Total   counts            `json:"total"`
	}{values, total}
	output, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "coverage/summary.json"), append(output, '\n'), 0644); err != nil {
		return err
	}
	ok := total.passes()
	for _, module := range []string{"backend", "frontend", "mocks", "total"} {
		value := total
		if module != "total" {
			value = values[module]
		}
		fmt.Printf("%s: %d/%d executable lines (%.2f%%)\n", module, value.Covered, value.Total, float64(value.Covered)*100/float64(value.Total))
		ok = ok && value.passes()
	}
	if !ok {
		return fmt.Errorf("each module and total must exceed 80%%")
	}
	return nil
}
func main() {
	root := "../.."
	if len(os.Args) == 2 {
		root = os.Args[1]
	}
	if err := run(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
