package util

import (
	"fmt"
	"runtime"
	"sync"
)

func Unclog[T any](c chan T) {
	//non-blockingly receives msgs from channel until none left
	for {
		select {
		case <-c:
			continue
		default:
			return
		}
	}
}

type PriorityLock struct{
	prio int //priority, zero or above
	mut *sync.Mutex //shared mutex
	cmut sync.Mutex //mutex for current one (deciding which one of the same priority comes first)
	wait sync.WaitGroup //counter for locking process higher than current
	next *PriorityLock //maps to higher priority lock
}

func NewPriorityLock() *PriorityLock{
	return &PriorityLock{0,&sync.Mutex{},sync.Mutex{},sync.WaitGroup{},nil}
}

func (l *PriorityLock) Lock(prio int){
	//if lock's priority is lower than current, add 1 to Waitgroup and proceed to next lock
	// (creating one if needed, if next lock's priority is higher than current, insert one that matches current)
	//if lock's priority is the same, lock current mutex, wait for higher group, lock shared mutex, unlock current mutex (allowing next one with same priority to hold lock)
	switch {
	case prio > l.prio:
		l.wait.Add(1)
		if l.next == nil{
			l.next = &PriorityLock{l.prio+1,l.mut,sync.Mutex{},sync.WaitGroup{},nil}
		}
		l.next.Lock(prio)
	case prio == l.prio:
		l.cmut.Lock()
		l.wait.Wait()
		l.mut.Lock()
		l.cmut.Unlock()
	default:
		panic(fmt.Sprintf("Function Lock() with priority %d executed on PriorityLock with priority %d",prio,l.prio))
	}
}

func (l *PriorityLock) Unlock(prio int){
	//if lock's priority is lower than current, remove 1 from Waitgroup and proceed to next lock
	//if lock's priority is the same, unlock shared mutex
	switch {
	case prio > l.prio:
		l.wait.Done()
		l.next.Unlock(prio)
	case prio == l.prio:
		l.mut.Unlock()
	default:
		panic(fmt.Sprintf("Function Unlock() with priority %d executed on PriorityLock with priority %d",prio,l.prio))
	}
}


func SetRuntimeBlockProfileRate(rate int){
	runtime.SetBlockProfileRate(rate)
}

func SetRuntimeMutexProfileRate(rate int){
	runtime.SetMutexProfileFraction(rate)
}

func SetRuntimeCPU(num int){
	runtime.GOMAXPROCS(num)
	fmt.Printf("Debug >> Set GOMAXPROCS=%d Success\n",num)
}