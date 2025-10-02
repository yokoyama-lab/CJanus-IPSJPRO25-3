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
	head       int   //LineNo of next instruction to be run
	pc         int   //Ctr that increases by 1 every exec
	pid        int   //Display pid, unique integer with 0 begin root process
	originpid  int   //pid of the caller, focus will fall back to that once terminated
	executing  bool
	nest       int                    //counter of how much indent is nested
	localtable map[string][]localaddr //last one is newest
	pnode *dagpnode
	dfunc	[]func()
	lastlabel string
	children []*pc
}

type localaddr struct {
	addr addr
	nest int
}

const (
	HIST_COLON = -1
	HIST_AT    = -2
)

func (p *pc) indent() {
	p.nest += 1
}

func (p *pc) unindent() {
	p.nest -= 1
}

type execresult int

const (
	EXR_NORMAL execresult = iota
	EXR_EOF
	EXR_PROCEND
)

func (p *pc) StringfyPid() string {
	return strconv.Itoa(p.pid)
}

func (p *pc) Propagate(f func(*pc)){
	f(p)
	for _,c := range p.children{
		c.Propagate(f)
	}
}

func (p *pc) execute(r *runtime) int {
	//0 means suspended
	//1 means reached EOF
	//2 means terminated
	for {
		select {
		case rev := <-r.runonce:
			//run once mode
			if !rev {
				p.AddNextNodeFwd()
			}
			if !p.executing {
				//check if dag allows it to run, if yes then execute, send rundone
				ret := p.executeBlock(r, rev)
				r.rundone <- true
				if SLOW_DEBUG != 0 {
					time.Sleep(SLOW_DEBUG)
				}
				switch ret {
				case EXR_EOF:
					//EOF
					util.Unclog(r.runclose)
					r.runclose <- true
					return 1
				case EXR_PROCEND:
					//End of non main proc
					return 2
				}
			} else {
				//otherwise broadcast msg to another, block until rundone, which will also be broadcasted
				r.runonce <- rev
				<-r.rundoner
				r.rundoner <- true
			}
		case rev := <-r.runever:
			//run forever mode
			//broadcast run msg
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
					//check if dag allows it to run, if yes then execute, send dagupdate if modified dag
					ret := p.executeBlock(r, rev)
					//TODO add if dag actually does update check
					util.Unclog(r.dagupdate) // unclogs buffer so that it will not be blocked
					if !rev {
						p.AddNextNodeFwd()
						if p.pnode.wt != nil {
							p.pnode.wt.head.tail = p.pnode.wt
						}
						p.pc++
						p.pnode = p.pnode.next
					} else {
						if p.pnode.wt != nil{
							p.pnode.wt.head.tail = p.pnode.wt.prev
						}
					}
					for _, f := range p.dfunc {
						f()
					}
					r.dagupdate <- true
					if SLOW_DEBUG != 0 {
						time.Sleep(SLOW_DEBUG)
					}
					//unlock all nodes
					switch ret {
					case EXR_EOF:
						//EOF, suspend the execution
						util.Unclog(r.runclose)
						r.runclose <- true
						return 1
					case EXR_PROCEND:
						//End of non main proc
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

func (p *pc) getinst(r *runtime, inst string, rev bool) (func() *rdwtinfo, func() int) {
	for _, i := range r.instset {
		match := i.re.FindStringSubmatch(inst)
		if match != nil {
			if rev {
				return i.bwd(r, p, match)
			} else {
				return i.fwd(r, p, match)
			}
		}
	}
	fmt.Printf("Instruction unknown [%s]\n", inst)
	panic("Instruction unknown")
}

type rdwtinfo struct{
	wt []addr
	rd []addr
	v []addr
	p []addr
	lock []addr
	unlock []addr
}

func NewRdWtInfo() *rdwtinfo{
	return &rdwtinfo{make([]addr, 0),make([]addr, 0),make([]addr, 0),make([]addr, 0),make([]addr, 0),make([]addr, 0)}
}

func (i *rdwtinfo) Add(j *rdwtinfo){
	i.wt = append(i.wt, j.wt...)
	i.rd = append(i.rd, j.rd...)
	i.v = append(i.v, j.v...)
	i.p = append(i.p, j.p...)
	i.lock = append(i.lock, j.lock...)
	i.unlock = append(i.unlock, j.unlock...)
}

func (i *rdwtinfo) Compact(){
	i.wt = slices.CompactFunc(i.wt,func(a addr, b addr) bool {return a.EqualsMem(b)})
	i.v = slices.CompactFunc(i.v,func(a addr, b addr) bool {return a.EqualsMem(b)})
	i.p = slices.CompactFunc(i.p,func(a addr, b addr) bool {return a.EqualsMem(b)})
	i.lock = slices.CompactFunc(i.lock,func(a addr, b addr) bool {return a.EqualsMem(b)})
	i.unlock = slices.CompactFunc(i.unlock,func(a addr, b addr) bool {return a.EqualsMem(b)})
	i.rd = slices.CompactFunc(i.rd,func(a addr, b addr) bool {return a.EqualsMem(b)})
	i.rd = slices.DeleteFunc(i.rd,func(a addr) bool {return slices.ContainsFunc(i.wt,func(b addr) bool {return a.EqualsMem(b)})})
}

func (p *pc) tryRunnable(r *runtime, rev bool, rdwts *rdwtinfo, failctr int) (bool, func()){
	//returns function to do dag update
	defer func() {
		if r := recover(); r != nil{
			switch r.(type) {
			case daglockerror, dagrunnableerror, pvrunnableerror, varlockrunnableerror:
				if DAGLOCK_DEBUG {
					fmt.Printf("Check fail, Recover started by proc (%d,%d)\n",p.pid,p.pc)
				}
				for _, f := range p.dfunc {
					f()
				}
				if DAGLOCK_DEBUG {
					fmt.Printf("Recover ended by proc (%d,%d)\n",p.pid,p.pc)
				}
				switch r.(type) {
				case dagrunnableerror, varlockrunnableerror:
					time.Sleep(time.Duration(failctr) * time.Microsecond)
				case pvrunnableerror:
					time.Sleep(time.Duration(5*failctr) * time.Microsecond)
				}
			default:
				panic(r)
			}
		}
	}()
	df := make([]func(),0)
	if DAGLOCK_DEBUG {
		fmt.Printf("Runnable check started by proc (%d,%d)\n",p.pid,p.pc)
	}
	p.AssertRunnable(rev)
	if !rev {
		for _,v := range rdwts.wt{
			df = append(df, r.dag.AddWEdge(v,p))
		}
		for _,v := range rdwts.rd{
			df = append(df, r.dag.AddREdge(v,p))
		}
	}
	for _,v := range rdwts.wt{
		r.WLockAddr(p,v)
	}
	for _,v := range rdwts.v{
		r.TryV(p,v)
	}
	for _,v := range rdwts.p{
		r.TryP(p,v)
	}
	for _,v := range rdwts.lock{
		r.LockAddr(p,v)
	}
	for _,v := range rdwts.unlock{
		df = append(df, func() {r.UnlockAddr(p,v)})
	}
	if DAGLOCK_DEBUG {
		fmt.Printf("Runnable check passed by proc (%d,%d)\n",p.pid,p.pc)
	}
	return true, func() {
		for _,n := range df{
			n()
		}
	}
}

func (p *pc) waitRunnable(r *runtime, rev bool, rdwts *rdwtinfo) func(){
	failctr := 0
	for {
		select{
		case <-r.runclose:
			r.runclose <- true
			return func() {}
		default:
			s,f := p.tryRunnable(r,rev,rdwts,failctr)
			if s {
				return f
			}
			failctr++
			p.dfunc = make([]func(), 0)
		}
	}
}

func (p *pc) executeBlock(r *runtime, rev bool) execresult {
	//We made sure that block that is executed is next/previous 3 lines, by adding entry/exit that doesn't exist in source
	//head points to next instruction to run in fwd, instruction just ran in bwd
	p.executing = true
	defer func() {
		p.executing = false
	}()
	inst := make([]string, 0, 3)
	insf := make([]func() *rdwtinfo, 0, 3)
	insa := make([]func() int, 0, 3)
	rt := EXR_NORMAL
	if rev {
		if p.head-2 < 0 {
			p.head = 0
			return EXR_EOF
		}
		for i := range 3 {
			inst = append(inst, (*r.file)[p.head-i-1])
			f, a := p.getinst(r, (*r.file)[p.head-i-1], rev)
			insf = append(insf, f)
			insa = append(insa, a)
		}
	} else {
		if len(*(r.file)) <= p.head {
			p.head = len(*(r.file)) - 1
			return EXR_EOF
		}
		for i := range 3 {
			inst = append(inst, (*r.file)[p.head+i])
			f, a := p.getinst(r, (*r.file)[p.head+i], rev)
			insf = append(insf, f)
			insa = append(insa, a)
		}
	}

	rdwtall := NewRdWtInfo()
	for i := range 3 {
		f := insf[i]
		rdwtall.Add( f() )
	}
	rdwtall.Compact()

	dagf := p.waitRunnable(r,rev,rdwtall)

	if SLOW_PARSE {
		m := 0
		for m <= 500000 {
			m++
		}
	}

	if !STRICT {
		r.skiplock++
	}
L1:
	for i := range 3 {
		f := insa[i]
		ret := f()
		switch ret {
		case 0:
			if rev {
				p.head--
			} else {
				p.head++
			}
			continue
		case 1:
			if rev {
				p.head--
			} else {
				p.head++
			}
			rt = EXR_EOF
			break L1
		case 2:
			if rev {
				p.head--
			} else {
				p.head++
			}
			rt = EXR_PROCEND
			break L1
		case 3:
			continue
		case 4:
			continue
		}
	}
	if !SUPPRESSOUTPUT {
		fmt.Print("P", p.pid, ",", p.pc, ">\n", strings.Join(inst, " "), "\n")
	}

	dagf()

	if EXEC_DEBUG {
		if !rev {
			r.exmap[pidandpc{p.pid, p.pc}] = strings.Join(inst, "")
		} else {
			s := ""
			for i := range 3 {
				s += inst[2-i]
			}
			if s != r.exmap[pidandpc{p.pid, p.pc}] {
				panic("Unmatched execution: P" + strconv.Itoa(p.pid) + "," + strconv.Itoa(p.pc) + "\n" +
					"Expected: \n" + r.exmap[pidandpc{p.pid, p.pc}] +
					"\nGot: \n" + s)
			}
		}
	}
	return rt
}

//	type eMutex struct{
//		sync.Mutex
//	}
//
//	func (m *eMutex) Lock(){
//		m.Mutex.Lock()
//		print("Locked mutex\n")
//	}
//
//	func (m *eMutex) Unlock(){
//		m.Mutex.Unlock()
//		print("Unlocked mutex\n")
//	}
//
// var exmut *eMutex = &eMutex{}

func (p *pc) getBlock() string {
	return ":" + strconv.Itoa(p.nest)
}

func (r *runtime) PrintProc() {
	// bctr := 0
	// for _, p := range r.pcs {
	// 	fmt.Printf("(%d,%d) ", p.pid, p.pc)
	// 	bctr += p.pc
	// }
	fmt.Print("\n")
}

func checkerr(e error) {
	if e != nil {
		panic(e)
	}
}
