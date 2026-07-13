//go:build tools

// Package tools records dependencies and exact tool pins used by repository automation.
package tools

import (
	_ "github.com/prometheus/client_golang/prometheus"
	_ "go.opentelemetry.io/otel"
)
