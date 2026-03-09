package main

import "strconv"

// labels manages the mapping between label names and line numbers
// for goto, come-from, begin, and end statements.
type labels struct {
	gotomap  map[string]int
	comemap  map[string]int
	beginmap map[string]int
	endmap   map[string]int
	tmplabel int
}

func newLabels() *labels {
	return &labels{
		gotomap:  make(map[string]int),
		comemap:  make(map[string]int),
		beginmap: make(map[string]int),
		endmap:   make(map[string]int),
	}
}

func (l *labels) GetGoto(labelname string) int {
	if val, ex := l.gotomap[labelname]; ex {
		return val
	}
	panic("goto label not found: " + labelname)
}

func (l *labels) GetComeFrom(labelname string) int {
	if val, ex := l.comemap[labelname]; ex {
		return val
	}
	panic("come-from label not found: " + labelname)
}

func (l *labels) GetBegin(labelname string) int {
	if val, ex := l.beginmap[labelname]; ex {
		return val
	}
	panic("begin label not found: " + labelname)
}

func (l *labels) GetEnd(labelname string) int {
	if val, ex := l.endmap[labelname]; ex {
		return val
	}
	panic("end label not found: " + labelname)
}

func (l *labels) AddGoto(labelname string, lineno int)     { l.gotomap[labelname] = lineno }
func (l *labels) AddComeFrom(labelname string, lineno int) { l.comemap[labelname] = lineno }
func (l *labels) AddBegin(labelname string, lineno int)    { l.beginmap[labelname] = lineno }
func (l *labels) AddEnd(labelname string, lineno int)      { l.endmap[labelname] = lineno }

// RegisterNewLabel creates a temporary label pair for implicit blocks.
func (l *labels) RegisterNewLabel(lineno int) string {
	s := "_" + strconv.Itoa(l.tmplabel)
	l.AddGoto(s, lineno)
	l.AddComeFrom(s, lineno+1)
	l.tmplabel++
	return s
}
