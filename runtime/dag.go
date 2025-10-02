package main

import (
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"
)

type dag struct{
	entries map[maddr]*dagentry
	entrylock sync.Mutex
}

type maddr struct{
	idx int
	isGlobal bool
}

func (a *maddr)String()string{
	return fmt.Sprintf("%s[%d]\n",func()string{if a.isGlobal {return "M"}; return "V"}(),a.idx)
}

func (d *dag) GetEntry(a addr) *dagentry{
	d.entrylock.Lock()
	e,ex := d.entries[maddr{a.idx,a.isGlobal}]
	if !ex {
		e = &dagentry{}
		e.m = make(chan struct{},1)
		n := &dagwnode{head: e}
		n.m = make(chan struct{},1)
		e.tail = n
		d.entries[maddr{a.idx,a.isGlobal}] = e
	}
	d.entrylock.Unlock()
	return e
}

type dagnode struct{
	m chan struct{}
	lockerid int
	lockcount int
}

type daglockerror struct{
	node *dagnode
}

func (d *daglockerror) Error() string{
	return fmt.Sprintf("DAG Cell Lock Failure at %p",d.node)
}

type dagrunnableerror struct{
	p *pc
}

func (d *dagrunnableerror) Error() string{
	return fmt.Sprintf("DAG Runnable Assetion Failure at pid %d",d.p.pid)
}

type pvrunnableerror struct{
	p *pc
}

func (d *pvrunnableerror) Error() string{
	return fmt.Sprintf("P/V Operation Assetion Failure at pid %d",d.p.pid)
}

type varlockrunnableerror struct{
	p *pc
}

func (d *varlockrunnableerror) Error() string{
	return fmt.Sprintf("Variable Lock Failure at pid %d",d.p.pid)
}

func (n *dagnode)doLockNode(p *pc) {
	if DAGLOCK_DEBUG {
		fmt.Printf("Locked node %p by proc (%d,%d) x%d\n",n,p.pid,p.pc,n.lockcount)
	}
	p.dfunc = append(p.dfunc, func() {
		if DAGLOCK_DEBUG {
			fmt.Printf("Unlocking node %p by proc (%d,%d) x%d\n",n,p.pid,p.pc,n.lockcount)
		}
		n.lockcount--
		if n.lockcount == 0 {
			n.lockerid = 0
			<- n.m
		}
	})
	n.lockerid = p.pid+1
	n.lockcount++
}

func (n *dagnode)LockNode(p *pc) {
	if n.lockerid == p.pid+1 {
		n.doLockNode(p)
		return
	}
	if DAGLOCK_DEBUG {
		fmt.Printf("Locking node %p by proc (%d,%d)\n",n,p.pid,p.pc)
	}
	if n.lockerid != 0 && n.lockerid < p.pid+1{
		select {
		case n.m <- struct{}{}:
			n.doLockNode(p)
			return
		default:
			select {
			case n.m <- struct{}{}:
				n.doLockNode(p)
				return
			case <- time.After(1*time.Microsecond):
				if DAGLOCK_DEBUG {
					fmt.Printf("Lock Fail on node %p by proc (%d,%d)\n",n,p.pid,p.pc)
				}
				panic(daglockerror{n})
			}
		}
	} else {
		if DAGLOCK_DEBUG {
			fmt.Printf("Prioritized Lock of node %p by proc (%d,%d) x%d\n",n,p.pid,p.pc,n.lockcount)
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
			case <- time.After(100*time.Microsecond):
				if DAGLOCK_DEBUG {
					fmt.Printf("Lock Fail on node %p by proc (%d,%d)\n",n,p.pid,p.pc)
				}
				panic(daglockerror{n})
			}
		}
	}
}

type dagentry struct{
	dagnode
	lock sync.RWMutex
	tail *dagwnode
}

func (e *dagentry) RLock(){
	e.lock.RLock()
}

func (e *dagentry) TryRLock() bool {
	return e.lock.TryRLock()
}

func (e *dagentry) WLock(){
	e.lock.Lock()
}

func (e *dagentry) TryWLock() bool {
	return e.lock.TryLock()
}

func (e *dagentry) RUnlock(){
	e.lock.RUnlock()
}

func (e *dagentry) WUnlock(){	
	e.lock.Unlock()
}


type dagwnode struct{
	dagnode
	head *dagentry
	prev *dagwnode
	next *dagwnode
	node *dagpnode
	read *dagrnode
}


type dagrnode struct{
	dagnode
	head *dagwnode
	node *dagpnode
	next *dagrnode
}


type dagpnode struct{
	dagnode
	proc *pc
	prev *dagpnode
	next *dagpnode
	pc int
	wt *dagwnode
	rd *dagprnode
}



type dagprnode struct{
	dagnode
	node *dagrnode
	next *dagprnode
}


func (p *pc) AddNextNodeFwd(){
	if p.pnode == nil {
		p.pnode = &dagpnode{proc: p, prev: p.pnode, pc: p.pc}
		p.pnode.m = make(chan struct{},1)
		return
	}
	if p.pnode.next == nil {
		p.pnode.next = &dagpnode{proc: p, prev: p.pnode, pc: p.pc+1}
		p.pnode.next.m = make(chan struct{},1)
	}
}

// func (p *pc) LockNextNode(){
// 	//TODO
// }


func (d *dag) AddWEdge(a addr, dest *pc) func(){
	//Node to be locked
	//current pnode
	//writing symbol's entry and tail node
	destnode := dest.pnode
	destnode.LockNode(dest)
	entrynode := d.GetEntry(a)
	entrynode.LockNode(dest)
	tailnode := entrynode.tail
	tailnode.LockNode(dest)
	if tailnode.next == nil {
		return func(){
			if DAG_DEBUG {
				if tailnode.node == nil{
					fmt.Printf("DAG WEdge Added BOT -> (%d,%d) of addr %s\n",dest.pid,dest.pc,a.toString())
				} else {
					fmt.Printf("DAG WEdge Added (%d,%d) -> (%d,%d) of addr %s\n",tailnode.node.proc.pid,tailnode.node.pc,dest.pid,dest.pc,a.toString())
				}
			}
			newnode := &dagwnode{head: tailnode.head, prev: tailnode, node: destnode}
			newnode.m = make(chan struct{},1)
			tailnode.next = newnode
			destnode.wt = newnode
		}
	} else {
		return func() {}
		// if destnode != tailnode.next.node{
		// 	panic(fmt.Errorf("Tried to add write edges to (%d,%d) from already exiting node (%d,%d)",destnode.proc.pid,destnode.pc,tailnode.node.proc.pid,tailnode.node.pc))
		// }
	}
}

func (d *dag) AddREdge(a addr, dest *pc) func(){
	//Node to be locked
	//Read symbol's entry
	destnode := dest.pnode
	destnode.LockNode(dest)
	enode := d.GetEntry(a)
	enode.LockNode(dest)
	tailnode := enode.tail
	tailnode.LockNode(dest)
	var n *dagrnode = nil
	if tailnode.read != nil {
		n = tailnode.read
		n.LockNode(dest)
		for {
			if n.node == destnode{
				if DAG_DEBUG{
					fmt.Printf("DAG REdge addition to (%d,%d) of %s prevented due to existing REdge\n",dest.pid,dest.pc,a.toString())
				}
				return func() {}
			}
			if n.next == nil{
				break
			}
			n = n.next
			n.LockNode(dest)
		}
	}
	//n.next = &dagrnode{head: tailnode, node: destnode}
	var b *dagprnode = nil
	if destnode.rd != nil{
		b = destnode.rd
		b.LockNode(dest)
		for b.next != nil {
			b = b.next
			b.LockNode(dest)
		}
		//b.next = &dagprnode{node: n.next}
	} else {
		//destnode.rd = &dagprnode{node: n.next}
	}
	return func() {
		if n == nil {
			tailnode.read = &dagrnode{head: tailnode, node: destnode}
			tailnode.read.m = make(chan struct{},1)
			n = tailnode.read
		} else {
			n.next = &dagrnode{head: tailnode, node: destnode}
			n.next.m = make(chan struct{},1)
			n = n.next
		}
		if b != nil {
			b.next = &dagprnode{node: n}
			b.next.m = make(chan struct{},1)
		} else {
			destnode.rd = &dagprnode{node: n}
			destnode.rd.m = make(chan struct{},1)
		}
		if DAG_DEBUG {
			if tailnode.node == nil{
				fmt.Printf("DAG REdge Added BOT -> (%d,%d) of addr %s\n",dest.pid,dest.pc,a.toString())
			} else {
				fmt.Printf("DAG REdge Added (%d,%d) -> (%d,%d) of addr %s\n",tailnode.node.proc.pid,tailnode.node.pc,dest.pid,dest.pc,a.toString())
			}
		}
	}
}

func (p *pc) AssertRunnable(bwd bool){
	if bwd {
		p.isRunnableBwd()
	} else {
		p.isRunnableFwd()
	}
}

func (p *pc) isRunnableBwd() {
	n := p.pnode
	n.LockNode(p)
	if n.wt != nil{
		wn := n.wt
		wn.LockNode(p)
		if wn.next != nil {
			a := wn.next
			a.LockNode(p)
			wnn := a.node
			wnn.LockNode(p)
			if wnn.proc.pc > wnn.pc{
				panic(dagrunnableerror{p})
			}
		}
		if wn.read != nil {
			a := wn.read
			a.LockNode(p)
			for {
				rpn := a.node
				rpn.LockNode(p)
				if rpn.proc.pc > rpn.pc{
					panic(dagrunnableerror{p})
				}
				if a.next == nil{
					break
				}
				a = a.next
				a.LockNode(p)
			}
		}
	}
	if n.rd != nil{
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
			if a.next == nil{
				break
			}
			a = a.next
		}
	}
}

func (p *pc) isRunnableFwd() {
	pn := p.pnode
	pn.LockNode(p)
	if pn.wt != nil {
		wn := pn.wt
		wn.LockNode(p)
		if wn.prev != nil{
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
					if orn.proc.pc < orn.pc{
						panic(dagrunnableerror{p})
					}
					if a.next == nil{
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
				if ppn.proc.pc < ppn.pc{
					panic(dagrunnableerror{p})
				}
			}
			if a.next == nil{
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
	r.rootpc.Propagate(func(p *pc) {p.pnode.next = nil})
}


func (r *runtime) PrintDag(animate bool) {
	skeys := slices.SortedFunc(maps.Keys(r.dag.entries),func(a maddr, b maddr) int {
		if a.isGlobal && !b.isGlobal{
			return -1
		}
		if !a.isGlobal && b.isGlobal{
			return 1
		}
		return a.idx - b.idx
	})
	for _,a := range skeys{
		fmt.Printf("Address: %s[%d], ",func()string{if a.isGlobal {return "M"}; return "V"}(),a.idx)
		b := r.dag.entries[a].tail
		if b.node == nil {
			fmt.Printf("Tail at BOT\n")
		} else {
			fmt.Printf("Tail at (%d,%d)\n",b.node.proc.pid,b.node.pc)
		}
		for b.prev != nil {
			b = b.prev
		}
		for {
			if b.node == nil {
				fmt.Print("BOT |")
			} else {
				fmt.Printf("(%d,%d) |",b.node.proc.pid,b.node.pc)
			}
			if b.read != nil {
				a := b.read
				for {
					fmt.Printf(" (%d,%d)",a.node.proc.pid,a.node.pc)
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
// func (r *runtime) PrintDag(animate bool) {
// 	fmt.Print("Constructing graph...\n")
// 	files := make([]*image.Paletted, 0, len(r.dag.exechistory))
// 	//construct fill dag image first
// 	graph, _ := r.dag.gviz.Graph()
// 	if SEQ_DAG {
// 		graph.SetRankSeparator(0.02)
// 	}
// 	subgraphs := make([]*cgraph.Graph, 0)
// 	bot, _ := graph.CreateNodeByName("BOT")
// 	if SEQ_DAG {
// 		for pid := range r.pcs {
// 			sg,_ := graph.CreateSubGraphByName("P"+strconv.Itoa(pid))
// 			sg.SetBackgroundColor("gray")
// 			sg.SetStyle(cgraph.SolidGraphStyle)
// 			subgraphs = append(subgraphs, sg)
// 		}
// 	}
// 	for pid, v := range r.dag.incedges {
// 		for pc, edges := range v {
// 			p, ex := r.getPC(pid)
// 			c := 0
// 			if pid < len(r.dag.outedgectr) {
// 				if pc < len(r.dag.outedgectr[pid]) {
// 					c = r.dag.outedgectr[pid][pc]
// 				}
// 			}
// 			if len(edges) != 0 || c != 0 {
// 				var gnode *cgraph.Node
// 				if SEQ_DAG {
// 					gnode, _ = subgraphs[pid].NodeByName("(" + strconv.Itoa(pid) + "," + strconv.Itoa(pc) + ")")
// 				} else {
// 					gnode, _ = graph.NodeByName("(" + strconv.Itoa(pid) + "," + strconv.Itoa(pc) + ")")
// 				}
// 				if gnode == nil {
// 					if SEQ_DAG {
// 						gnode, _ = subgraphs[pid].CreateNodeByName("(" + strconv.Itoa(pid) + "," + strconv.Itoa(pc) + ")")
// 					} else {
// 						gnode, _ = graph.CreateNodeByName("(" + strconv.Itoa(pid) + "," + strconv.Itoa(pc) + ")")
// 					}
// 				}
// 				act := true
// 				if animate {
// 					gnode.SetStyle("invis")
// 				} else {
// 					if !ex {
// 						act = false
// 					} else if p.pc <= pc {
// 						act = false
// 					}
// 					if !act {
// 						gnode.SetColor("gray")
// 					}
// 				}
// 				//render invis edges that connect to previous pc of same proc
// 				if SEQ_DAG {
// 					var pnode *cgraph.Node
// 					pnode, _ = subgraphs[pid].NodeByName("(" + strconv.Itoa(pid) + "," + strconv.Itoa(pc-1) + ")")
// 					if pnode != nil {
// 						pedge, _ := graph.CreateEdgeByName("", pnode, gnode)
// 						pedge.SetWeight(100000)
// 						pedge.SetStyle("invis")
// 					}
// 				}
// 				//render all other incoming edges
// 			L1:
// 				for _, e := range edges {
// 					if !e.wt {
// 						for _, ae := range edges {
// 							if ae.pid == e.pid && ae.pc == e.pc && ae.varname == e.varname && ae.wt {
// 								continue L1
// 							}
// 						}
// 					}
// 					var tnode *cgraph.Node
// 					if e.pid == -1 && e.pc == -1 {
// 						tnode = bot
// 					} else {
// 						if SEQ_DAG {
// 							tnode, _ = subgraphs[e.pid].NodeByName("(" + strconv.Itoa(e.pid) + "," + strconv.Itoa(e.pc) + ")")
// 						} else {
// 							tnode, _ = graph.NodeByName("(" + strconv.Itoa(e.pid) + "," + strconv.Itoa(e.pc) + ")")
// 						}
// 						if tnode == nil {
// 							if SEQ_DAG {
// 								tnode, _ = subgraphs[e.pid].CreateNodeByName("(" + strconv.Itoa(e.pid) + "," + strconv.Itoa(e.pc) + ")")
// 							} else {
// 								tnode, _ = graph.CreateNodeByName("(" + strconv.Itoa(e.pid) + "," + strconv.Itoa(e.pc) + ")")
// 							}
// 							gnode.SetGroup(strconv.Itoa(e.pid))
// 						}
// 					}
// 					if animate {
// 						if e.pid != -1 || e.pc != -1 {
// 							tnode.SetStyle("invis")
// 						}
// 					} else {
// 						tp, ex := r.getPC(e.pid)
// 						act := true
// 						if !ex {
// 							act = false
// 						} else if tp.pc <= e.pc {
// 							act = false
// 						}
// 						if e.pid == -1 && e.pc == -1 {
// 							act = true
// 						}
// 						if !act {
// 							tnode.SetColor("gray")
// 						}
// 					}
// 					gedge, _ := graph.CreateEdgeByName("(" + strconv.Itoa(e.pid) + "," + strconv.Itoa(e.pc) + ")->(" + strconv.Itoa(pid) + "," + strconv.Itoa(pc) + ")", tnode, gnode)
// 					gedge.SetLabel(e.varname)
// 					if !e.wt {
// 						gedge.SetStyle("dashed")
// 					}
// 					if animate {
// 						gedge.SetColor("white")
// 						gedge.SetFontColor("white")
// 					} else {
// 						if !act {
// 							gedge.SetColor("gray")
// 						}
// 					}
// 				}
// 			}
// 		}
// 	}
// 	if !animate {
// 		fmt.Print("Rendering...\n")
// 		var buf bytes.Buffer
// 		r.dag.gviz.Render(*r.dag.gvcontext, graph, "dot", &buf)
// 		fmt.Println(buf.String())
// 		e := r.dag.gviz.RenderFilename(*r.dag.gvcontext,graph, graphviz.PNG, "dag.png")
// 		if e != nil{
// 			fmt.Print(e)
// 		}
		
// 		fmt.Printf("Done!\n")
// 		return
// 	}
// 	pcmax := make(map[int]int)
// 	pccur := make(map[int]int)
// 	instnode, _ := graph.CreateNodeByName("inst")
// 	instnode.SetShape(cgraph.BoxShape)
// 	for imindex, p := range r.dag.exechistory {
// 		fmt.Printf("Drawing DAG... (%d/%d)\n", imindex+1, len(r.dag.exechistory))
// 		instnode.SetLabel("(" + strconv.Itoa(p.pid) + "," + strconv.Itoa(p.pc) + ")" + "\n" + p.exec)
// 		v, ex := pcmax[p.pid]
// 		if !ex {
// 			pcmax[p.pid] = p.pc
// 		} else if p.pc > v {
// 			pcmax[p.pid] = p.pc
// 		}
// 		pccur[p.pid] = p.pc
// 		for npid, mpc := range pcmax {
// 			for npc := range mpc + 1 {
// 				var gnode *cgraph.Node
// 				if SEQ_DAG {
// 					gnode, _ = subgraphs[npid].NodeByName("(" + strconv.Itoa(npid) + "," + strconv.Itoa(npc) + ")")
// 				} else {
// 					gnode, _ = graph.NodeByName("(" + strconv.Itoa(npid) + "," + strconv.Itoa(npc) + ")")
// 				}
// 				if gnode != nil {
// 					c, ex := pccur[npid]
// 					if !ex {
// 						c = 0
// 					}
// 					if (npc > c && !p.rev) || (npc >= c && p.rev) {
// 						gnode.SetStyle("")
// 						gnode.SetColor("gray")
// 						gnode.SetFontColor("gray")
// 					} else {
// 						gnode.SetStyle("")
// 						gnode.SetColor("")
// 						gnode.SetFontColor("")
// 					}
// 					e,_ := graph.FirstIn(gnode)
// 					for e != nil {
// 						if (npc > c && !p.rev) || (npc >= c && p.rev) {
// 							e.SetColor("gray")
// 							e.SetFontColor("gray")
// 						} else {
// 							e.SetColor("")
// 							e.SetFontColor("")
// 						}
// 						e,_ = graph.NextIn(e)
// 					}

// 				}
// 			}
// 		}
// 		im, _ := r.dag.gviz.RenderImage(*r.dag.gvcontext,graph)
// 		bounds := im.Bounds()
// 		pl := image.NewPaletted(bounds, palette.WebSafe)
// 		draw.Draw(pl, bounds, im, bounds.Min, draw.Src)
// 		files = append(files, pl)
// 	}
// 	delays := make([]int, 0, len(r.dag.exechistory))
// 	for _ = range len(r.dag.exechistory) {
// 		delays = append(delays, 50)
// 	}
// 	f, _ := os.Create("dag.gif")
// 	defer f.Close()
// 	gif.EncodeAll(f, &gif.GIF{
// 		Image: files,
// 		Delay: delays,
// 	})
// 	fmt.Print("Done!\n")
// }
