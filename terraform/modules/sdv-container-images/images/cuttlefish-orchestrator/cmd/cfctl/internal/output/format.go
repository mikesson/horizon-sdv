// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package output provides the CLI's messaging and JSON-printing primitives.
// It imports no cfctl packages so every other package can use Debug.
package output

import (
	"encoding/json"
	"fmt"
	"os"
)

// PrintJSON prints the value to stdout as indented JSON.
func PrintJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// ActionResult is the --json outcome of a state-changing command. Status is
// "ok", or "canceled" when the user declines the prompt.
type ActionResult struct {
	Resource string `json:"resource"`
	ID       string `json:"id,omitempty"`
	Action   string `json:"action"`
	Status   string `json:"status"`
}

// PrintActionResult prints an ActionResult with status "ok" to stdout.
func PrintActionResult(resource, id, action string) error {
	return PrintJSON(ActionResult{Resource: resource, ID: id, Action: action, Status: "ok"})
}

// Success prints a success message to stdout.
func Success(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
}

// Info prints a progress message to stderr. Stdout is reserved for command
// results.
func Info(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// Warn prints a warning message.
func Warn(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Warning: "+format+"\n", args...)
}

// verbose controls whether debug messages are printed.
var verbose bool

// SetVerbose enables or disables verbose output.
func SetVerbose(v bool) {
	verbose = v
}

// Debug prints a debug message to stderr if verbose mode is enabled.
func Debug(format string, args ...any) {
	if verbose {
		fmt.Fprintf(os.Stderr, "[DEBUG] "+format+"\n", args...)
	}
}
