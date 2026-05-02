package gcp

import (
	"context"
	"fmt"
	"sort"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"

	"gcp-mailnode/internal/logger"

	"google.golang.org/api/option"
	"google.golang.org/protobuf/proto"
)

// 目标 firewall 规则的名称和 tag（project 级全局规则，幂等）。
// 所有 mail-node 的 VPS 打上 MailNodeTag，自动命中这两条规则。
const (
	MailNodeTag = "mail-node"
)

// mailNodeInboundNameFor 返回某个网络的入站规则名。
// default 网络保留旧名（避免重复创建）。
func mailNodeInboundNameFor(network string) string {
	if network == "default" {
		return "mailnode-mail-ports-v2"
	}
	return "mailnode-mail-ports-" + network
}

// mailNodeOutboundNameFor 返回某个网络的出站规则名。
func mailNodeOutboundNameFor(network string) string {
	if network == "default" {
		return "mailnode-smtp-out"
	}
	return "mailnode-smtp-out-" + network
}

// EnsureMailNodeFirewall 确保指定网络下存在 mail-node 入/出站防火墙规则。
// 入站：22 + 25 + 80 + 110 + 143 + 443 + 465 + 587 + 993 + 995 + 2525 + 4190 + icmp
// 出站：25 + 587 + 465（GCP 项目级可能仍封 25 出站）
// 已存在同名规则也会校正 ports/source ranges/target tags，避免旧规则只允许部分来源。
func (c *Client) EnsureMailNodeFirewall(ctx context.Context, network string) error {
	if c.projectID == "" {
		return fmt.Errorf("projectID 为空")
	}
	if network == "" {
		network = "default"
	}
	cli, err := compute.NewFirewallsRESTClient(ctx, option.WithTokenSource(c.tokenSource))
	if err != nil {
		return fmt.Errorf("构造 Firewalls client 失败: %w", err)
	}
	defer cli.Close()

	netURL := "global/networks/" + network
	inboundName := mailNodeInboundNameFor(network)
	outboundName := mailNodeOutboundNameFor(network)

	if err := ensureFirewallRule(ctx, cli, c.projectID, &computepb.Firewall{
		Name:         proto.String(inboundName),
		Description:  proto.String("gcp-mailnode: inbound SSH + mail submission ports (" + network + ")"),
		Network:      proto.String(netURL),
		Direction:    proto.String("INGRESS"),
		Priority:     proto.Int32(1000),
		TargetTags:   []string{MailNodeTag},
		SourceRanges: []string{"0.0.0.0/0"},
		Allowed: []*computepb.Allowed{
			{IPProtocol: proto.String("tcp"), Ports: []string{"22", "25", "80", "110", "143", "443", "465", "587", "993", "995", "2525", "4190"}},
			{IPProtocol: proto.String("icmp")},
		},
	}); err != nil {
		return fmt.Errorf("创建入站规则 %s 失败: %w", inboundName, err)
	}

	if err := ensureFirewallRule(ctx, cli, c.projectID, &computepb.Firewall{
		Name:              proto.String(outboundName),
		Description:       proto.String("gcp-mailnode: outbound SMTP ports 25/465/587 (" + network + ")"),
		Network:           proto.String(netURL),
		Direction:         proto.String("EGRESS"),
		Priority:          proto.Int32(1000),
		TargetTags:        []string{MailNodeTag},
		DestinationRanges: []string{"0.0.0.0/0"},
		Allowed: []*computepb.Allowed{
			{IPProtocol: proto.String("tcp"), Ports: []string{"25", "465", "587"}},
		},
	}); err != nil {
		return fmt.Errorf("创建出站规则 %s 失败: %w", outboundName, err)
	}

	return nil
}

// ensureFirewallRule 幂等创建/校正。已存在但内容不完整时会 Patch 到目标配置。
func ensureFirewallRule(ctx context.Context, cli *compute.FirewallsClient, project string, rule *computepb.Firewall) error {
	name := rule.GetName()
	existing, err := cli.Get(ctx, &computepb.GetFirewallRequest{
		Project:  project,
		Firewall: name,
	})
	if err == nil {
		if !firewallNeedsPatch(existing, rule) {
			logger.Info("firewall 规则 %s 已存在且配置正确，跳过", name)
			return nil
		}
		logger.Info("firewall 规则 %s 已存在但配置不完整，正在校正", name)
		op, err := cli.Patch(ctx, &computepb.PatchFirewallRequest{
			Project:          project,
			Firewall:         name,
			FirewallResource: rule,
		})
		if err != nil {
			return fmt.Errorf("Patch firewall %s 失败: %w", name, err)
		}
		if err := op.Wait(ctx); err != nil {
			return fmt.Errorf("等待 firewall %s patch 完成失败: %w", name, err)
		}
		logger.Info("firewall 规则 %s 校正完成", name)
		return nil
	}
	if !IsNotFound(err) {
		return fmt.Errorf("查询 firewall %s 失败: %w", name, err)
	}
	logger.Info("创建 firewall 规则 %s", name)
	op, err := cli.Insert(ctx, &computepb.InsertFirewallRequest{
		Project:          project,
		FirewallResource: rule,
	})
	if err != nil {
		return fmt.Errorf("插入 firewall 失败: %w", err)
	}
	if err := op.Wait(ctx); err != nil {
		return fmt.Errorf("等待 firewall 插入完成失败: %w", err)
	}
	logger.Info("firewall 规则 %s 创建完成", name)
	return nil
}

func firewallNeedsPatch(existing, desired *computepb.Firewall) bool {
	if existing.GetDirection() != desired.GetDirection() {
		return true
	}
	if existing.GetPriority() != desired.GetPriority() {
		return true
	}
	if existing.GetNetwork() != desired.GetNetwork() {
		return true
	}
	if !sameStringSet(existing.GetTargetTags(), desired.GetTargetTags()) {
		return true
	}
	if !sameStringSet(existing.GetSourceRanges(), desired.GetSourceRanges()) {
		return true
	}
	if !sameStringSet(existing.GetDestinationRanges(), desired.GetDestinationRanges()) {
		return true
	}
	return !sameAllowed(existing.GetAllowed(), desired.GetAllowed())
}

func sameAllowed(a, b []*computepb.Allowed) bool {
	if len(a) != len(b) {
		return false
	}
	normalize := func(in []*computepb.Allowed) []string {
		out := make([]string, 0, len(in))
		for _, item := range in {
			ports := append([]string{}, item.GetPorts()...)
			sort.Strings(ports)
			out = append(out, item.GetIPProtocol()+":"+fmt.Sprint(ports))
		}
		sort.Strings(out)
		return out
	}
	aa := normalize(a)
	bb := normalize(b)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa := append([]string{}, a...)
	bb := append([]string{}, b...)
	sort.Strings(aa)
	sort.Strings(bb)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
