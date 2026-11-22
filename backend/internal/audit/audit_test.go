package audit

import (
	"fmt"
	"net/netip"
	"testing"
	"time"

	"go4.org/netipx"

	"mrsmith/internal/rules"
)

func provider(name string, domains []string, cidrs []netip.Prefix) Provider {
	set := rules.NewSet()
	for _, d := range domains {
		set.Domains[d] = struct{}{}
	}
	var b netipx.IPSetBuilder
	for _, c := range cidrs {
		set.CIDRs[c] = struct{}{}
		b.AddPrefix(c)
	}
	p := Provider{Candidate: Candidate{Name: name, Rules: set.Len()}, set: set}
	if len(cidrs) > 0 {
		p.ips, _ = b.IPSet()
	}
	return p
}

func TestLargeAuditIsFast(t *testing.T) {
	var a1, a2 []string
	var c1, c2 []netip.Prefix
	for i := 0; i < 100_000; i++ {
		a1 = append(a1, fmt.Sprintf("+.site%d.example%d.com", i, i%50))
		if i%2 == 0 {
			a2 = append(a2, fmt.Sprintf("www.site%d.example%d.com", i, i%50)) // covered by a1
		} else {
			a2 = append(a2, fmt.Sprintf("+.other%d.net", i))
		}
	}
	for i := 0; i < 20_000; i++ {
		c1 = append(c1, netip.PrefixFrom(netip.AddrFrom4([4]byte{10, byte(i >> 8), byte(i), 0}), 24))
		c2 = append(c2, netip.PrefixFrom(netip.AddrFrom4([4]byte{10, byte(i >> 8), byte(i), 7}), 32))
	}
	rep := &Report{Providers: []Provider{provider("big", a1, c1), provider("other", a2, c2)}}
	an := &analysis{provs: rep.Providers, report: rep, pairs: map[[2]int]*Pair{}, wide: map[[2]int]map[string]bool{}, removable: map[string]bool{}}
	start := time.Now()
	an.run()
	took := time.Since(start)
	t.Logf("240k rules audited in %s: %+v", took, rep.Totals)
	if rep.Totals.Covered != 50_000+20_000 {
		t.Fatalf("covered = %d", rep.Totals.Covered)
	}
	if took > 10*time.Second {
		t.Fatalf("too slow: %s", took)
	}
}
