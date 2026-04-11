package main

import "time"

const (
	// SubprocessIdleTTL is the maximum idle time before a subprocess is swept.
	SubprocessIdleTTL = 30 * time.Minute

	// SubprocessSweepInterval is the cadence at which Pool.Sweep runs.
	SubprocessSweepInterval = 1 * time.Minute

	// SubprocessStartTimeout bounds the readiness check after spawning.
	SubprocessStartTimeout = 60 * time.Second

	// SubprocessStopTimeout is the SIGTERM grace period before SIGKILL.
	SubprocessStopTimeout = 10 * time.Second

	// RequestForwardTimeout bounds a single proxied request.
	RequestForwardTimeout = 5 * time.Minute

	// DefaultHTTPPort is the dispatcher's public listen port.
	DefaultHTTPPort = 8090

	// MaxRequestBodyBytes caps incoming request bodies at 10 MiB.
	MaxRequestBodyBytes = 10 << 20

	// PoolRetryAttempts caps Pool.GetOrSpawn retry loops.
	PoolRetryAttempts = 3

	// readyPollInterval is the interval between readiness TCP-connect probes.
	readyPollInterval = 500 * time.Millisecond
)
