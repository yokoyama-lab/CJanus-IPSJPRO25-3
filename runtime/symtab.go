package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Memory system
//
// Address resolution order:
//   variable name (string) + runtime + pc
//   → pc + isGlobal + name (stripped of prefixes)
//   → addr
//   → value

// heapmemory is a dynamically-expanding integer array with concurrent access support.
type heapmemory struct {
	values        []int
	expansionLock sync.RWMutex // reads can be concurrent; expansion must be exclusive
}

func newHeapMemory() *heapmemory {
	return &heapmemory{values: make([]int, 0)}
}

func (h *heapmemory) get(index int) int {
	if len(h.values) <= index {
		h.expansionLock.Lock()
		for len(h.values) <= index {
			h.values = append(h.values, 0)
		}
		h.expansionLock.Unlock()
	}
	h.expansionLock.RLock()
	val := h.values[index]
	h.expansionLock.RUnlock()
	return val
}

func (h *heapmemory) set(index int, value int) {
	if len(h.values) <= index {
		h.expansionLock.Lock()
		for len(h.values) <= index {
			h.values = append(h.values, 0)
		}
		h.expansionLock.Unlock()
	}
	h.expansionLock.RLock()
	h.values[index] = value
	h.expansionLock.RUnlock()
}

// addr represents an address in the CJanus memory system.
type addr struct {
	idx      int
	isGlobal bool   // true=M[idx] (heap), false=V[idx] (var)
	sym      string // original symbol name
	treepid  string // process tree path
}

func (a addr) EqualsMem(b addr) bool {
	return a.idx == b.idx && a.isGlobal == b.isGlobal
}

func (a addr) String() string {
	if a.isGlobal {
		return fmt.Sprintf("M[%d]", a.idx)
	}
	return fmt.Sprintf("V[%d]", a.idx)
}

func (a addr) toSymName() string {
	if a.treepid == "" {
		return a.sym
	}
	return a.sym + "@" + a.treepid
}

// symtab manages global and local variable allocation and lookup.
type symtab struct {
	alloctable map[string]addr    // global variable allocation table
	alloclock  *sync.RWMutex     // protects alloctable
	heapmem    *heapmemory       // global heap memory (M[])
	varmem     *heapmemory       // local variable memory (V[])
	alclimit   int               // next free address index
	alclimlock *sync.Mutex       // protects alclimit
}

func newSymtab() *symtab {
	return &symtab{
		alloctable: make(map[string]addr),
		alloclock:  &sync.RWMutex{},
		heapmem:    newHeapMemory(),
		varmem:     newHeapMemory(),
		alclimit:   1,
		alclimlock: &sync.Mutex{},
	}
}

// --- Semaphore operations ---

func (r *runtime) TryV(p *pc, adr addr) {
	if r.ReadAddr(adr, p) != 0 {
		panic(pvrunnableerror{p})
	}
}

func (r *runtime) TryP(p *pc, adr addr) {
	if r.ReadAddr(adr, p) != 1 {
		panic(pvrunnableerror{p})
	}
}

// --- Variable locking ---

func (r *runtime) LockAddr(p *pc, a addr) {
	if cfg.LockDebug {
		fmt.Printf("RLocking %s\n", a.String())
	}
	e := r.dag.GetEntry(a)
	e.LockNode(p)
	if e.TryRLock() {
		if cfg.LockDebug {
			fmt.Printf("RLock of %s passed\n", a.String())
		}
		return
	}
	if cfg.LockDebug {
		fmt.Printf("RLock of %s failed\n", a.String())
	}
	panic(varlockrunnableerror{p})
}

func (r *runtime) UnlockAddr(p *pc, a addr) {
	r.dag.GetEntry(a).RUnlock()
	if cfg.LockDebug {
		fmt.Printf("RUnlocked %s\n", a.String())
	}
}

func (r *runtime) WLockAddr(p *pc, a addr) {
	if cfg.LockDebug {
		fmt.Printf("WLocking %s\n", a.String())
	}
	e := r.dag.GetEntry(a)
	e.LockNode(p)
	if e.TryWLock() {
		if cfg.LockDebug {
			fmt.Printf("WLock of %s passed\n", a.String())
		}
		p.dfunc = append(p.dfunc, e.WUnlock)
		return
	}
	if cfg.LockDebug {
		fmt.Printf("WLock of %s failed\n", a.String())
	}
	panic(varlockrunnableerror{p})
}

func (r *runtime) WUnlockAddr(p *pc, a addr) {
	r.dag.GetEntry(a).WUnlock()
	if cfg.LockDebug {
		fmt.Printf("WUnlocked %s\n", a.String())
	}
}

// --- Memory access ---

func (r *runtime) GetMem(p *pc, isGlobal bool) *heapmemory {
	if isGlobal {
		return r.heapmem
	}
	return r.varmem
}

func (r *runtime) AllocSym(p *pc, isGlobal bool, s string, size int) int {
	l := r.alclimit
	r.alloclock.Lock()
	r.alloctable[s] = addr{l, false, s, p.StringfyPid()}
	r.alloclock.Unlock()
	for i := range size {
		r.varmem.set(l+size-i-1, 0)
	}
	r.alclimit += size
	return l
}

func (r *runtime) ReadAddr(a addr, pc *pc) int {
	mem := r.GetMem(pc, a.isGlobal)
	v := mem.get(a.idx)
	if cfg.VarDebug {
		fmt.Printf("ReadOp: %s -> %d\n", a.String(), v)
	}
	return v
}

func (r *runtime) WriteAddr(a addr, val int, pc *pc) {
	mem := r.GetMem(pc, a.isGlobal)
	mem.set(a.idx, val)
	if cfg.VarDebug {
		fmt.Printf("WriteOp: %s -> %d\n", a.String(), val)
	}
}

// --- Symbol operations ---

func (r *runtime) SetSym(dst string, src string, pc *pc) {
	srca, df := r.GetAddrOfAllocable(src, pc)
	r.SetAddr(dst, srca, pc)
	df()
}

func (r *runtime) UnsetSym(dst string, src string, pc *pc) {
	srca, df := r.GetAddrOfAllocable(src, pc)
	r.UnsetAddr(dst, srca, pc)
	df()
}

func (r *runtime) ReadSym(s string, pc *pc) int {
	adr := r.GetAddr(s, pc)
	return r.ReadAddr(adr, pc)
}

func (r *runtime) WriteSym(s string, value int, pc *pc) {
	adr := r.GetAddr(s, pc)
	r.WriteAddr(adr, value, pc)
}

// Pre-compiled regexes for address resolution
var (
	heapreg  = regexp.MustCompile(`^M\[([\w\d]+)\]$`)
	varreg   = regexp.MustCompile(`^(\w[\w\d]*)(?:\[([\w\d]+)\])?$`)
	annotreg = regexp.MustCompile(`^([\$\#]?)(\w[\w\d]*)((?:\@\d+)?)$`)
)

// GetAddrOfAllocable resolves a name that may contain allocation prefixes ($/#) and
// process suffixes (@N). Returns the address and a cleanup function.
//
// Name format: [prefix]name[@childpid]
//   prefix: $ = local allocation, # = global allocation, none = lookup
//   suffix: @N = allocate in Nth child process
func (r *runtime) GetAddrOfAllocable(name string, pc *pc) (addr, func()) {
	m := annotreg.FindStringSubmatch(name)
	if m == nil {
		return r.GetAddr(name, pc), func() {}
	}

	if m[3] == "" && m[1] == "" {
		return r.GetAddr(name, pc), func() {}
	}

	if m[3] != "" {
		spid, ex := strings.CutPrefix(m[3], "@")
		if ex {
			pid, _ := strconv.Atoi(spid)
			pc = r.getChildPC(pc, pid)
		}
	}

	switch m[1] {
	case "$":
		if r.IsAllocatedLocal(m[2], pc) {
			ads, ex := pc.localtable[m[2]]
			if ex && len(ads) != 0 {
				return ads[len(ads)-1].addr, func() {}
			}
			panic("deallocation of local variable failed: does not exist")
		}
		ad := r.GetNewLocalAddr(m[2], pc)
		r.RegisterLocal(m[2], ad, pc)
		return ad, func() {}

	case "#":
		if r.IsAllocatedGlobal(m[2]) {
			r.alloclock.RLock()
			ad, ex := r.alloctable[m[2]]
			r.alloclock.RUnlock()
			if ex {
				return ad, func() {}
			}
			panic("deallocation of global variable failed: does not exist")
		}
		ad := addr{}
		r.RegisterGlobal(m[2], ad)
		return ad, func() {}

	default:
		return r.GetAddrOfName(m[2], pc), func() {}
	}
}

// GetAddr resolves a variable name to an address.
// Supports: M[x] (heap), x[y] (array), x (simple), $x@N (annotated).
func (r *runtime) GetAddr(name string, pc *pc) addr {
	if m := heapreg.FindStringSubmatch(name); m != nil {
		index, _ := strconv.Atoi(m[1])
		return addr{index, true, "M", pc.StringfyPid()}
	}
	if m := varreg.FindStringSubmatch(name); m != nil {
		index := 0
		if len(m) == 3 && m[2] != "" {
			var er error
			index, er = strconv.Atoi(m[2])
			if er != nil {
				index = r.ReadSym(m[2], pc)
			}
		}
		adr := r.GetAddrOfName(m[1], pc)
		return addr{adr.idx + index, adr.isGlobal, m[1], pc.StringfyPid()}
	}
	return r.GetAddrOfName(name, pc)
}

func (r *runtime) GetNewLocalAddr(sym string, p *pc) addr {
	r.alclimlock.Lock()
	a := r.alclimit
	r.alclimit++
	r.alclimlock.Unlock()
	return addr{a, false, sym, p.StringfyPid()}
}

// GetAddrOfName looks up a variable by name: first in local table, then global.
func (r *runtime) GetAddrOfName(name string, pc *pc) addr {
	ads, ex := pc.localtable[name]
	if ex && len(ads) != 0 {
		return ads[len(ads)-1].addr
	}
	r.alloclock.RLock()
	ad, ex := r.alloctable[name]
	r.alloclock.RUnlock()
	if ex {
		return ad
	}
	panic("tried to reference nonexistent variable: " + name)
}

// --- Address binding (set/unset) ---

func (r *runtime) SetAddr(name string, ad addr, pc *pc) {
	m := annotreg.FindStringSubmatch(name)
	if m == nil {
		return
	}
	if m[3] != "" {
		spid, ex := strings.CutPrefix(m[3], "@")
		if ex {
			pid, _ := strconv.Atoi(spid)
			pc = r.getChildPC(pc, pid)
		}
	}
	switch m[1] {
	case "$":
		if !r.IsAllocatedLocal(m[2], pc) {
			r.RegisterLocal(m[2], ad, pc)
		}
	case "#":
		if !r.IsAllocatedGlobal(m[2]) {
			r.RegisterGlobal(m[2], ad)
		}
	default:
		panic("set instruction requires $ or # prefix for allocation")
	}
}

func (r *runtime) UnsetAddr(name string, ad addr, pc *pc) {
	m := annotreg.FindStringSubmatch(name)
	if m == nil {
		panic("unset instruction requires annotated variable name")
	}
	if m[3] != "" {
		spid, ex := strings.CutPrefix(m[3], "@")
		if ex {
			pid, _ := strconv.Atoi(spid)
			pc = r.getChildPC(pc, pid)
		}
	}
	switch m[1] {
	case "$":
		if r.IsAllocatedLocal(m[2], pc) {
			return
		}
		panic("unset of local variable failed: not allocated")
	case "#":
		if r.IsAllocatedGlobal(m[2]) {
			val := r.ReadAddr(ad, pc)
			if val != 0 {
				panic("unset of non-zero global variable")
			}
			return
		}
		panic("unset of global variable failed: not allocated")
	default:
		panic("unset instruction requires $ or # prefix")
	}
}

// --- Allocation queries and registration ---

func (r *runtime) IsAllocatedGlobal(name string) bool {
	r.alloclock.RLock()
	_, ex := r.alloctable[name]
	r.alloclock.RUnlock()
	return ex
}

func (r *runtime) RegisterGlobal(name string, addr addr) {
	r.alloclock.Lock()
	defer r.alloclock.Unlock()
	if _, ex := r.alloctable[name]; ex {
		panic("tried to create already existing global variable: " + name)
	}
	r.alloctable[name] = addr
}

func (r *runtime) DeleteGlobal(name string) {
	r.alloclock.Lock()
	defer r.alloclock.Unlock()
	if _, ex := r.alloctable[name]; !ex {
		panic("tried to delete nonexistent global variable: " + name)
	}
	delete(r.alloctable, name)
}

func (r *runtime) IsAllocatedLocal(name string, pc *pc) bool {
	ads, ex := pc.localtable[name]
	if ex && len(ads) != 0 {
		return ads[len(ads)-1].nest == pc.nest
	}
	return false
}

func (r *runtime) RegisterLocal(name string, addr addr, pc *pc) {
	ads, ex := pc.localtable[name]
	if !ex || len(ads) == 0 {
		pc.localtable[name] = make([]localaddr, 0)
	} else {
		if ads[len(ads)-1].nest == pc.nest {
			panic("tried to create already existing local variable: " + name)
		}
	}
	pc.localtable[name] = append(pc.localtable[name], localaddr{addr, pc.nest})
}

func (r *runtime) DeleteLocal(name string, pc *pc) {
	ads, ex := pc.localtable[name]
	if !ex || ads[len(ads)-1].nest != pc.nest {
		panic("tried to delete nonexistent local variable: " + name)
	}
	pc.localtable[name] = pc.localtable[name][:len(pc.localtable[name])-1]
}

func (tab *symtab) PrintSym() {
	fmt.Print("\n---Memory Status---\n")
	fmt.Print("[")
	for i, v := range tab.heapmem.values {
		if i != 0 {
			fmt.Print(",")
		}
		fmt.Print(v)
	}
	fmt.Print("]\n")
}
