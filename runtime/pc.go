package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mkuroda13/ConcurrentJanus/util"
)

type pc struct {
	head      int  // line number of next instruction to run
	pc        int  // counter that increases by 1 every exec
	pid       int  // unique process ID (0 = root)
	originpid int  // caller's PID (focus falls back on termination)
	executing bool // true while a block is being executed

	nest       int                    // indentation nesting depth
	localtable map[string][]localaddr // local variable bindings (last = newest)

	pnode     *dagpnode // DAG process node
	dfunc     []func()  // deferred functions (node unlocks)
	lastlabel string    // last branch label (for assertion checks)
	children  []*pc     // child processes spawned by call
}

type localaddr struct {
	addr addr
	nest int
}

// History constants for special local address nesting values
const (
	histColon = -1
	histAt    = -2
)

type execresult int

const (
	exrNormal  execresult = iota // execution completed normally
	exrEOF                       // reached end of main
	exrProcEnd                   // non-main process terminated
)

func (p *pc) StringfyPid() string {
	return strconv.Itoa(p.pid)
}

func (p *pc) indent() {
	p.nest++
}

func (p *pc) unindent() {
	p.nest--
}

func (p *pc) getBlock() string {
	return ":" + strconv.Itoa(p.nest)
}

// Propagate applies f to this pc and all its descendants.
func (p *pc) Propagate(f func(*pc)) {
	f(p)
	for _, c := range p.children {
		c.Propagate(f)
	}
}

// execute is the main execution loop for a process.
// Returns: 0=suspended, 1=EOF, 2=terminated
func (p *pc) execute(r *runtime) int {
	for {
		select {
		case rev := <-r.runonce:
			// Step-by-step mode
			if !rev {
				p.AddNextNodeFwd()
			}
			if !p.executing {
				ret := p.executeBlock(r, rev)
				r.rundone <- true
				if cfg.SlowDebug != 0 {
					time.Sleep(cfg.SlowDebug)
				}
				switch ret {
				case exrEOF:
					util.Unclog(r.runclose)
					r.runclose <- true
					return 1
				case exrProcEnd:
					return 2
				}
			} else {
				r.runonce <- rev
				<-r.rundoner
				r.rundoner <- true
			}

		case rev := <-r.runever:
			// Continuous execution mode
			r.runever <- rev
			for {
				select {
				case <-r.runclose:
					r.runclose <- true
					return 0
				default:
					p.dfunc = make([]func(), 0)
					if rev {
						p.pc--
						p.pnode = p.pnode.prev
					}
					ret := p.executeBlock(r, rev)
					util.Unclog(r.dagupdate)
					if !rev {
						p.AddNextNodeFwd()
						if p.pnode.wt != nil {
							p.pnode.wt.head.tail = p.pnode.wt
						}
						p.pc++
						p.pnode = p.pnode.next
					} else {
						if p.pnode.wt != nil {
							p.pnode.wt.head.tail = p.pnode.wt.prev
						}
					}
					for _, f := range p.dfunc {
						f()
					}
					r.dagupdate <- true
					if cfg.SlowDebug != 0 {
						time.Sleep(cfg.SlowDebug)
					}
					switch ret {
					case exrEOF:
						util.Unclog(r.runclose)
						r.runclose <- true
						return 1
					case exrProcEnd:
						r.termproc <- procchange{p.pid, p.originpid}
						return 2
					}
				}
			}

		case <-r.runclose:
			r.runclose <- true
			return 0
		}
	}
}

// getinst matches an instruction string against the instruction set
// and returns the appropriate prepare/execute function pair.
func (p *pc) getinst(r *runtime, inst string, rev bool) (func() *rdwtinfo, func() int) {
	for _, i := range r.instset {
		match := i.re.FindStringSubmatch(inst)
		if match != nil {
			if rev {
				return i.bwd(r, p, match)
			}
			return i.fwd(r, p, match)
		}
	}
	panic(fmt.Sprintf("unknown instruction: [%s]", inst))
}

// rdwtinfo tracks read/write/lock dependencies for a basic block.
type rdwtinfo struct {
	wt     []addr // write targets
	rd     []addr // read sources
	v      []addr // V (signal) semaphore targets
	p      []addr // P (wait) semaphore targets
	lock   []addr // addresses to lock
	unlock []addr // addresses to unlock
}

func NewRdWtInfo() *rdwtinfo {
	return &rdwtinfo{
		wt:     make([]addr, 0),
		rd:     make([]addr, 0),
		v:      make([]addr, 0),
		p:      make([]addr, 0),
		lock:   make([]addr, 0),
		unlock: make([]addr, 0),
	}
}

func (i *rdwtinfo) Add(j *rdwtinfo) {
	i.wt = append(i.wt, j.wt...)
	i.rd = append(i.rd, j.rd...)
	i.v = append(i.v, j.v...)
	i.p = append(i.p, j.p...)
	i.lock = append(i.lock, j.lock...)
	i.unlock = append(i.unlock, j.unlock...)
}

// Compact removes duplicate entries and eliminates reads that are also writes.
func (i *rdwtinfo) Compact() {
	addrEq := func(a addr, b addr) bool { return a.EqualsMem(b) }
	i.wt = slices.CompactFunc(i.wt, addrEq)
	i.v = slices.CompactFunc(i.v, addrEq)
	i.p = slices.CompactFunc(i.p, addrEq)
	i.lock = slices.CompactFunc(i.lock, addrEq)
	i.unlock = slices.CompactFunc(i.unlock, addrEq)
	i.rd = slices.CompactFunc(i.rd, addrEq)
	// Remove reads that are also writes
	i.rd = slices.DeleteFunc(i.rd, func(a addr) bool {
		return slices.ContainsFunc(i.wt, func(b addr) bool { return a.EqualsMem(b) })
	})
}

// tryRunnable checks if the process can execute and acquires necessary locks.
// Returns (success, cleanup_func). Uses panic/recover for DAG lock contention.
func (p *pc) tryRunnable(r *runtime, rev bool, rdwts *rdwtinfo, failctr int) (bool, func()) {
	defer func() {
		if rec := recover(); rec != nil {
			switch rec.(type) {
			case daglockerror, dagrunnableerror, pvrunnableerror, varlockrunnableerror:
				if cfg.DAGLockDebug {
					fmt.Printf("Check fail, recover started by proc (%d,%d)\n", p.pid, p.pc)
				}
				for _, f := range p.dfunc {
					f()
				}
				if cfg.DAGLockDebug {
					fmt.Printf("Recover ended by proc (%d,%d)\n", p.pid, p.pc)
				}
				switch rec.(type) {
				case dagrunnableerror, varlockrunnableerror:
					time.Sleep(time.Duration(failctr) * time.Microsecond)
				case pvrunnableerror:
					time.Sleep(time.Duration(5*failctr) * time.Microsecond)
				}
			default:
				panic(rec)
			}
		}
	}()

	df := make([]func(), 0)
	if cfg.DAGLockDebug {
		fmt.Printf("Runnable check started by proc (%d,%d)\n", p.pid, p.pc)
	}
	p.AssertRunnable(rev)

	if !rev {
		for _, v := range rdwts.wt {
			df = append(df, r.dag.AddWEdge(v, p))
		}
		for _, v := range rdwts.rd {
			df = append(df, r.dag.AddREdge(v, p))
		}
	}
	for _, v := range rdwts.wt {
		r.WLockAddr(p, v)
	}
	for _, v := range rdwts.v {
		r.TryV(p, v)
	}
	for _, v := range rdwts.p {
		r.TryP(p, v)
	}
	for _, v := range rdwts.lock {
		r.LockAddr(p, v)
	}
	for _, v := range rdwts.unlock {
		df = append(df, func() { r.UnlockAddr(p, v) })
	}

	if cfg.DAGLockDebug {
		fmt.Printf("Runnable check passed by proc (%d,%d)\n", p.pid, p.pc)
	}
	return true, func() {
		for _, n := range df {
			n()
		}
	}
}

// waitRunnable spins until the process can execute, with backoff.
func (p *pc) waitRunnable(r *runtime, rev bool, rdwts *rdwtinfo) func() {
	failctr := 0
	for {
		select {
		case <-r.runclose:
			r.runclose <- true
			return func() {}
		default:
			s, f := p.tryRunnable(r, rev, rdwts, failctr)
			if s {
				return f
			}
			failctr++
			p.dfunc = make([]func(), 0)
		}
	}
}

// slowParseDelay adds an artificial delay to simulate slower parsing.
const slowParseIterations = 500000

// executeBlock executes a basic block (3 instructions) atomically.
func (p *pc) executeBlock(r *runtime, rev bool) execresult {
	p.executing = true
	defer func() {
		p.executing = false
	}()

	inst := make([]string, 0, 3)
	insf := make([]func() *rdwtinfo, 0, 3)
	insa := make([]func() int, 0, 3)
	rt := exrNormal

	if rev {
		if p.head-2 < 0 {
			p.head = 0
			return exrEOF
		}
		for i := range 3 {
			inst = append(inst, (*r.file)[p.head-i-1])
			f, a := p.getinst(r, (*r.file)[p.head-i-1], rev)
			insf = append(insf, f)
			insa = append(insa, a)
		}
	} else {
		if len(*r.file) <= p.head {
			p.head = len(*r.file) - 1
			return exrEOF
		}
		for i := range 3 {
			inst = append(inst, (*r.file)[p.head+i])
			f, a := p.getinst(r, (*r.file)[p.head+i], rev)
			insf = append(insf, f)
			insa = append(insa, a)
		}
	}

	// Collect read/write info from all 3 instructions
	rdwtall := NewRdWtInfo()
	for i := range 3 {
		rdwtall.Add(insf[i]())
	}
	rdwtall.Compact()

	dagf := p.waitRunnable(r, rev, rdwtall)

	if cfg.SlowParse {
		m := 0
		for m <= slowParseIterations {
			m++
		}
	}

	if !cfg.Strict {
		r.skiplock++
	}

	for i := range 3 {
		ret := insa[i]()
		switch ret {
		case retNormal:
			if rev {
				p.head--
			} else {
				p.head++
			}
		case retEOF:
			if rev {
				p.head--
			} else {
				p.head++
			}
			rt = exrEOF
			goto done
		case retProcEnd:
			if rev {
				p.head--
			} else {
				p.head++
			}
			rt = exrProcEnd
			goto done
		case retRespawn, retBranch:
			// Head already updated or no update needed
		}
	}

done:
	if !cfg.SuppressOutput {
		fmt.Print("P", p.pid, ",", p.pc, ">\n", strings.Join(inst, " "), "\n")
	}

	dagf()

	if cfg.ExecDebug {
		if !rev {
			r.exmap[pidandpc{p.pid, p.pc}] = strings.Join(inst, "")
		} else {
			s := ""
			for i := range 3 {
				s += inst[2-i]
			}
			if s != r.exmap[pidandpc{p.pid, p.pc}] {
				panic("unmatched execution: P" + strconv.Itoa(p.pid) + "," + strconv.Itoa(p.pc) + "\n" +
					"expected: \n" + r.exmap[pidandpc{p.pid, p.pc}] +
					"\ngot: \n" + s)
			}
		}
	}
	return rt
}
