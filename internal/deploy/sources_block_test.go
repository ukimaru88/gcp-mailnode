package deploy

import (
	"strings"
	"testing"
)

func TestBuildSourcesBlock_Empty(t *testing.T) {
	if got := BuildSourcesBlock(nil); got != "" {
		t.Errorf("empty want '', got %q", got)
	}
}

func TestBuildSourcesBlock_Single(t *testing.T) {
	got := BuildSourcesBlock([]SourceSpec{{Name: "primary", IP: "10.146.0.10", EHLO: "mail.example.jp"}})
	checks := []string{
		"get_egress_source",
		"get_egress_pool",
		"source_address = '10.146.0.10'",
		"ehlo_domain = 'mail.example.jp'",
		"name='primary'",
	}
	for _, c := range checks {
		if !strings.Contains(got, c) {
			t.Errorf("single source missing %q in:\n%s", c, got)
		}
	}
}

func TestBuildSourcesBlock_Multi(t *testing.T) {
	srcs := []SourceSpec{
		{Name: "ip0", IP: "10.146.0.10", EHLO: "mail1.example.jp"},
		{Name: "ip1", IP: "10.201.0.10", EHLO: "mail2.example.jp"},
		{Name: "ip2", IP: "10.202.0.10", EHLO: "mail3.example.jp"},
	}
	got := BuildSourcesBlock(srcs)
	for _, s := range srcs {
		if !strings.Contains(got, "name == '"+s.Name+"'") {
			t.Errorf("missing source dispatch for %s", s.Name)
		}
		if !strings.Contains(got, "source_address = '"+s.IP+"'") {
			t.Errorf("missing source_address for %s (%s)", s.Name, s.IP)
		}
		if !strings.Contains(got, "ehlo_domain = '"+s.EHLO+"'") {
			t.Errorf("missing ehlo_domain for %s (%s)", s.Name, s.EHLO)
		}
		if !strings.Contains(got, "{ name='"+s.Name+"' }") {
			t.Errorf("missing pool entry for %s", s.Name)
		}
	}
	if !strings.Contains(got, "error('unknown egress source") {
		t.Errorf("multi source should have error fallback")
	}
}
