package main

import (
	"os"
	"time"
)

// Config holds all runtime configuration flags.
// Previously these were scattered as 16+ global variables.
type Config struct {
	// Debug flags
	SlowDebug    time.Duration // add sleep after every instruction
	SeqDAG       bool          // group same process together in DAG display
	DAGDebug     bool          // display DAG update information
	VarDebug     bool          // display variable read/write information
	BlockDebug   bool          // display block updates
	ExecDebug    bool          // store and compare fwd/bwd execution
	SemDebug     bool          // display semaphore V/P operations
	LockDebug    bool          // display lock/unlock of addresses
	DAGLockDebug bool          // display lock/unlock of DAG nodes
	TimingDebug  bool          // time the execution

	// Execution modes
	Sequential      bool // disable goroutines and locks
	SuppressOutput  bool // stop printing executed blocks
	SlowParse       bool // simulate slower parsing with artificial delay
	Strict          bool // strictly follow basic-block-wise atomicity
	NoDAG           bool // do not record annotation DAG
	ExtExpr         bool // allow expressions of arbitrary length

	// Profiling
	Profile     bool
	ProfileFile *os.File
	Tracing     bool
	TracingFile *os.File

	// Runtime
	MaxProcs int // GOMAXPROCS limit (0 = no limit)

	// Auto mode
	Auto         bool
	AutoProgress int // 0=forward, 1=backward, 2=replay
}

// Global config instance, initialized from command-line flags in main().
var cfg Config

// EXTEXPR is kept as a standalone global because the yacc-generated
// parser (expr.go) references it directly. Synced from cfg in main().
var EXTEXPR = false
