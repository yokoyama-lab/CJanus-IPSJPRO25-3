package main

import (
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"
)

// Lock timeout durations for DAG node acquisition.
// Lower-priority processes get shorter timeouts to yield to higher-priority ones.
const (
	lockTimeoutLow  = 1 * time.Microsecond   // for lower-priority lock attempts
	lockTimeoutHigh = 100 * time.Microsecond  // for higher-priority (prioritized) attempts
)

type dag struct {
	entries   map[maddr]*dagentry
	entrylock sync.Mutex
}

// maddr is the map key for DAG entries, identifying a memory address.
type maddr struct {
	idx      int
	isGlobal bool
}

func (a *maddr) String() string {
	kind := "V"
	if a.isGlobal {
		kind = "M"
	}
	return fmt.Sprintf("%s[%d]\n", kind, a.idx)
}

// GetEntry returns (or creates) the DAG entry for the given address.
// Protected by entrylock to ensure thread-safe lazy initialization.
func (d *dag) GetEntry(a addr) *dagentry {
	d.entrylock.Lock()
	e, ex := d.entries[maddr{a.idx, a.isGlobal}]
	if !ex {
		e = &dagentry{}
		e.m = make(chan struct{}, 1)
		n := &dagwnode{head: e}
		n.m = make(chan struct{}, 1)
		e.tail = n
		d.entries[maddr{a.idx, a.isGlobal}] = e
	}
	d.entrylock.Unlock()
	return e
}

// --- DAG node types ---

type dagnode struct {
	m         chan struct{}
	lockerid  int // pid+1 of lock holder (0 = unlocked)
	lockcount int
}

// Error types for panic-based lock contention signaling

type daglockerror struct {
	node *dagnode
}

func (d *daglockerror) Error() string {
	return fmt.Sprintf("DAG cell lock failure at %p", d.node)
}

type dagrunnableerror struct {
	p *pc
}

func (d *dagrunnableerror) Error() string {
	return fmt.Sprintf("DAG runnable assertion failure at pid %d", d.p.pid)
}

type pvrunnableerror struct {
	p *pc
}

func (d *pvrunnableerror) Error() string {
	return fmt.Sprintf("P/V operation assertion failure at pid %d", d.p.pid)
}

type varlockrunnableerror struct {
	p *pc
}

func (d *varlockrunnableerror) Error() string {
	return fmt.Sprintf("variable lock failure at pid %d", d.p.pid)
}

// doLockNode performs the actual lock and registers an unlock in p.dfunc.
func (n *dagnode) doLockNode(p *pc) {
	if cfg.DAGLockDebug {
		fmt.Printf("Locked node %p by proc (%d,%d) x%d\n", n, p.pid, p.pc, n.lockcount)
	}
	p.dfunc = append(p.dfunc, func() {
		if cfg.DAGLockDebug {
			fmt.Printf("Unlocking node %p by proc (%d,%d) x%d\n", n, p.pid, p.pc, n.lockcount)
		}
		n.lockcount--
		if n.lockcount == 0 {
			n.lockerid = 0
			<-n.m
		}
	})
	n.lockerid = p.pid + 1 // +1 so that 0 means "unlocked"
	n.lockcount++
}

// LockNode attempts to lock this DAG node for process p.
// Uses a priority-based protocol: higher-PID processes get longer timeouts.
func (n *dagnode) LockNode(p *pc) {
	// Re-entrant: same process can lock multiple times
	if n.lockerid == p.pid+1 {
		n.doLockNode(p)
		return
	}

	if cfg.DAGLockDebug {
		fmt.Printf("Locking node %p by proc (%d,%d)\n", n, p.pid, p.pc)
	}

	// Choose timeout based on priority (lower PID = lower priority = shorter timeout)
	timeout := lockTimeoutHigh
	if n.lockerid != 0 && n.lockerid < p.pid+1 {
		timeout = lockTimeoutLow
	}

	select {
	case n.m <- struct{}{}:
		n.doLockNode(p)
		return
	default:
		select {
		case n.m <- struct{}{}:
			n.doLockNode(p)
			return
		case <-time.After(timeout):
			if cfg.DAGLockDebug {
				fmt.Printf("Lock fail on node %p by proc (%d,%d)\n", n, p.pid, p.pc)
			}
			panic(daglockerror{n})
		}
	}
}

// --- DAG entry and node structures ---

type dagentry struct {
	dagnode
	lock sync.RWMutex
	tail *dagwnode
}

func (e *dagentry) RLock()          { e.lock.RLock() }
func (e *dagentry) TryRLock() bool  { return e.lock.TryRLock() }
func (e *dagentry) WLock()          { e.lock.Lock() }
func (e *dagentry) TryWLock() bool  { return e.lock.TryLock() }
func (e *dagentry) RUnlock()        { e.lock.RUnlock() }
func (e *dagentry) WUnlock()        { e.lock.Unlock() }

// dagwnode represents a write node in the annotation DAG.
type dagwnode struct {
	dagnode
	head *dagentry
	prev *dagwnode
	next *dagwnode
	node *dagpnode
	read *dagrnode
}

// dagrnode represents a read node in the annotation DAG.
type dagrnode struct {
	dagnode
	head *dagwnode
	node *dagpnode
	next *dagrnode
}

// dagpnode represents a process node in the annotation DAG.
type dagpnode struct {
	dagnode
	proc *pc
	prev *dagpnode
	next *dagpnode
	pc   int
	wt   *dagwnode
	rd   *dagprnode
}

// dagprnode links a process node to its read nodes.
type dagprnode struct {
	dagnode
	node *dagrnode
	next *dagprnode
}

// AddNextNodeFwd creates the next forward process node if it doesn't exist.
func (p *pc) AddNextNodeFwd() {
	if p.pnode == nil {
		p.pnode = &dagpnode{proc: p, prev: p.pnode, pc: p.pc}
		p.pnode.m = make(chan struct{}, 1)
		return
	}
	if p.pnode.next == nil {
		p.pnode.next = &dagpnode{proc: p, prev: p.pnode, pc: p.pc + 1}
		p.pnode.next.m = make(chan struct{}, 1)
	}
}

// AddWEdge adds a write edge to the DAG for the given address.
// Returns a deferred function that performs the actual mutation after lock release.
func (d *dag) AddWEdge(a addr, dest *pc) func() {
	destnode := dest.pnode
	destnode.LockNode(dest)
	entrynode := d.GetEntry(a)
	entrynode.LockNode(dest)
	tailnode := entrynode.tail
	tailnode.LockNode(dest)

	if tailnode.next == nil {
		return func() {
			if cfg.DAGDebug {
				if tailnode.node == nil {
					fmt.Printf("DAG WEdge Added BOT -> (%d,%d) of addr %s\n", dest.pid, dest.pc, a.String())
				} else {
					fmt.Printf("DAG WEdge Added (%d,%d) -> (%d,%d) of addr %s\n",
						tailnode.node.proc.pid, tailnode.node.pc, dest.pid, dest.pc, a.String())
				}
			}
			newnode := &dagwnode{head: tailnode.head, prev: tailnode, node: destnode}
			newnode.m = make(chan struct{}, 1)
			tailnode.next = newnode
			destnode.wt = newnode
		}
	}
	return func() {}
}

// AddREdge adds a read edge to the DAG for the given address.
func (d *dag) AddREdge(a addr, dest *pc) func() {
	destnode := dest.pnode
	destnode.LockNode(dest)
	enode := d.GetEntry(a)
	enode.LockNode(dest)
	tailnode := enode.tail
	tailnode.LockNode(dest)

	var n *dagrnode
	if tailnode.read != nil {
		n = tailnode.read
		n.LockNode(dest)
		for {
			if n.node == destnode {
				if cfg.DAGDebug {
					fmt.Printf("DAG REdge addition to (%d,%d) of %s prevented due to existing REdge\n",
						dest.pid, dest.pc, a.String())
				}
				return func() {}
			}
			if n.next == nil {
				break
			}
			n = n.next
			n.LockNode(dest)
		}
	}

	var b *dagprnode
	if destnode.rd != nil {
		b = destnode.rd
		b.LockNode(dest)
		for b.next != nil {
			b = b.next
			b.LockNode(dest)
		}
	}

	return func() {
		if n == nil {
			tailnode.read = &dagrnode{head: tailnode, node: destnode}
			tailnode.read.m = make(chan struct{}, 1)
			n = tailnode.read
		} else {
			n.next = &dagrnode{head: tailnode, node: destnode}
			n.next.m = make(chan struct{}, 1)
			n = n.next
		}
		if b != nil {
			b.next = &dagprnode{node: n}
			b.next.m = make(chan struct{}, 1)
		} else {
			destnode.rd = &dagprnode{node: n}
			destnode.rd.m = make(chan struct{}, 1)
		}
		if cfg.DAGDebug {
			if tailnode.node == nil {
				fmt.Printf("DAG REdge Added BOT -> (%d,%d) of addr %s\n", dest.pid, dest.pc, a.String())
			} else {
				fmt.Printf("DAG REdge Added (%d,%d) -> (%d,%d) of addr %s\n",
					tailnode.node.proc.pid, tailnode.node.pc, dest.pid, dest.pc, a.String())
			}
		}
	}
}

// AssertRunnable panics if the process cannot execute in the given direction.
func (p *pc) AssertRunnable(bwd bool) {
	if bwd {
		p.isRunnableBwd()
	} else {
		p.isRunnableFwd()
	}
}

// isRunnableBwd checks backward execution constraints in the DAG.
func (p *pc) isRunnableBwd() {
	n := p.pnode
	n.LockNode(p)

	if n.wt != nil {
		wn := n.wt
		wn.LockNode(p)
		if wn.next != nil {
			a := wn.next
			a.LockNode(p)
			wnn := a.node
			wnn.LockNode(p)
			if wnn.proc.pc > wnn.pc {
				panic(dagrunnableerror{p})
			}
		}
		if wn.read != nil {
			a := wn.read
			a.LockNode(p)
			for {
				rpn := a.node
				rpn.LockNode(p)
				if rpn.proc.pc > rpn.pc {
					panic(dagrunnableerror{p})
				}
				if a.next == nil {
					break
				}
				a = a.next
				a.LockNode(p)
			}
		}
	}

	if n.rd != nil {
		a := n.rd
		a.LockNode(p)
		for {
			rn := a.node
			rn.LockNode(p)
			wn := rn.head
			wn.LockNode(p)
			en := wn.head
			en.LockNode(p)
			if en.tail != wn {
				panic(dagrunnableerror{p})
			}
			if a.next == nil {
				break
			}
			a = a.next
		}
	}
}

// isRunnableFwd checks forward execution constraints in the DAG.
func (p *pc) isRunnableFwd() {
	pn := p.pnode
	pn.LockNode(p)

	if pn.wt != nil {
		wn := pn.wt
		wn.LockNode(p)
		if wn.prev != nil {
			pwn := wn.prev
			pwn.LockNode(p)
			ppn := pwn.node
			if ppn != nil {
				ppn.LockNode(p)
				if ppn.proc.pc < ppn.pc {
					panic(dagrunnableerror{p})
				}
			}
			if pwn.read != nil {
				a := pwn.read
				a.LockNode(p)
				for {
					orn := a.node
					orn.LockNode(p)
					if orn.proc.pc < orn.pc {
						panic(dagrunnableerror{p})
					}
					if a.next == nil {
						break
					}
					a = a.next
					a.LockNode(p)
				}
			}
		}
	}

	if pn.rd != nil {
		a := pn.rd
		a.LockNode(p)
		for {
			rn := a.node
			rn.LockNode(p)
			prn := rn.head
			prn.LockNode(p)
			ppn := prn.node
			if ppn != nil {
				ppn.LockNode(p)
				if ppn.proc.pc < ppn.pc {
					panic(dagrunnableerror{p})
				}
			}
			if a.next == nil {
				break
			}
			a = a.next
			a.LockNode(p)
		}
	}
}

func newDag() *dag {
	return &dag{entries: make(map[maddr]*dagentry)}
}

func (r *runtime) resetDag() {
	r.dag = *newDag()
	r.rootpc.Propagate(func(p *pc) { p.pnode.next = nil })
}

// PrintDag displays the annotation DAG to stdout.
func (r *runtime) PrintDag() {
	skeys := slices.SortedFunc(maps.Keys(r.dag.entries), func(a maddr, b maddr) int {
		if a.isGlobal && !b.isGlobal {
			return -1
		}
		if !a.isGlobal && b.isGlobal {
			return 1
		}
		return a.idx - b.idx
	})

	for _, a := range skeys {
		kind := "V"
		if a.isGlobal {
			kind = "M"
		}
		fmt.Printf("Address: %s[%d], ", kind, a.idx)
		b := r.dag.entries[a].tail
		if b.node == nil {
			fmt.Printf("Tail at BOT\n")
		} else {
			fmt.Printf("Tail at (%d,%d)\n", b.node.proc.pid, b.node.pc)
		}
		for b.prev != nil {
			b = b.prev
		}
		for {
			if b.node == nil {
				fmt.Print("BOT |")
			} else {
				fmt.Printf("(%d,%d) |", b.node.proc.pid, b.node.pc)
			}
			if b.read != nil {
				a := b.read
				for {
					fmt.Printf(" (%d,%d)", a.node.proc.pid, a.node.pc)
					if a.next == nil {
						break
					}
					a = a.next
				}
			}
			fmt.Print("\n")
			if b.next == nil {
				break
			}
			b = b.next
		}
	}
	fmt.Print("\n")
}
