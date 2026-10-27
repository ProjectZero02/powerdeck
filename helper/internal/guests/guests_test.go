package guests

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestCached(t *testing.T) {
	calls := 0
	f := Cached(time.Hour, func(context.Context) *Info { calls++; return &Info{Running: calls} })
	if f(context.Background()).Running != 1 || f(context.Background()).Running != 1 || calls != 1 {
		t.Errorf("not cached: %d calls", calls)
	}
}

func TestParse(t *testing.T) {
	qm := `      VMID NAME                 STATUS     MEM(MB)    BOOTDISK(GB) PID
       100 ops                  running    2048              32.00 1234
       200 public               stopped    4096             150.00 0
      9000 template             stopped    2048              16.00 0
`
	if got := ParseQM(qm); !slices.Equal(got, []string{"ops"}) {
		t.Errorf("qm: %v", got)
	}
	pct := `VMID       Status     Lock         Name
101        running                 dns
102        stopped    backup       old
`
	if got := ParsePCT(pct); !slices.Equal(got, []string{"dns"}) {
		t.Errorf("pct: %v", got)
	}
}
