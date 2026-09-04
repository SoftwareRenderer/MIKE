package main

import (
	"fmt"
	"os"
	"strconv"
)

// Every MIKE_* setting has a flag twin, and the precedence is fixed: flag, then environment
// variable, then built-in default. Resolving them inline left five copies of that ladder, and the
// port copy read it with fmt.Sscanf, which discards the conversion error — MIKE_PORT=tcp3000 left
// the server on 3000 without saying so. A bad value is a startup failure that names the variable.

// resolveString returns the first non-empty of the flag value, the named environment variable, and
// the fallback. A set-but-empty variable counts as unset: for MIKE_LISTEN the two readings of an
// empty value are "loopback" and "every interface", so the safe one wins.
func resolveString(flagValue, envName, fallback string) string {
	if flagValue != "" {
		return flagValue
	}
	if v := os.Getenv(envName); v != "" {
		return v
	}
	return fallback
}

// resolvePort resolves a TCP port the same way. flagName is only used to name the source in an
// error; a flag value is range-checked too, since --port 99999 parses and binds nothing.
func resolvePort(flagValue int, flagName, envName string, fallback int) (int, error) {
	if flagValue != 0 {
		if err := validatePort(flagValue, "--"+flagName); err != nil {
			return 0, err
		}
		return flagValue, nil
	}
	raw := os.Getenv(envName)
	if raw == "" {
		return fallback, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a port number", envName, raw)
	}
	if err := validatePort(port, envName); err != nil {
		return 0, err
	}
	return port, nil
}

func validatePort(port int, what string) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%s: port %d is outside 1-65535", what, port)
	}
	return nil
}
