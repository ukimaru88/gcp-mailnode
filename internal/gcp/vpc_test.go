package gcp

import (
	"testing"
)

func TestExtraVPCNames(t *testing.T) {
	cases := []struct {
		nicCount int
		want     []string
	}{
		{0, nil},
		{1, nil},
		{2, []string{"mail-vpc-1"}},
		{4, []string{"mail-vpc-1", "mail-vpc-2", "mail-vpc-3"}},
		{8, []string{"mail-vpc-1", "mail-vpc-2", "mail-vpc-3", "mail-vpc-4", "mail-vpc-5", "mail-vpc-6", "mail-vpc-7"}},
	}
	for _, c := range cases {
		got := ExtraVPCNames(c.nicCount)
		if len(got) != len(c.want) {
			t.Errorf("ExtraVPCNames(%d) len=%d want %d", c.nicCount, len(got), len(c.want))
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("ExtraVPCNames(%d)[%d]=%q want %q", c.nicCount, i, got[i], c.want[i])
			}
		}
	}
}

func TestSubnetCIDRForVPC(t *testing.T) {
	cases := []struct {
		vpc  string
		want string
	}{
		{"mail-vpc-1", "10.201.0.0/24"},
		{"mail-vpc-2", "10.202.0.0/24"},
		{"mail-vpc-7", "10.207.0.0/24"},
	}
	for _, c := range cases {
		if got := SubnetCIDRForVPC(c.vpc); got != c.want {
			t.Errorf("SubnetCIDRForVPC(%q)=%q want %q", c.vpc, got, c.want)
		}
	}
}

func TestMailNodeFirewallNames(t *testing.T) {
	if got := mailNodeInboundNameFor("default"); got != "mailnode-mail-ports-v2" {
		t.Errorf("default inbound name=%q want mailnode-mail-ports-v2", got)
	}
	if got := mailNodeOutboundNameFor("default"); got != "mailnode-smtp-out" {
		t.Errorf("default outbound name=%q want mailnode-smtp-out", got)
	}
	if got := mailNodeInboundNameFor("mail-vpc-1"); got != "mailnode-mail-ports-mail-vpc-1" {
		t.Errorf("mail-vpc-1 inbound name=%q want mailnode-mail-ports-mail-vpc-1", got)
	}
	if got := mailNodeOutboundNameFor("mail-vpc-7"); got != "mailnode-smtp-out-mail-vpc-7" {
		t.Errorf("mail-vpc-7 outbound name=%q want mailnode-smtp-out-mail-vpc-7", got)
	}
}
