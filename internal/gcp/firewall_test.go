package gcp

import (
	"testing"

	"cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/protobuf/proto"
)

func TestFirewallNeedsPatchDetectsRestrictedSource(t *testing.T) {
	desired := &computepb.Firewall{
		Network:      proto.String("global/networks/default"),
		Direction:    proto.String("INGRESS"),
		Priority:     proto.Int32(1000),
		TargetTags:   []string{MailNodeTag},
		SourceRanges: []string{"0.0.0.0/0"},
		Allowed: []*computepb.Allowed{
			{IPProtocol: proto.String("tcp"), Ports: []string{"25", "587"}},
		},
	}
	existing := &computepb.Firewall{
		Network:      proto.String("global/networks/default"),
		Direction:    proto.String("INGRESS"),
		Priority:     proto.Int32(1000),
		TargetTags:   []string{MailNodeTag},
		SourceRanges: []string{"203.0.113.10/32"},
		Allowed: []*computepb.Allowed{
			{IPProtocol: proto.String("tcp"), Ports: []string{"25", "587"}},
		},
	}
	if !firewallNeedsPatch(existing, desired) {
		t.Fatal("restricted source range must require patch")
	}
}

func TestFirewallNeedsPatchIgnoresOrder(t *testing.T) {
	desired := &computepb.Firewall{
		Network:      proto.String("global/networks/default"),
		Direction:    proto.String("INGRESS"),
		Priority:     proto.Int32(1000),
		TargetTags:   []string{"other", MailNodeTag},
		SourceRanges: []string{"10.0.0.0/8", "0.0.0.0/0"},
		Allowed: []*computepb.Allowed{
			{IPProtocol: proto.String("tcp"), Ports: []string{"25", "587"}},
			{IPProtocol: proto.String("icmp")},
		},
	}
	existing := &computepb.Firewall{
		Network:      proto.String("global/networks/default"),
		Direction:    proto.String("INGRESS"),
		Priority:     proto.Int32(1000),
		TargetTags:   []string{MailNodeTag, "other"},
		SourceRanges: []string{"0.0.0.0/0", "10.0.0.0/8"},
		Allowed: []*computepb.Allowed{
			{IPProtocol: proto.String("icmp")},
			{IPProtocol: proto.String("tcp"), Ports: []string{"587", "25"}},
		},
	}
	if firewallNeedsPatch(existing, desired) {
		t.Fatal("same firewall config in different order should not require patch")
	}
}
