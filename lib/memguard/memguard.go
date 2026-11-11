// Package memguard keeps the process's memory visible and bounded: a default
// soft heap limit, a periodic memory log line and an optional pprof listener.
package memguard

import (
	"math"
	"net"
	"net/http"
	_ "net/http/pprof" // registers /debug/pprof on http.DefaultServeMux
	"os"
	"runtime"
	"runtime/debug"
	"time"

	"go.uber.org/zap"
)

const mb = 1 << 20

// ApplyDefaultLimit sets a soft heap limit of defaultMB when GOMEMLIMIT is not
// set. An explicit GOMEMLIMIT always wins. Near the limit the GC runs harder
// instead of letting the heap grow until the kernel's OOM killer steps in.
func ApplyDefaultLimit(defaultMB int) {
	switch {
	case os.Getenv("GOMEMLIMIT") != "":
		zap.L().Info("memory limit", zap.Int64("limitMB", debug.SetMemoryLimit(-1)/mb), zap.String("source", "GOMEMLIMIT"))
	case defaultMB > 0:
		debug.SetMemoryLimit(int64(defaultMB) * mb)
		zap.L().Info("memory limit", zap.Int("limitMB", defaultMB), zap.String("source", "default"))
	default:
		if debug.SetMemoryLimit(-1) == math.MaxInt64 {
			zap.L().Info("memory limit", zap.String("source", "none"))
		}
	}
}

// LogEvery writes one line with the heap and goroutine counts at each interval.
func LogEvery(interval time.Duration) {
	if interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var m runtime.MemStats
		for range ticker.C {
			runtime.ReadMemStats(&m)
			zap.L().Info("memstats",
				zap.Uint64("heapAllocMB", m.HeapAlloc/mb),
				zap.Uint64("heapInuseMB", m.HeapInuse/mb),
				zap.Uint64("sysMB", m.Sys/mb),
				zap.Uint32("numGC", m.NumGC),
				zap.Int("goroutines", runtime.NumGoroutine()),
			)
		}
	}()
}

// ServePprof exposes /debug/pprof on addr. Heap dumps carry message content, so
// only a loopback address is accepted. Empty disables it.
func ServePprof(addr string) {
	if addr == "" {
		return
	}
	host, _, err := net.SplitHostPort(addr)
	if ip := net.ParseIP(host); err != nil || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
		zap.L().Warn("pprof not started: PPROF_ADDR must be a loopback address", zap.String("addr", addr))
		return
	}
	go func() {
		if err := http.ListenAndServe(addr, http.DefaultServeMux); err != nil {
			zap.L().Warn("pprof listener stopped", zap.String("addr", addr), zap.Error(err))
		}
	}()
}
