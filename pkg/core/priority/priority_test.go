package priority

import "testing"

func TestAdd(t *testing.T) {
	m := make(Map)
	m.Add("apple", "orange")
	m.Add("apple", "banana")
	m.Add("apple", "orange")
	l := m["apple"]
	if len(l) != 2 {
		t.Errorf("Priority: expecting two superiors, got %d", len(l))
	}
}

func TestComplete(t *testing.T) {
	m := make(Map)
	m.Add("apple", "orange")
	m.Add("orange", "banana")
	m.Add("orange", "grapes")
	m.Add("banana", "grapes")
	m.Add("banana", "grapefruit")
	m.Complete()
	l := m["apple"]
	if len(l) != 4 {
		t.Errorf("Priority: expecting two superiors, got %d", len(l))
	}
	l = m["orange"]
	if len(l) != 3 {
		t.Errorf("Priority: expecting two superiors, got %d", len(l))
	}
}

func TestList(t *testing.T) {
	m := make(Map)
	m.Add("apple", "orange")
	m.Add("orange", "banana")
	m.Add("orange", "grapes")
	m.Add("banana", "grapes")
	m.Add("banana", "grapefruit")
	m.Complete()
	list := m.List([]string{"apple", "grapes", "grapes", "banana", "banana", "apple"})
	if len(list) != 6 {
		t.Errorf("Priority: expecting six sets of indexes, got %d", len(list))
	}
	if len(list[0]) != 4 {
		t.Errorf("Priority: expecting four indexes for apple, got %v", len(list[0]))
	}
	if len(list[5]) != 4 {
		t.Errorf("Priority: expecting four indexes for apple, got %v", len(list[5]))
	}
}

func TestSubset(t *testing.T) {
	m := make(Map)
	m.Add("apple", "orange")
	m.Add("orange", "banana")
	m.Add("orange", "grapes")
	m.Add("banana", "grapes")
	m.Add("banana", "grapefruit")
	m.Complete()
	list := m.List([]string{"apple", "grapes", "grapes", "banana", "banana", "apple"})
	sub := list.Subset([]int{0, 3, 5})
	if len(sub) != 3 {
		t.Errorf("Priority: expecting 3 in the subset list, got %d", len(sub))
	}
	if len(sub[0]) != 1 {
		t.Errorf("Priority: expecting one index for apple subset, got %v", len(sub[0]))
	}
	if len(sub[2]) != 1 {
		t.Errorf("Priority: expecting one index for apple subset, got %v", len(sub[2]))
	}
}

func TestSet(t *testing.T) {
	m := make(Map)
	m.Add("apple", "orange")
	m.Add("orange", "banana")
	m.Add("orange", "grapes")
	m.Add("banana", "grapes")
	m.Add("banana", "grapefruit")
	m.Complete()
	list := m.List([]string{"apple", "grapes", "grapes", "banana", "banana", "apple"})
	list2 := m.List([]string{"grapefruit", "banana", "grapes"})
	s := &Set{}
	s.Add(list, len(list))
	s.Add(list2, len(list2))
	w := s.WaitSet()
	if !w.Check(8) {
		t.Error("Priority: should get continue signal")
	}
	if w.Put(8) {
		t.Error("Priority: should not be satisfied")
	}
	if !w.Put(1) {
		t.Error("Priority: should be satisfied")
	}
	w.Put(7)
	if !w.Check(6) {
		t.Error("Priority: expecting to be waiting on grapefruits")
	}
	wo := w.WaitingOn()
	if len(wo) != 2 {
		t.Error("Priority: expecting to be waiting on two")
	}
	if wo[0] != 6 {
		t.Error("Priority: expecting to be waiting on grapefruits")
	}
}
