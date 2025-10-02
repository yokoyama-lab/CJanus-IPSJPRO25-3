package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

//Memory system

// transformation is done in following order
// variable name (string, can contain #,$,@), runtime and pc
//
// pc (to get addr from), isGlobal (decide global/local mapping to get addr from), name (does not contain str)
//
// name runtime and pc -> addrspec -> addr -> value

type heapmemory struct{
	values []int
	expansionLock sync.RWMutex //Read and writes to the slice can be concurrent(RLock), Expansion must be exclusive
}

func newHeapMemory() *heapmemory{
	return &heapmemory{make([]int, 0),sync.RWMutex{}}
}

func (h *heapmemory) get(index int) int {
	if len(h.values) <= index {
		h.expansionLock.Lock()
		for len(h.values) <= index{
			h.values = append(h.values, 0)
		}
		h.expansionLock.Unlock()
	}
	h.expansionLock.RLock()
	val := h.values[index]
	h.expansionLock.RUnlock()
	return val
}

func (h *heapmemory) set(index int, value int){
	if len(h.values) <= index {
		h.expansionLock.Lock()
		for len(h.values) <= index{
			h.values = append(h.values, 0)
		}
		h.expansionLock.Unlock()
	}
	h.expansionLock.RLock()
	h.values[index] = value
	h.expansionLock.RUnlock()
}


type addr struct{
	idx int
	isGlobal bool //if true, refers to M[idx], otherwise refers varmem
	sym string
	treepid string
}

func (a addr) EqualsMem(b addr) bool{
	return a.idx == b.idx && a.isGlobal == b.isGlobal
}

func (a addr) toString() string{
	if a.isGlobal{
		return fmt.Sprintf("M[%d]",a.idx)
	}
	return fmt.Sprintf("V[%d]",a.idx)
}

func (a addr) toSymName() string{
	if a.treepid == ""{
		return a.sym
	}
	return a.sym + "@" +a.treepid
}


type symtab struct {
	//Value of type varentry
	alloctable map[string]addr //Global only
	alloclock *sync.RWMutex //Global only
	heapmem *heapmemory
	varmem *heapmemory
	alclimit int
	alclimlock *sync.Mutex
}


func newSymtab() *symtab {
	tab := symtab{}
	tab.alloctable = make(map[string]addr)
	tab.alloclock = &sync.RWMutex{}
	tab.heapmem = newHeapMemory()
	tab.varmem = newHeapMemory()
	tab.alclimit = 1
	tab.alclimlock = &sync.Mutex{}
	return &tab
}
func (r *runtime) TryV(p *pc, adr addr){
	if r.ReadAddr(adr,p) != 0{
		panic(pvrunnableerror{p})
	}

}
func (r *runtime) TryP(p *pc, adr addr){
	if r.ReadAddr(adr,p) != 1{
		panic(pvrunnableerror{p})
	}
	
}
func (r *runtime) LockAddr(p *pc, a addr){
	//Locks the address to be read-only until unlock occurs
	//Single address can be locked mutiple times
	if LOCK_DEBUG {
		fmt.Printf("RLocking %s\n",a.toString())	
	}
	e := r.dag.GetEntry(a)
	e.LockNode(p)
	if e.TryRLock(){
		if LOCK_DEBUG {
			fmt.Printf("RLock of %s passed\n",a.toString())	
		}
		return
	}
	if LOCK_DEBUG {
		fmt.Printf("RLock of %s failed\n",a.toString())	
	}
	panic(varlockrunnableerror{p})
		
}
func (r *runtime) UnlockAddr(p *pc, a addr){
	//Unlocks the address, allowing write if no other process is locking it
	r.dag.GetEntry(a).RUnlock()
	if LOCK_DEBUG {
		fmt.Printf("RUnlocked %s\n",a.toString())	
	}
}
func (r *runtime) WLockAddr(p *pc, a addr){
	//Waits until the address is unlocked
	if LOCK_DEBUG {
		fmt.Printf("WLocking %s\n",a.toString())	
	}
	e := r.dag.GetEntry(a)
	e.LockNode(p)
	if e.TryWLock(){
		if LOCK_DEBUG {
			fmt.Printf("WLock of %s passed\n",a.toString())	
		}
		p.dfunc = append(p.dfunc, e.WUnlock)
		return
	}
	if LOCK_DEBUG {
		fmt.Printf("WLock of %s failed\n",a.toString())	
	}
	panic(varlockrunnableerror{p})
}

func (r *runtime) WUnlockAddr(p *pc, a addr){
	r.dag.GetEntry(a).WUnlock()
	if LOCK_DEBUG {
		fmt.Printf("WUnlocked %s\n",a.toString())	
	}
}

func (r *runtime) GetMem(p *pc, isGlobal bool) *heapmemory{
	if isGlobal{
		return r.heapmem
	}
	return r.varmem
}

func (r *runtime) AllocSym(p *pc, isGlobal bool, s string, size int) int{
	//allocate new entry in alloctable, does not update pid and pc
	l := r.alclimit
	r.alloclock.Lock()
	r.alloctable[s] = addr{l,false,s,p.StringfyPid()}
	r.alloclock.Unlock()
	for i := range size{
		//do it from latter to reduce allocation overhead
		r.varmem.set(l+size-i-1,0)
	}
	r.alclimit += size
	return l
}

func (r *runtime) ReadAddr(a addr, pc *pc) int{
	mem := r.GetMem(pc,a.isGlobal)
	v := mem.get(a.idx)
	if VAR_DEBUG {
		fmt.Printf("ReadOp: %s -> %d\n",a.toString(),v)
	}
	return v
}
func (r *runtime) WriteAddr(a addr, val int, pc *pc){
	mem := r.GetMem(pc,a.isGlobal)
	mem.set(a.idx,val)
	if VAR_DEBUG {
		fmt.Printf("WriteOp: %s -> %d\n",a.toString(),val)
	}
}

func (r *runtime) SetSym(dst string, src string, pc *pc){
	srca,df := r.GetAddrOfAllocable(src,pc)
	r.SetAddr(dst,srca,pc)
	df()
}
func (r *runtime) UnsetSym(dst string, src string, pc *pc){
	srca,df := r.GetAddrOfAllocable(src,pc)
	r.UnsetAddr(dst,srca,pc)
	df()
}
func (r *runtime) ReadSym(s string, pc *pc) int {
	adr := r.GetAddr(s,pc)
	v := r.ReadAddr(adr,pc)
	return v
}
func (r *runtime) WriteSym(s string, value int, pc *pc) {
	adr := r.GetAddr(s,pc)
	r.WriteAddr(adr,value,pc)
}

var heapreg = regexp.MustCompile(`^M\[([\w\d]+)\]$`)
var varreg = regexp.MustCompile(`^(\w[\w\d]*)(?:\[([\w\d]+)\])?$`)
var annotreg = regexp.MustCompile(`^([\$\#]?)(\w[\w\d]*)((?:\@\d+)?)$`)

//name is in the form M[x], x[y], x or $x@0
//no space allowed
//prefix : $ is local alloction, # is global allocation, search allocated symbol otherwise
//suffix : @1 means it should be allocated to localtable of 1st child process 
func (r *runtime) GetAddrOfAllocable(name string, pc *pc) (addr,func()){
	if m := annotreg.FindStringSubmatch(name); m != nil{
		if m[3] == "" && m[1] == ""{
			return r.GetAddr(name,pc),func() {}
		}
		if m[3] != ""{
			spid,ex := strings.CutPrefix(m[3],"@")
			if ex{
				pid,_ := strconv.Atoi(spid)
				cpc := r.getChildPC(pc,pid)
				pc = cpc
			}
		}
		switch m[1] {
		case "$":
			if r.IsAllocatedLocal(m[2],pc) {
				ads,ex := pc.localtable[m[2]]
				if ex && len(ads) != 0{
					ad := ads[len(pc.localtable[m[2]])-1].addr
					//a := m[2]
					return ad, func(){}
					//return ad, func(){r.DeleteLocal(a,pc)}
				}	
				panic("Deallocation of local variable failed becase it does not exist")
			} else {
				ad := r.GetNewLocalAddr(m[2],pc)
				r.RegisterLocal(m[2],ad,pc)
				return ad,func(){}
			}
		case "#":
			if r.IsAllocatedGlobal(m[2]) {
				r.alloclock.RLock()
				ad,ex := r.alloctable[m[2]]
				r.alloclock.RUnlock()
				if ex{
					//a := m[2]
					return ad, func() {}
					//return ad,func() {r.DeleteGlobal(a)}
				}
				panic("Deallocation of global variable failed becase it does not exist")
			} else {
				ad := addr{}
				r.RegisterGlobal(m[2],ad)
				return ad,func() {}
			}
		default:
			return r.GetAddrOfName(m[2],pc),func() {}
		}
	} else {
		return r.GetAddr(name,pc),func() {}
	}
}

func (r *runtime) GetAddr(name string, pc *pc) addr{
	if m := heapreg.FindStringSubmatch(name); m != nil{
		index,_ := strconv.Atoi(m[1])
		return addr{index,true,"M",pc.StringfyPid()}
	} else if m := varreg.FindStringSubmatch(name); m != nil{
		index := 0
		if len(m) == 3 && m[2] != ""{
			var er error
			index,er = strconv.Atoi(m[2])
			if er != nil{
				index = r.ReadSym(m[2],pc)
			}
		}
		adr := r.GetAddrOfName(m[1],pc)
		return addr{adr.idx+index,adr.isGlobal,m[1],pc.StringfyPid()}
	}
	return r.GetAddrOfName(name,pc)
}

func (r *runtime) GetNewLocalAddr(sym string, p *pc) addr {
	r.alclimlock.Lock()
	a := r.alclimit
	r.alclimit++
	r.alclimlock.Unlock()
	return addr{a,false,sym,p.StringfyPid()}
}

//true if exists
func (r *runtime) GetAddrOfName(name string, pc *pc) addr{
	ads,ex := pc.localtable[name]
	if ex && len(ads) != 0{
		return ads[len(pc.localtable[name])-1].addr
	}	
	r.alloclock.RLock()
	ad,ex := r.alloctable[name]
	r.alloclock.RUnlock()
	if ex{
		return ad
	}
	panic("Tried to reference nonexistent variable")
}

//set addr of name, allocate if nessesary, no deallocation allowed
func (r *runtime) SetAddr(name string, ad addr, pc *pc){
	if m := annotreg.FindStringSubmatch(name); m != nil{
		if m[3] != ""{
			spid,ex := strings.CutPrefix(m[3],"@")
			if ex{
				pid,_ := strconv.Atoi(spid)
				cpc := r.getChildPC(pc,pid)
				pc = cpc
			}
		}
		switch m[1] {
		case "$":
			if r.IsAllocatedLocal(m[2],pc) {	
				//panic("Deallocation of local variable failed because it is used in set instruction")
			} else {
				r.RegisterLocal(m[2],ad,pc)
				return
			}
		case "#":
			if r.IsAllocatedGlobal(m[2]) {
				//panic("Deallocation of global variable failed because it is used in set instruction")
			} else {
				r.RegisterGlobal(m[2],ad)
				return
			}
		default:
			panic("Allocation of variable used in set instruction failed because it already exists")
		}
	}
	//panic("Allocation of variable used in set instruction failed because it already exists")
}

//check value of addr is zero and unbind addr from name, deallocate if nessesary, no allocation allowed
func (r *runtime) UnsetAddr(name string, ad addr, pc *pc){
	if m := annotreg.FindStringSubmatch(name); m != nil{
		if m[3] != ""{
			spid,ex := strings.CutPrefix(m[3],"@")
			if ex{
				pid,_ := strconv.Atoi(spid)
				cpc := r.getChildPC(pc,pid)
				pc = cpc
			}
		}
		switch m[1] {
		case "$":
			if r.IsAllocatedLocal(m[2],pc) {
				// if !ad.isGlobal {
				// 	val := r.ReadAddrIf(ad,pc,func(i int) bool {return false})
				// 	if val != 0{
				// 		panic("Unset of non-zero local variable")
				// 	}
				// }
				//r.DeleteLocal(m[2],pc)
				return
			} else {
				panic("Allocation of local variable failed because it is used in unset instruction")
			}
		case "#":
			if r.IsAllocatedGlobal(m[2]) {
				val := r.ReadAddr(ad,pc)
				if val != 0{
					panic("Unset of non-zero global variable")
				}
				//r.DeleteGlobal(m[2])
				return
			} else {
				panic("Allocation of global variable failed because it is used in unset instruction")
			}
		default:
			panic("Deallocation of variable used in unset instruction failed because it does not allocate on backwards")
		}
	}
	panic("Deallocation of variable used in unset instruction failed because it does not allocate on backwards")
}


func (r *runtime) IsAllocatedGlobal(name string) bool {
	r.alloclock.RLock()
	_,ex := r.alloctable[name]
	r.alloclock.RUnlock()
	return ex
}

func (r *runtime) RegisterGlobal(name string, addr addr){
	r.alloclock.Lock()
	defer r.alloclock.Unlock()
	_,ex := r.alloctable[name]
	if !ex{
		r.alloctable[name] = addr
		return
	}
	panic("Tried to create alredy existing global varable")
}

func (r *runtime) DeleteGlobal(name string){
	r.alloclock.Lock()
	defer r.alloclock.Unlock()
	_,ex := r.alloctable[name]
	if !ex{
		panic("Tried to delete nonexistent global variables")
	}
	delete(r.alloctable,name)
}

func (r *runtime) IsAllocatedLocal(name string, pc *pc) bool {
	ads,ex := pc.localtable[name]
	if ex && len(ads) != 0{
		return ads[len(pc.localtable[name])-1].nest == pc.nest
	}
	return false
}

func (r *runtime) RegisterLocal(name string, addr addr, pc *pc){
	ads,ex := pc.localtable[name]
	if !ex || len(pc.localtable[name]) == 0{
		pc.localtable[name] = make([]localaddr, 0)
	} else {
		if ads[len(pc.localtable[name])-1].nest == pc.nest{
			panic("Tried to create alredy existing local varable")
		}
	}
	pc.localtable[name] = append(pc.localtable[name], localaddr{addr,pc.nest})
}

func (r *runtime) DeleteLocal(name string, pc *pc){
	ads,ex := pc.localtable[name]
	if !ex || ads[len(pc.localtable[name])-1].nest != pc.nest{
		panic("Tried to delete nonexistent local variables")
	}
	pc.localtable[name] = pc.localtable[name][:len(pc.localtable[name])-1]
}

func (tab *symtab) PrintSym() {
	fmt.Print("\n---Memory Status---\n")
	fmt.Print("[")
	for i,v := range tab.heapmem.values{
		if i != 0 {
			fmt.Print(",")
		}
		fmt.Print(v)
	}
	fmt.Print("]\n")
}
