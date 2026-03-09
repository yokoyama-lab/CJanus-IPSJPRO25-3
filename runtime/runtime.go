package main

import (
	"fmt"
	"os"
	"regexp"
	"runtime/pprof"
	"runtime/trace"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mkuroda13/ConcurrentJanus/util"
)

// Channel buffer sizes
const (
	chanBufSmall = 1
	chanBufLarge = 8
)

// Instruction return codes from execution functions
const (
	retNormal  = 0 // continue execution
	retEOF     = 1 // reached end of file (main process end)
	retProcEnd = 2 // non-main process terminated
	retRespawn = 3 // respawn needed (don't modify head)
	retBranch  = 4 // branch taken (head already updated)
)

type runtime struct {
	labels
	symtab
	rev     bool
	instset []instentry
	file    *[]string
	rootpc  *pc
	dag     dag

	// Channels for debug/execution coordination
	runonce  chan bool // step-by-step: signal one execution step
	rundone  chan bool // step-by-step: signal step completed
	rundoner chan bool // step-by-step: acknowledge step completion
	runever  chan bool // normal run: signal continuous execution
	runclose chan bool // signal execution termination
	dagupdate chan bool // signal DAG modification

	// Process lifecycle
	newproc  chan int        // notify new process creation
	termproc chan procchange // notify process termination

	// Execution verification (used when cfg.ExecDebug is true)
	exmap map[pidandpc]string

	startTime time.Time
	skiplock  int
	pidctr    atomic.Int32
}

type procchange struct {
	term   int
	origin int
}

type pidandpc struct {
	pid int
	pc  int
}

type instentry struct {
	re  *regexp.Regexp
	fwd func(*runtime, *pc, []string) (func() *rdwtinfo, func() int)
	bwd func(*runtime, *pc, []string) (func() *rdwtinfo, func() int)
}

func (r *runtime) getChildPC(parent *pc, childid int) *pc {
	for len(parent.children) <= childid {
		pid := r.pidctr.Add(1)
		npc := &pc{}
		npc.pid = int(pid)
		npc.originpid = parent.pid
		npc.localtable = make(map[string][]localaddr)
		npc.pnode = &dagpnode{pc: 0, proc: npc}
		npc.pnode.m = make(chan struct{}, 1)
		npc.children = make([]*pc, 0)
		parent.children = append(parent.children, npc)
	}
	return parent.children[childid]
}

func (r *runtime) runPC(head int) {
	if r.rootpc == nil {
		r.rootpc = &pc{}
		r.rootpc.originpid = 0
		r.rootpc.pid = 0
		r.rootpc.localtable = make(map[string][]localaddr)
		r.rootpc.pnode = &dagpnode{pc: 0, proc: r.rootpc}
		r.rootpc.pnode.m = make(chan struct{}, 1)
		r.rootpc.children = make([]*pc, 0)
	}
	r.rootpc.head = head
	r.newproc <- 0
	go r.rootpc.execute(r)
}

func (r *runtime) runPCCall(parent *pc, childid int, head int, group *sync.WaitGroup, suspended chan bool) {
	p := r.getChildPC(parent, childid)
	p.head = head
	r.newproc <- p.pid
	go func() {
		defer group.Done()
		a := p.execute(r)
		if a == 0 {
			suspended <- true
		}
	}()
}

// Debug loop state
const (
	runModeRunning  = 1 // normal execution in progress
	runModeStep     = 2 // step-by-step execution
	runModeStopped  = 0 // stopped, awaiting commands
)

// debug is the main debugger loop that coordinates execution.
//
// Channel protocol:
//   runonce:    step-by-step trigger (true=backward)
//   rundone:    step completed notification
//   rundoner:   step completion acknowledgement
//   runever:    continuous execution trigger (true=backward)
//   dagupdate:  DAG modification notification
//   runclose:   execution termination signal
//   newproc:    new process PID notification
//   termproc:   process termination notification
func (r *runtime) debug(debugsym chan string, done chan bool) {
	if cfg.Profile {
		cfg.ProfileFile, _ = os.Create("a.prof")
		fmt.Print("Debug >> Profiling started\n")
		pprof.StartCPUProfile(cfg.ProfileFile)
	}
	if cfg.Tracing {
		cfg.TracingFile, _ = os.Create("trace.out")
		fmt.Print("Debug >> Tracing started\n")
		trace.Start(cfg.TracingFile)
	}
	if cfg.TimingDebug {
		r.startTime = time.Now()
	}

	focusReg := regexp.MustCompile(`focus\s+(\d+)`)
	focusTgt := 0
	focusNow := 0
	r.runPC(r.GetBegin("main"))
	running := runModeRunning
	r.runever <- false

	for {
		switch running {
		case runModeRunning:
			select {
			case <-done:
				return
			case <-r.runclose:
				r.runclose <- true
				running = runModeStopped
				fmt.Print("Debug >> Execution finished\n")
				if cfg.TimingDebug {
					endTime := time.Now()
					elapsed := endTime.Sub(r.startTime)
					fmt.Print("========Timing Info========\n")
					fmt.Printf("Begin: %s\n", r.startTime)
					fmt.Printf("End: %s\n", endTime)
					fmt.Printf("Elapsed: %s\n", elapsed)
				}
				if cfg.Profile {
					pprof.StopCPUProfile()
				}
				if cfg.Tracing {
					trace.Stop()
				}
				if cfg.Auto {
					go func() {
						time.Sleep(1 * time.Second)
						switch cfg.AutoProgress {
						case 0:
							debugsym <- "bwd\n"
						case 1:
							debugsym <- "fwd\n"
						default:
							close(done)
							return
						}
						cfg.AutoProgress++
						time.Sleep(1 * time.Second)
						debugsym <- "run\n"
					}()
				}
			case s := <-debugsym:
				switch s {
				case "\n":
					util.Unclog(r.runclose)
					util.Unclog(r.runever)
					r.runclose <- true
					running = runModeStopped
					fmt.Print("Debug >> Execution suspended by user\n")
				default:
					fmt.Print("Debug >> Unknown command\n")
				}
			case pid := <-r.newproc:
				if pid == focusTgt && pid != focusNow {
					focusNow = pid
				}
			case c := <-r.termproc:
				if c.term == focusNow {
					focusNow = c.origin
				}
			}

		case runModeStep:
			// Step-by-step mode (reserved for future implementation)

		case runModeStopped:
			select {
			case <-done:
				return
			case pid := <-r.newproc:
				if pid == focusTgt {
					focusNow = pid
				}
			case c := <-r.termproc:
				if c.term == focusNow {
					focusNow = c.origin
				}
			case s := <-debugsym:
				switch s {
				case "\n":
					// Run one step of focused proc
					util.Unclog(r.runclose)
					util.Unclog(r.runever)
					util.Unclog(r.rundone)
					running = runModeStep
					r.rundone <- true
				case "run\n":
					if cfg.Profile {
						cfg.ProfileFile, _ = os.Create("a.prof")
						fmt.Print("Debug >> Profiling started\n")
						pprof.StartCPUProfile(cfg.ProfileFile)
					}
					if cfg.Tracing {
						cfg.TracingFile, _ = os.Create("trace.out")
						fmt.Print("Debug >> Tracing started\n")
						trace.Start(cfg.TracingFile)
					}
					if cfg.TimingDebug {
						r.startTime = time.Now()
					}
					util.Unclog(r.runclose)
					util.Unclog(r.runever)
					r.runever <- r.rev
					running = runModeRunning
					st := r.GetBegin("main")
					if r.rev {
						st = r.GetEnd("main") + 1
					}
					r.runPC(st)
					fmt.Print("Debug >> Running...\n")
				case "fwd\n":
					r.rev = false
					fmt.Print("Debug >> Forward mode set\n")
				case "bwd\n":
					r.rev = true
					fmt.Print("Debug >> Backward mode set\n")
				case "var\n":
					r.PrintSym()
				case "dag\n":
					r.PrintDag()
				case "proc\n":
					r.PrintProc()
				case "deldag\n":
					r.resetDag()
					fmt.Print("Debug >> Annotation DAG deleted\n")
				default:
					match := focusReg.FindStringSubmatch(s)
					if match != nil {
						i, er := strconv.ParseInt(match[1], 0, 0)
						checkerr(er)
						focusTgt = int(i)
						fmt.Printf("Debug >> Focus changed to %d\n", i)
					} else {
						fmt.Print("Debug >> Unknown command\n")
					}
				}
			}
		}
	}
}

func newRuntime(file *[]string) *runtime {
	return &runtime{
		rev:       false,
		labels:    *newLabels(),
		symtab:    *newSymtab(),
		instset:   make([]instentry, 0),
		file:      file,
		dag:       *newDag(),
		runonce:   make(chan bool, chanBufSmall),
		rundone:   make(chan bool, chanBufSmall),
		rundoner:  make(chan bool, chanBufSmall),
		runever:   make(chan bool, chanBufLarge),
		runclose:  make(chan bool, chanBufLarge),
		dagupdate: make(chan bool, chanBufLarge),
		newproc:   make(chan int, chanBufLarge),
		termproc:  make(chan procchange, chanBufLarge),
		exmap:     make(map[pidandpc]string),
		rootpc:    nil,
	}
}

func (r *runtime) addrun(re string, fwd func(*runtime, *pc, []string) (func() *rdwtinfo, func() int), bwd func(*runtime, *pc, []string) (func() *rdwtinfo, func() int)) {
	reg, _ := regexp.Compile(re)
	r.instset = append(r.instset, instentry{reg, fwd, bwd})
}

func (r *runtime) PrintProc() {
	fmt.Print("\n")
}

func checkerr(e error) {
	if e != nil {
		panic(e)
	}
}
