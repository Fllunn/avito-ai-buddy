package main

import (
	"ai-buddy/backend/internal/app"
	"ai-buddy/backend/platform/process"
)

func main() { process.Main("api", app.Factory) }
