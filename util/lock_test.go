package util

import (
	"math/rand/v2"
	"testing"
	"time"
)

func TestPriorityLock(t *testing.T){
	l := NewPriorityLock()
	k := make([]int,0)
	l.Lock(0)
	go func ()  {
		defer l.Unlock(0)
		<- time.After(1*time.Millisecond)
	}()
	for range 1000{
		n := rand.IntN(500)
		go func (n int)  {
			<- time.After(1*time.Millisecond)
			l.Lock(n)
			defer l.Unlock(n)
			t.Logf("Locked with priority %d",n)
			<- time.After(1*time.Millisecond)
			k = append(k, n)
			t.Logf("Unlocking with priority %d",n)
		}(n)
	}
	<- time.After(3*time.Second)
	for i := range 999{
		if k[i] < k[i+1]{
			t.Errorf("Conflict between index %d and %d, with priorities %d and %d",i,i+1,k[i],k[i+1])
		}
	}
}
