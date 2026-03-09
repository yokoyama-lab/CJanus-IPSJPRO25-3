package main

//go:generate $GOPATH/bin/goyacc -o expr.go -p "expr" expr.y

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"syscall"

	"github.com/mkuroda13/ConcurrentJanus/util"
)

func main() {
	verbose := true
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: %s [args] file\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.DurationVar(&cfg.SlowDebug, "s", 0, "Add sleep of specified amount after every instruction")
	flag.BoolVar(&cfg.DAGDebug, "dd", false, "Display Annotation DAG updates to stdout")
	flag.BoolVar(&cfg.VarDebug, "dv", false, "Display variable reads/writes to stdout")
	flag.BoolVar(&cfg.BlockDebug, "db", false, "Display block changes to stdout")
	flag.BoolVar(&cfg.SemDebug, "ds", false, "Display semaphore update upon V/P operation to stdout")
	flag.BoolVar(&cfg.LockDebug, "dl", false, "Display lock/unlock of addresses to stdout")
	flag.BoolVar(&cfg.DAGLockDebug, "da", false, "Display lock/unlock of Annotation DAG's nodes to stdout")
	flag.BoolVar(&verbose, "v", false, "Verbose: enables -dd, -dv, -db, -ds, -dl, and -e")
	flag.BoolVar(&cfg.ExecDebug, "e", false, "Fail if backwards execution does not match forward")
	flag.BoolVar(&cfg.TimingDebug, "t", false, "Measure and display execution time")
	flag.BoolVar(&cfg.Sequential, "seq", false, "Sequential mode: no goroutines, single call only")
	flag.BoolVar(&cfg.SuppressOutput, "silent", false, "Stop printing executed blocks")
	flag.BoolVar(&cfg.Profile, "profile", false, "Enable CPU profiling (output: a.prof)")
	flag.BoolVar(&cfg.Tracing, "trace", false, "Enable execution tracing (output: trace.out)")
	flag.BoolVar(&cfg.NoDAG, "nodag", false, "Do not record annotation DAG")
	flag.BoolVar(&cfg.SlowParse, "slowparse", false, "Simulate slower parsing with artificial delay")
	flag.BoolVar(&cfg.Strict, "strict", false, "Strictly follow basic-block-wise atomicity")
	flag.BoolVar(&cfg.ExtExpr, "extexpr", false, "Allow expressions of arbitrary length")
	flag.IntVar(&cfg.MaxProcs, "procs", 0, "Set GOMAXPROCS to limit CPU usage")
	flag.BoolVar(&cfg.Auto, "auto", false, "Automatically run forward, backward, replay, then terminate")
	flag.Parse()

	if verbose {
		cfg.DAGDebug = true
		cfg.VarDebug = true
		cfg.BlockDebug = true
		cfg.ExecDebug = true
		cfg.SemDebug = true
		cfg.LockDebug = true
		cfg.DAGLockDebug = true
	}
	// Sync generated-code globals from config
	EXTEXPR = cfg.ExtExpr

	if cfg.Profile {
		util.SetRuntimeBlockProfileRate(1)
		util.SetRuntimeMutexProfileRate(1)
	}
	if cfg.MaxProcs != 0 {
		util.SetRuntimeCPU(cfg.MaxProcs)
	}
	runDebugger(flag.Arg(0))
}

func runDebugger(filename string) {
	file, err := os.Open(filename)
	checkerr(err)

	f := make([]string, 0)
	rt := newRuntime(&f)
	scanner := bufio.NewScanner(file)

	// Regex for -> and <- label statements
	goreg := regexp.MustCompile(`^[\w\d$.:\[\]+\-*/!<=>&|%]*\s*->\s*([\w\d$.:]+)\s*(?:;\s*([\w\d$.:]+)\s*)?`)
	comereg := regexp.MustCompile(`^\s*([\w\d$.:]+)\s*(?:;\s*([\w\d$.:]+)\s*)?<-[\w\d$.:\[\]+\-*/!<=>&|%]*`)
	beginreg := regexp.MustCompile(`^begin\s+([\w\d$.:]+)$`)
	endreg := regexp.MustCompile(`^end\s+([\w\d$.:]+)$`)
	empreg := regexp.MustCompile(`^\s*$`)

	lineno := 0
	for scanner.Scan() {
		t := scanner.Text()
		if empreg.MatchString(t) {
			continue
		}

		gom := goreg.FindStringSubmatch(t)
		com := comereg.FindStringSubmatch(t)
		if gom != nil {
			for i := 1; i <= 2; i++ {
				if len(gom[i]) != 0 {
					rt.labels.AddGoto(gom[i], lineno)
				}
			}
		}
		if com != nil {
			for i := 1; i <= 2; i++ {
				if len(com[i]) != 0 {
					rt.labels.AddComeFrom(com[i], lineno)
				}
			}
		}

		bgm := beginreg.FindStringSubmatch(t)
		enm := endreg.FindStringSubmatch(t)
		if bgm != nil {
			rt.labels.AddBegin(bgm[1], lineno)
		}
		if enm != nil {
			rt.labels.AddEnd(enm[1], lineno)
		}

		if lineno%3 == 1 && (enm != nil || gom != nil) {
			f = append(f, "skip")
			lineno++
		}
		if lineno%3 == 2 && enm == nil && gom == nil {
			s := rt.RegisterNewLabel(lineno)
			f = append(f, "-> "+s, s+" <-")
			lineno += 2
		}

		f = append(f, t)
		lineno++
	}

	rt.initInstset()
	debugsym := make(chan string)
	done := make(chan bool)

	go handleSignals(done)
	go readStdin(debugsym, done)
	go rt.debug(debugsym, done)
	<-done
}

func readStdin(debugsym chan<- string, done <-chan bool) {
	in := bufio.NewReader(os.Stdin)
	for {
		select {
		case <-done:
			return
		default:
			r, err := in.ReadString('\n')
			checkerr(err)
			debugsym <- r
		}
	}
}

func handleSignals(done chan bool) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	for {
		select {
		case <-done:
			return
		case sig := <-sigs:
			fmt.Print("\n", sig, "\n")
			close(done)
			return
		}
	}
}
