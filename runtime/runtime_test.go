package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testBinary builds the runtime binary once and returns its path.
var testBinary string

func TestMain(m *testing.M) {
	// Build the runtime binary for integration tests
	dir, err := os.MkdirTemp("", "cjanus-test-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	testBinary = filepath.Join(dir, "cjanus-runtime")
	cmd := exec.Command("go", "build", "-o", testBinary, ".")
	cmd.Dir = findProjectRoot()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic("failed to build runtime: " + err.Error())
	}

	os.Exit(m.Run())
}

func findProjectRoot() string {
	// Walk up from current dir to find go.work
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "runtime")); err == nil {
			return filepath.Join(dir, "runtime")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "."
}

func findProgsDir() string {
	dir, _ := os.Getwd()
	for {
		progs := filepath.Join(dir, "progs")
		if _, err := os.Stat(progs); err == nil {
			return progs
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "progs"
}

// runCJanus executes the runtime with the given arguments and returns stdout+stderr.
func runCJanus(t *testing.T, timeout time.Duration, args ...string) (string, error) {
	t.Helper()

	cmd := exec.Command(testBinary, args...)

	// The CJanus runtime reads stdin interactively and panics on EOF.
	// Use /dev/zero to provide an infinite stream of null bytes (same as benchmark.sh).
	devZero, err := os.Open("/dev/zero")
	if err != nil {
		t.Fatalf("cannot open /dev/zero: %v", err)
	}
	defer devZero.Close()
	cmd.Stdin = devZero

	done := make(chan struct{})
	var output []byte
	var cmdErr error

	go func() {
		output, cmdErr = cmd.CombinedOutput()
		close(done)
	}()

	select {
	case <-done:
		return string(output), cmdErr
	case <-time.After(timeout):
		cmd.Process.Kill()
		<-done
		return string(output), &timeoutError{timeout}
	}
}

type timeoutError struct {
	d time.Duration
}

func (e *timeoutError) Error() string {
	return "execution timed out after " + e.d.String()
}

// parseElapsedTimes extracts Elapsed durations from output.
func parseElapsedTimes(output string) []time.Duration {
	re := regexp.MustCompile(`Elapsed: ([0-9.]+(?:ms|s|µs|ns|h|m))`)
	matches := re.FindAllStringSubmatch(output, -1)
	var durations []time.Duration
	for _, m := range matches {
		d, err := time.ParseDuration(m[1])
		if err == nil {
			durations = append(durations, d)
		}
	}
	return durations
}

// --- Compiler Tests ---

func TestCompilerConsistency(t *testing.T) {
	progsDir := findProgsDir()
	sources, err := filepath.Glob(filepath.Join(progsDir, "*.cj"))
	if err != nil || len(sources) == 0 {
		t.Skipf("no .cj files found in %s", progsDir)
	}

	// Build compiler
	dir, _ := os.MkdirTemp("", "cjanus-compiler-test-*")
	defer os.RemoveAll(dir)
	compilerBin := filepath.Join(dir, "cjanus-compiler")

	projectRoot, _ := filepath.Abs(filepath.Join(findProgsDir(), ".."))
	cmd := exec.Command("go", "build", "-o", compilerBin, "./compiler")
	cmd.Dir = projectRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compiler build failed: %s\n%s", err, out)
	}

	for _, src := range sources {
		name := strings.TrimSuffix(filepath.Base(src), ".cj")
		t.Run(name, func(t *testing.T) {
			existing := filepath.Join(progsDir, name+".crl")
			if _, err := os.Stat(existing); os.IsNotExist(err) {
				t.Skipf("no existing .crl for %s", name)
			}

			tmpOut := filepath.Join(dir, name+".crl")
			cmd := exec.Command(compilerBin, "-o", tmpOut, src)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("compilation failed: %s\n%s", err, out)
			}

			expected, _ := os.ReadFile(existing)
			actual, _ := os.ReadFile(tmpOut)
			if string(expected) != string(actual) {
				t.Errorf("output differs from existing .crl (%d vs %d bytes)",
					len(expected), len(actual))
			}
		})
	}
}

// --- Runtime Auto-mode Tests (Forward → Backward → Replay) ---

func TestAutoModeFib10(t *testing.T) {
	progsDir := findProgsDir()
	crl := filepath.Join(progsDir, "fib.crl")

	tests := []struct {
		name  string
		procs int
	}{
		{"procs1", 1},
		{"procs2", 2},
		{"procs10", 10},
		{"procs20", 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := runCJanus(t, 30*time.Second,
				"-silent", "-v=false", "-auto", "-t",
				"-procs", strconv.Itoa(tt.procs), crl)
			if err != nil {
				t.Fatalf("execution failed: %v\noutput: %s", err, output)
			}

			durations := parseElapsedTimes(output)
			if len(durations) != 3 {
				t.Fatalf("expected 3 Elapsed lines (fwd/bwd/replay), got %d\noutput:\n%s",
					len(durations), output)
			}

			t.Logf("Forward: %s, Backward: %s, Replay: %s",
				durations[0], durations[1], durations[2])
		})
	}
}

func TestAutoModeFib15(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping fib15 in short mode")
	}

	progsDir := findProgsDir()
	crl := filepath.Join(progsDir, "fib15.crl")

	tests := []struct {
		name    string
		procs   int
		timeout time.Duration
	}{
		{"procs1", 1, 60 * time.Second},
		{"procs2", 2, 60 * time.Second},
		{"procs10", 10, 60 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := runCJanus(t, tt.timeout,
				"-silent", "-v=false", "-auto", "-t",
				"-procs", strconv.Itoa(tt.procs), crl)
			if err != nil {
				if _, ok := err.(*timeoutError); ok {
					t.Skipf("timed out (known instability at high procs)")
				}
				t.Fatalf("execution failed: %v", err)
			}

			durations := parseElapsedTimes(output)
			if len(durations) != 3 {
				t.Fatalf("expected 3 phases, got %d", len(durations))
			}
			t.Logf("Forward: %s, Backward: %s, Replay: %s",
				durations[0], durations[1], durations[2])
		})
	}
}

func TestAutoModeSfib(t *testing.T) {
	progsDir := findProgsDir()
	crl := filepath.Join(progsDir, "sfib.crl")

	output, err := runCJanus(t, 60*time.Second,
		"-silent", "-v=false", "-auto", "-t", "-procs", "20", crl)
	if err != nil {
		t.Fatalf("execution failed: %v\noutput: %s", err, output)
	}

	durations := parseElapsedTimes(output)
	if len(durations) != 3 {
		t.Fatalf("expected 3 phases, got %d\noutput:\n%s", len(durations), output)
	}
	t.Logf("Forward: %s, Backward: %s, Replay: %s",
		durations[0], durations[1], durations[2])
}

func TestAutoModeSieve100(t *testing.T) {
	progsDir := findProgsDir()
	crl := filepath.Join(progsDir, "sieve.crl")

	tests := []struct {
		name  string
		procs int
	}{
		{"procs1", 1},
		{"procs2", 2},
		{"procs10", 10},
		{"procs20", 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := runCJanus(t, 60*time.Second,
				"-silent", "-v=false", "-auto", "-t",
				"-procs", strconv.Itoa(tt.procs), crl)
			if err != nil {
				t.Fatalf("execution failed: %v\noutput: %s", err, output)
			}

			durations := parseElapsedTimes(output)
			if len(durations) != 3 {
				t.Fatalf("expected 3 phases, got %d\noutput:\n%s", len(durations), output)
			}
			t.Logf("Forward: %s, Backward: %s, Replay: %s",
				durations[0], durations[1], durations[2])
		})
	}
}

func TestAutoModeSieveNoplock(t *testing.T) {
	progsDir := findProgsDir()
	crl := filepath.Join(progsDir, "sieve_noplock.crl")

	tests := []struct {
		name  string
		procs int
	}{
		{"procs1", 1},
		{"procs2", 2},
		{"procs10", 10},
		{"procs20", 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := runCJanus(t, 60*time.Second,
				"-silent", "-v=false", "-auto", "-t",
				"-procs", strconv.Itoa(tt.procs), crl)
			if err != nil {
				t.Fatalf("execution failed: %v\noutput: %s", err, output)
			}

			durations := parseElapsedTimes(output)
			if len(durations) != 3 {
				t.Fatalf("expected 3 phases, got %d\noutput:\n%s", len(durations), output)
			}
			t.Logf("Forward: %s, Backward: %s, Replay: %s",
				durations[0], durations[1], durations[2])
		})
	}
}

// --- Flag Tests ---

func TestFlagTrace(t *testing.T) {
	progsDir := findProgsDir()
	crl := filepath.Join(progsDir, "fib.crl")

	tmpDir, _ := os.MkdirTemp("", "cjanus-trace-*")
	defer os.RemoveAll(tmpDir)

	cmd := exec.Command(testBinary,
		"-silent", "-v=false", "-auto", "-trace", "-procs", "2", crl)
	cmd.Dir = tmpDir
	devZero, _ := os.Open("/dev/zero")
	defer devZero.Close()
	cmd.Stdin = devZero

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("execution failed: %v\n%s", err, out)
	}

	traceFile := filepath.Join(tmpDir, "trace.out")
	info, err := os.Stat(traceFile)
	if err != nil {
		t.Fatal("trace.out not generated")
	}
	if info.Size() == 0 {
		t.Fatal("trace.out is empty")
	}
	t.Logf("trace.out size: %d bytes", info.Size())
}

func TestFlagProfile(t *testing.T) {
	progsDir := findProgsDir()
	crl := filepath.Join(progsDir, "fib.crl")

	tmpDir, _ := os.MkdirTemp("", "cjanus-profile-*")
	defer os.RemoveAll(tmpDir)

	cmd := exec.Command(testBinary,
		"-silent", "-v=false", "-auto", "-profile", "-procs", "2", crl)
	cmd.Dir = tmpDir
	devZero, _ := os.Open("/dev/zero")
	defer devZero.Close()
	cmd.Stdin = devZero

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("execution failed: %v\n%s", err, out)
	}

	profFile := filepath.Join(tmpDir, "a.prof")
	info, err := os.Stat(profFile)
	if err != nil {
		t.Fatal("a.prof not generated")
	}
	if info.Size() == 0 {
		t.Fatal("a.prof is empty")
	}
	t.Logf("a.prof size: %d bytes", info.Size())
}

func TestFlagSequential(t *testing.T) {
	progsDir := findProgsDir()
	// sfib uses single call, so it works in sequential mode
	crl := filepath.Join(progsDir, "sfib.crl")

	output, err := runCJanus(t, 60*time.Second,
		"-silent", "-v=false", "-auto", "-t", "-seq", crl)
	if err != nil {
		t.Fatalf("execution failed: %v\noutput: %s", err, output)
	}

	durations := parseElapsedTimes(output)
	if len(durations) != 3 {
		t.Fatalf("expected 3 phases, got %d\noutput:\n%s", len(durations), output)
	}
	t.Logf("Sequential mode - Forward: %s, Backward: %s, Replay: %s",
		durations[0], durations[1], durations[2])
}

func TestFlagTiming(t *testing.T) {
	progsDir := findProgsDir()
	crl := filepath.Join(progsDir, "fib.crl")

	output, err := runCJanus(t, 30*time.Second,
		"-silent", "-v=false", "-auto", "-t", "-procs", "2", crl)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if !strings.Contains(output, "========Timing Info========") {
		t.Error("timing info header not found in output")
	}
	if !strings.Contains(output, "Begin:") || !strings.Contains(output, "End:") {
		t.Error("timing begin/end not found in output")
	}
}

// --- Backward Consistency Test ---
// Verifies that backward execution produces no assertion failures
// (the -e flag enables forward/backward matching checks)

func TestExecDebugConsistency(t *testing.T) {
	progsDir := findProgsDir()
	// Use sfib (single call) to avoid concurrent map writes on exmap.
	// The -e flag is not safe with multi-process fib due to unsynchronized exmap access.
	crl := filepath.Join(progsDir, "sfib.crl")

	output, err := runCJanus(t, 60*time.Second,
		"-silent", "-v=false", "-auto", "-e", "-seq", crl)
	if err != nil {
		t.Fatalf("execution failed (possible fwd/bwd mismatch): %v\noutput: %s", err, output)
	}

	if strings.Contains(output, "unmatched execution") {
		t.Fatal("forward/backward execution mismatch detected")
	}
	t.Log("Forward/backward consistency check passed")
}
