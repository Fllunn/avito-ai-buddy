package main

import (
	"ai-buddy/backend/platform/process"
	"ai-buddy/mocks/internal/app"
)

func main() { process.Main("mocks", app.Factory) }
