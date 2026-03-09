package main

import (
	"fmt"
	"strings"
	"sync"
)

func (r *runtime) initInstset() {
	// x += a  /  x -= a  /  x ^= a
	r.addrun(`^([\w\d$#.:\[\]]+)\s*([+\-^])=\s*([\w\d$#.:\[\]+\-*/!<=>&|%\(\) ]+)\s*$`,
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			var eval func() int
			var dest addr
			var destFn func()
			var execFn func() int
			expr := arg[3]
			destName := arg[1]
			switch arg[2] {
			case "+":
				execFn = func() int {
					v := r.ReadAddr(dest, p)
					r.WriteAddr(dest, v+eval(), p)
					destFn()
					return retNormal
				}
			case "-":
				execFn = func() int {
					v := r.ReadAddr(dest, p)
					r.WriteAddr(dest, v-eval(), p)
					destFn()
					return retNormal
				}
			case "^":
				execFn = func() int {
					v := r.ReadAddr(dest, p)
					r.WriteAddr(dest, v^eval(), p)
					destFn()
					return retNormal
				}
			}
			return func() *rdwtinfo {
				var rd []addr
				eval, rd = EvalExpr(expr, r, p)
				dest, destFn = r.GetAddrOfAllocable(destName, p)
				info := NewRdWtInfo()
				info.rd = append(info.rd, rd...)
				info.wt = append(info.wt, dest)
				return info
			}, execFn
		},
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			var eval func() int
			var dest addr
			var destFn func()
			var execFn func() int
			expr := arg[3]
			destName := arg[1]
			// Backward: reverse the operation
			switch arg[2] {
			case "+":
				execFn = func() int {
					v := r.ReadAddr(dest, p)
					r.WriteAddr(dest, v-eval(), p)
					destFn()
					return retNormal
				}
			case "-":
				execFn = func() int {
					v := r.ReadAddr(dest, p)
					r.WriteAddr(dest, v+eval(), p)
					destFn()
					return retNormal
				}
			case "^":
				execFn = func() int {
					v := r.ReadAddr(dest, p)
					r.WriteAddr(dest, v^eval(), p)
					destFn()
					return retNormal
				}
			}
			return func() *rdwtinfo {
				var rd []addr
				eval, rd = EvalExpr(expr, r, p)
				dest, destFn = r.GetAddrOfAllocable(destName, p)
				info := NewRdWtInfo()
				info.rd = append(info.rd, rd...)
				info.wt = append(info.wt, dest)
				return info
			}, execFn
		})

	// print x
	r.addrun(`^print\s+([\w\d$#.:\[\]]+)$`,
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int {
					v := r.ReadSym(arg[1], p)
					fmt.Printf(">>out: %s %d\n", arg[1], v)
					return retNormal
				}
		},
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int {
					v := r.ReadSym(arg[1], p)
					fmt.Printf("<<out: %s %d\n", arg[1], v)
					return retNormal
				}
		})

	// skip
	r.addrun(`^skip`,
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int { return retNormal }
		},
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int { return retNormal }
		})

	// -> L  (unconditional goto)
	r.addrun(`^->\s*([\w\d$#.:]+)$`,
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int {
					p.lastlabel = arg[1]
					p.head = r.labels.GetComeFrom(arg[1])
					return retBranch
				}
		},
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int {
					if p.lastlabel != arg[1] {
						panic("label mismatch after branch: expected " + arg[1] + ", got " + p.lastlabel)
					}
					p.lastlabel = ""
					return retNormal
				}
		})

	// L <-  (come-from)
	r.addrun(`^([\w\d$#.:]+)\s*<-$`,
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int {
					if p.lastlabel != arg[1] {
						panic("label mismatch after branch: expected " + arg[1] + ", got " + p.lastlabel)
					}
					p.lastlabel = ""
					return retNormal
				}
		},
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int {
					p.lastlabel = arg[1]
					p.head = r.labels.GetGoto(arg[1]) + 1
					return retBranch
				}
		})

	// a <=> b  (swap)
	r.addrun(`^([\w\d$#.:\[\]]+)\s*<=>\s*([\w\d$#.:\[\]]+)$`,
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int {
					a := r.ReadSym(arg[1], p)
					b := r.ReadSym(arg[2], p)
					r.WriteSym(arg[1], b, p)
					r.WriteSym(arg[2], a, p)
					return retNormal
				}
		},
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			// Swap is self-inverse
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int {
					a := r.ReadSym(arg[1], p)
					b := r.ReadSym(arg[2], p)
					r.WriteSym(arg[1], b, p)
					r.WriteSym(arg[2], a, p)
					return retNormal
				}
		})

	// expr -> L1;L2  (conditional branch)
	r.addrun(`^([\w\d$#.:\[\]+\-*/!<=>&|%\(\) ]+)\s*->\s*([\w\d$#.:]+)\s*;\s*([\w\d$#.:]+)\s*$`,
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			var eval func() int
			return func() *rdwtinfo {
				var rd []addr
				info := NewRdWtInfo()
				eval, rd = EvalExpr(arg[1], r, p)
				info.rd = append(info.rd, rd...)
				return info
			}, func() int {
				if eval() != 0 {
					p.lastlabel = arg[2]
					p.head = r.labels.GetComeFrom(arg[2])
				} else {
					p.lastlabel = arg[3]
					p.head = r.labels.GetComeFrom(arg[3])
				}
				return retBranch
			}
		},
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			var eval func() int
			return func() *rdwtinfo {
				var rd []addr
				info := NewRdWtInfo()
				eval, rd = EvalExpr(arg[1], r, p)
				info.rd = append(info.rd, rd...)
				return info
			}, func() int {
				if eval() != 0 {
					if p.lastlabel != arg[2] {
						panic("label mismatch after branch")
					}
				} else {
					if p.lastlabel != arg[3] {
						panic("label mismatch after branch")
					}
				}
				p.lastlabel = ""
				return retNormal
			}
		})

	// L1;L2 <- expr  (conditional come-from)
	r.addrun(`^([\w\d$#.:]+)\s*;\s*([\w\d$#.:]+)\s*<-\s*([\w\d$#.:\[\]+\-*/!<=>&|%\(\) ]+)$`,
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			var eval func() int
			return func() *rdwtinfo {
				var rd []addr
				info := NewRdWtInfo()
				eval, rd = EvalExpr(arg[3], r, p)
				info.rd = append(info.rd, rd...)
				return info
			}, func() int {
				if eval() != 0 {
					if p.lastlabel != arg[1] {
						panic("label mismatch after branch")
					}
				} else {
					if p.lastlabel != arg[2] {
						panic("label mismatch after branch")
					}
				}
				p.lastlabel = ""
				return retNormal
			}
		},
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			var eval func() int
			return func() *rdwtinfo {
				var rd []addr
				info := NewRdWtInfo()
				eval, rd = EvalExpr(arg[3], r, p)
				info.rd = append(info.rd, rd...)
				return info
			}, func() int {
				if eval() != 0 {
					p.lastlabel = arg[1]
					p.head = r.labels.GetGoto(arg[1]) + 1
				} else {
					p.lastlabel = arg[2]
					p.head = r.labels.GetGoto(arg[2]) + 1
				}
				return retBranch
			}
		})

	// begin <name>
	r.addrun(`^begin\s+([\w\d]+)`,
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int {
					if !cfg.Sequential && p.nest != 0 {
						panic("process started at non-zero nest depth")
					}
					return retNormal
				}
		},
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int {
					if !cfg.Sequential && p.nest != 0 {
						panic("process ended at non-zero nest depth")
					}
					if arg[1] == "main" {
						return retEOF
					}
					return retProcEnd
				}
		})

	// end <name>
	r.addrun(`^end\s+([\w\d$#.:]+)`,
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int {
					if !cfg.Sequential && p.nest != 0 {
						panic("process ended at non-zero nest depth")
					}
					if arg[1] == "main" {
						return retEOF
					}
					return retProcEnd
				}
		},
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int {
					if !cfg.Sequential && p.nest != 0 {
						panic("process started at non-zero nest depth")
					}
					return retNormal
				}
		})

	// call proc1, proc2, ...
	r.addrun(`^call\s+(.*)`,
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int {
					return r.executeCall(p, arg[1], false)
				}
		},
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int {
					return r.executeCall(p, arg[1], true)
				}
		})

	// push (indent)
	r.addrun(`^push`,
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int {
					if cfg.BlockDebug {
						fmt.Print("bin" + p.getBlock() + "f\n")
					}
					p.indent()
					if cfg.BlockDebug {
						fmt.Print("bou" + p.getBlock() + "f\n")
					}
					return retNormal
				}
		},
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int {
					if cfg.BlockDebug {
						fmt.Print("bin" + p.getBlock() + "f\n")
					}
					p.unindent()
					if cfg.BlockDebug {
						fmt.Print("bou" + p.getBlock() + "f\n")
					}
					return retNormal
				}
		})

	// pop (unindent)
	r.addrun(`^pop`,
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int {
					if cfg.BlockDebug {
						fmt.Print("bin" + p.getBlock() + "f\n")
					}
					p.unindent()
					if cfg.BlockDebug {
						fmt.Print("bou" + p.getBlock() + "f\n")
					}
					return retNormal
				}
		},
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int {
					if cfg.BlockDebug {
						fmt.Print("bin" + p.getBlock() + "f\n")
					}
					p.indent()
					if cfg.BlockDebug {
						fmt.Print("bou" + p.getBlock() + "f\n")
					}
					return retNormal
				}
		})

	// set dst src
	r.addrun(`^set\s+([\w\d$#.:\[\]\@]+)\s+([\w\d$#.:\[\]]+)`,
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int {
					r.SetSym(arg[1], arg[2], p)
					return retNormal
				}
		},
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int {
					r.UnsetSym(arg[1], arg[2], p)
					return retNormal
				}
		})

	// unset dst src
	r.addrun(`^unset\s+([\w\d$#.:\[\]\@]+)\s+([\w\d$#.:\[\]]+)`,
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int {
					r.UnsetSym(arg[1], arg[2], p)
					return retNormal
				}
		},
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo { return NewRdWtInfo() },
				func() int {
					r.SetSym(arg[1], arg[2], p)
					return retNormal
				}
		})

	// V (semaphore signal)
	r.addrun(`^V\s+([\w\d$#.:\[\]]+)`,
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			var a addr
			return func() *rdwtinfo {
				a = r.GetAddr(arg[1], p)
				info := NewRdWtInfo()
				info.wt = append(info.wt, a)
				info.v = append(info.v, a)
				return info
			}, func() int {
				r.WriteAddr(a, 1, p)
				return retNormal
			}
		},
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			var a addr
			return func() *rdwtinfo {
				a = r.GetAddr(arg[1], p)
				info := NewRdWtInfo()
				info.wt = append(info.wt, a)
				info.p = append(info.p, a)
				return info
			}, func() int {
				r.WriteAddr(a, 0, p)
				return retNormal
			}
		})

	// P (semaphore wait)
	r.addrun(`^P\s+([\w\d$#.:\[\]]+)`,
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			var a addr
			return func() *rdwtinfo {
				a = r.GetAddr(arg[1], p)
				info := NewRdWtInfo()
				info.wt = append(info.wt, a)
				info.p = append(info.p, a)
				return info
			}, func() int {
				r.WriteAddr(a, 0, p)
				return retNormal
			}
		},
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			var a addr
			return func() *rdwtinfo {
				a = r.GetAddr(arg[1], p)
				info := NewRdWtInfo()
				info.wt = append(info.wt, a)
				info.v = append(info.v, a)
				return info
			}, func() int {
				r.WriteAddr(a, 1, p)
				return retNormal
			}
		})

	// lock var1, var2, ...
	r.addrun(`^lock\s+([\w\d\s.:\[\],]+)`,
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo {
				info := NewRdWtInfo()
				for _, v := range strings.Split(arg[1], ",") {
					v = strings.TrimSpace(v)
					if v != "" {
						a := r.GetAddr(v, p)
						info.rd = append(info.rd, a)
						info.lock = append(info.lock, a)
					}
				}
				return info
			}, func() int { return retNormal }
		},
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo {
				info := NewRdWtInfo()
				for _, v := range strings.Split(arg[1], ",") {
					v = strings.TrimSpace(v)
					if v != "" {
						a := r.GetAddr(v, p)
						info.rd = append(info.rd, a)
						info.unlock = append(info.unlock, a)
					}
				}
				return info
			}, func() int { return retNormal }
		})

	// unlock var1, var2, ...
	r.addrun(`^unlock\s+([\w\d\s.:\[\],]+)`,
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo {
				info := NewRdWtInfo()
				for _, v := range strings.Split(arg[1], ",") {
					v = strings.TrimSpace(v)
					if v != "" {
						a := r.GetAddr(v, p)
						info.rd = append(info.rd, a)
						info.unlock = append(info.unlock, a)
					}
				}
				return info
			}, func() int { return retNormal }
		},
		func(r *runtime, p *pc, arg []string) (func() *rdwtinfo, func() int) {
			return func() *rdwtinfo {
				info := NewRdWtInfo()
				for _, v := range strings.Split(arg[1], ",") {
					v = strings.TrimSpace(v)
					if v != "" {
						a := r.GetAddr(v, p)
						info.rd = append(info.rd, a)
						info.lock = append(info.lock, a)
					}
				}
				return info
			}, func() int { return retNormal }
		})
}

// executeCall handles the "call" instruction for both forward and backward execution.
func (r *runtime) executeCall(p *pc, arglist string, backward bool) int {
	procnames := strings.Split(arglist, ",")

	if cfg.Sequential {
		if len(procnames) >= 2 {
			panic("sequential mode only allows one call")
		}
		name := strings.TrimSpace(procnames[0])
		var head int
		if backward {
			head = r.GetEnd(name) + 1
		} else {
			head = r.GetBegin(name)
		}
		lasthead := p.head
		p.head = head
		rt := p.execute(r)
		p.head = lasthead
		if rt == 0 {
			return retRespawn
		}
		return retNormal
	}

	var wg sync.WaitGroup
	suspendproc := make(chan bool, len(procnames))
	for i, v := range procnames {
		wg.Add(1)
		v = strings.TrimSpace(v)
		var head int
		if backward {
			head = r.GetEnd(v) + 1
		} else {
			head = r.GetBegin(v)
		}
		r.runPCCall(p, i, head, &wg, suspendproc)
	}
	// Unlock all nodes held by parent
	for _, f := range p.dfunc {
		f()
	}
	p.dfunc = make([]func(), 0)
	wg.Wait()

	select {
	case <-suspendproc:
		return retRespawn
	default:
		return retNormal
	}
}
