package integrations

import (
	"context"
	"net/netip"
	"testing"
)

func TestAddressPolicy(t *testing.T) {
	for _, ip := range []string{
		"0.0.0.0", "10.1.2.3", "100.64.0.1", "127.0.0.1", "168.63.129.16", "169.254.169.254", "172.16.0.0", "172.31.255.255",
		"192.0.0.8", "192.0.2.1", "192.88.99.1", "192.168.1.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1",
		"255.255.255.255", "::", "::1", "::ffff:192.168.1.1", "::ffff:8.8.8.8", "::8.8.8.8", "64:ff9b::a00:1", "64:ff9b:1::1",
		"::ffff:0:a00:1", "fc00::1", "fd00::1", "fe80::1", "fec0::1", "ff02::1", "2001::1", "2001:db8::1", "2002::1", "3fff::1",
		"5f00::1", "100::1", "2001:2::1", "4000::1", "2001:10::1",
	} {
		if Blocked(netip.MustParseAddr(ip)) != true {
			t.Error(ip)
		}
	}
	for _, ip := range []string{
		"8.8.8.8",
		"1.1.1.1",
		"93.184.216.34",
		"142.250.185.206",
		"172.32.0.1",
		"100.128.0.1",
		"192.0.1.1",
		"2606:2800:220:1:248:1893:25c8:1946",
		"2a00:1450:4001:82a::200e",
		"2001:3::1",
		"2001:4:112::1",
		"64:ff9b::808:808",
		"::ffff:0:808:808",
		"2c0f:ffff::1",
	} {
		if Blocked(netip.MustParseAddr(ip)) != false {
			t.Error(ip)
		}
	}
}

type fixedResolver struct{ ips []netip.Addr }

func (f fixedResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return f.ips, nil
}
func TestGuardRejectsNumericAndMixedAnswers(t *testing.T) {
	r := fixedResolver{[]netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("93.184.216.34")}}
	for _, host := range []string{"127.1", "0x7f.1", "2130706433", "0177.0.0.01", "[::1]", "under_score.example", "a..b", "1.2.3.4."} {
		if _, err := ResolvePublic(context.Background(), r, host); err == nil {
			t.Error(host)
		}
	}
	ip, err := ResolvePublic(context.Background(), r, "mixed.example")
	if err != nil || ip.String() != "93.184.216.34" {
		t.Fatal(ip, err)
	}
}
