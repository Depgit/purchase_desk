package documents

import "testing"

func TestDerived(t *testing.T) {
	for _, c := range []struct{ number, prefix, want string }{
		{"PR_HZ_0001", "PO", "PO_HZ_0001"},
		{"PR_HZ_0001_02", "PO", "PO_HZ_0001_02"},
		{"PR_HZ_0001", "GRN", "GRN_HZ_0001"},
		{"PR_HZ_0001_B1", "WT", "WT_HZ_0001_B1"},
		{"PR_CP_12345", "PO", "PO_CP_12345"},
	} {
		if got, ok := Derived(c.number, c.prefix); !ok || got != c.want {
			t.Errorf("Derived(%q, %q) = %q, %v; want %q", c.number, c.prefix, got, ok, c.want)
		}
	}
	for _, old := range []string{"PR-1001", "CR-1001", "PR-1001-B1"} {
		if _, ok := Derived(old, "PO"); ok {
			t.Errorf("Derived(%q) should leave an older number to NextNumber", old)
		}
	}
}

func TestRequestBase(t *testing.T) {
	for _, c := range []struct {
		number, base string
		suffix       int
	}{
		{"PR_HZ_0001", "PR_HZ_0001", 0},
		{"PR_HZ_0001_01", "PR_HZ_0001", 1},
		{"PR_HZ_0001_12", "PR_HZ_0001", 12},
	} {
		if b, s := RequestBase(c.number); b != c.base || s != c.suffix {
			t.Errorf("RequestBase(%q) = %q, %d; want %q, %d", c.number, b, s, c.base, c.suffix)
		}
	}
}

func TestIsRequestNumber(t *testing.T) {
	for n, want := range map[string]bool{"PR_HZ_0001": true, "PR_H1_0001_02": true, "PR_HZ_10000": true,
		"PR-1001": false, "PR_HZ_0001_B1": false, "PR_HZX_0001": false, "PR_hz_0001": false} {
		if IsRequestNumber(n) != want {
			t.Errorf("IsRequestNumber(%q) = %v, want %v", n, !want, want)
		}
	}
}

func TestAdvanceNumber(t *testing.T) {
	for _, c := range []struct {
		po   string
		n    int
		want string
	}{
		{"PO_HZ_0001", 1, "ADV_HZ_0001_01"},
		{"PO_HZ_0001", 2, "ADV_HZ_0001_02"},
		{"PO_HZ_0001_01", 1, "ADV_HZ_0001_01_01"},
		{"PO-2001", 1, "ADV-2001_01"},
	} {
		if got := AdvanceNumber(c.po, c.n); got != c.want {
			t.Errorf("AdvanceNumber(%q, %d) = %q, want %q", c.po, c.n, got, c.want)
		}
	}
}
