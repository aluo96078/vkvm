package network

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPacketTraceRecorderIsBoundedAndPrivate(t *testing.T) {
	recorder := NewPacketTraceRecorder(2)
	for seq := uint32(1); seq <= 3; seq++ {
		recorder.Record(PacketTraceEvent{At: time.Now().UTC(), Direction: "rx", Type: 1, Sequence: seq})
	}
	events := recorder.Snapshot()
	if len(events) != 2 || events[0].Sequence != 2 || events[1].Sequence != 3 {
		t.Fatalf("bounded trace=%+v", events)
	}
	path := filepath.Join(t.TempDir(), "packets.json")
	if err := recorder.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("trace file err=%v mode=%v", err, info.Mode())
	}
}
