// Package metrics embeds the reviewed SQL report used by tests and the local CLI.
package metrics

import _ "embed"

//go:embed report.sql
var ReportSQL string
