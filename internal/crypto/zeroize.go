package crypto

// Zeroize overwrites every byte of b with zero. Used by callers
// holding defensive copies of secret material — passphrases,
// derived keys — to collapse the leak window between "secret no
// longer needed" and "Go's GC reclaims the slice" into "between
// Zeroize and the next allocation."
//
// Best-effort: the Go runtime is free to move slices during
// goroutine scheduling and GC, so a heap dump or live memory
// acquisition during execution may still recover the bytes from
// a stale copy. The threat model documents this explicitly. We
// still wipe the live slice because that's the cheapest defense
// available to a CLI process holding short-lived key material.
//
// Previously each package that handled secrets defined its own
// unexported `zeroize` (cli, repo, ui — three identical bodies).
// Centralizing here keeps the implementation one-source-of-truth
// and lets a future improvement (e.g. //go:noinline + runtime.
// KeepAlive guards) land in exactly one place.
func Zeroize(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
