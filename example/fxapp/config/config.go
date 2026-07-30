// Package config reads the environment.
//
// It provides no fx types and has no Module of its own: every module builds
// its own Config with a NewConfig constructor that calls these helpers. That
// is the arrangement worth copying — a module states what it needs in one
// place, next to the code that needs it, and a boot that is missing a variable
// fails naming the module and the variable rather than dereferencing a zero
// value three layers down.
//
// Everything is prefixed FXAPP_ so a shell that also has a tasks example's
// variables exported does not cross the two.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Prefix is prepended to every name these helpers read.
const Prefix = "FXAPP_"

// Get returns the variable, or def when it is unset or empty.
func Get(name, def string) string {
	if v := os.Getenv(Prefix + name); v != "" {
		return v
	}
	return def
}

// Require returns the variable, or an error naming it.
//
// The error says what to set rather than that something was missing, because
// the reader of a boot failure is usually someone who has never seen this
// program before.
func Require(name string) (string, error) {
	if v := os.Getenv(Prefix + name); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("%s%s is not set", Prefix, name)
}

// Int returns the variable parsed as an integer, or def when it is unset. A
// value that does not parse is an error rather than a silent def: a typo in a
// pool size should stop the boot, not halve the throughput of a service nobody
// is looking at.
func Int(name string, def int) (int, error) {
	raw := os.Getenv(Prefix + name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s%s: %q is not a number", Prefix, name, raw)
	}
	return n, nil
}

// Duration returns the variable parsed as a time.Duration ("30s", "5m"), or
// def when it is unset.
func Duration(name string, def time.Duration) (time.Duration, error) {
	raw := os.Getenv(Prefix + name)
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s%s: %q is not a duration (try 30s or 5m)", Prefix, name, raw)
	}
	return d, nil
}
