package node

import (
	"bytes"
	"log"
	"os"
)

// Custom log writer that filters out unwanted messages
type LogFilter struct {
	logger *log.Logger
}

func NewLogFilter() *LogFilter {
	return &LogFilter{
		logger: log.New(os.Stdout, "", log.LstdFlags),
	}
}

func (lf *LogFilter) Write(p []byte) (int, error) {
	// List substrings that if found in a log entry will prevent it from being logged
	filterSubstrings := []string{
		"websocket: failed to close network connection",
		// Add other substrings as needed
	}

	// Check if the log contains any of the filtered substrings
	for _, filter := range filterSubstrings {
		if bytes.Contains(p, []byte(filter)) {
			return len(p), nil // Skip this log entry
		}
	}

	// If no filter matches, log the entry normally
	return 0, lf.logger.Output(2, string(p))
}
