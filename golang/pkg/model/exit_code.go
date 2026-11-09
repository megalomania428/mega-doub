// Package model holds leaf data types shared across packages.
package model

// Process exit codes.
const (
	// ExitOK reports success.
	ExitOK = 0
	// ExitSource reports a source failure.
	ExitSource = 1
	// ExitConfig reports an invocation, configuration, or secret resolution failure.
	ExitConfig = 2
	// ExitStorage reports a storage failure or a command that is not implemented yet.
	ExitStorage = 3
	// ExitInterrupted reports an interruption by a signal or a timeout.
	ExitInterrupted = 4
	// ExitProtection reports a protection failure.
	ExitProtection = 5
	// ExitVerify reports a verification failure.
	ExitVerify = 6
)
