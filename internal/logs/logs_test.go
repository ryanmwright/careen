package logs

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/ryanmwright/careen/internal/sshx"
)

func TestParseLine(t *testing.T) {
	b, err := os.ReadFile("testdata/journal.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var got []Entry
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		e, err := ParseLine(sc.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, e)
	}
	want0 := Entry{Time: time.UnixMicro(1791500000123456).UTC(), Unit: "nginx.service", Priority: 3, Message: "bind() to 0.0.0.0:80 failed"}
	if got[0] != want0 {
		t.Errorf("entry 0 = %+v", got[0])
	}
	if got[1].Unit != "kernel" || got[1].Message != "hi\xff" || got[1].Priority != 6 {
		t.Errorf("entry 1 = %+v", got[1])
	}
}

// TestReadLocal reads this machine's journal, where there is one.
func TestReadLocal(t *testing.T) {
	if _, err := os.Stat("/run/systemd/journal"); err != nil {
		t.Skip("no systemd journal")
	}
	entries, err := Read(context.Background(), sshx.LocalHost{}, "", 5)
	if err != nil {
		t.Skip("can't read the journal here: ", err)
	}
	if len(entries) == 0 || entries[0].Time.IsZero() {
		t.Errorf("entries = %+v", entries)
	}
}
