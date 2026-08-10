package network

import "testing"

func TestSeqDedupRejectsOldReplayAfterEviction(t *testing.T) {
	d := newSeqDedup()
	if d.isDuplicate(1) {
		t.Fatal("first sequence was rejected")
	}
	for seq := uint32(2); seq <= 513; seq++ {
		if d.isDuplicate(seq) {
			t.Fatalf("sequence %d was rejected", seq)
		}
	}
	if !d.isDuplicate(1) {
		t.Fatal("old replay was accepted after leaving the dedup window")
	}
}
