package gcp

import (
	"context"
	"fmt"

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
	// v2: 加 IMAP/POP3/993/995/4190 支持 mailcow 收件
	mailNodeInboundName  = "mailnode-mail-ports-v2"
	mailNodeOutboundName = "mailnode-smtp-out"
)

// EnsureMailNodeFirewall 确保 project 下存在 mail-node 入/出站防火墙规则。
// 入站：22 (SSH) + 25 (SMTP) + 465 (SMTPS) + 587 (Submission) + 2525 (Alt) + 80/443 (ACME)
// 出站：25 + 587 + 465（GCP 对新项目大多封 25 出站，这里显式 allow 但仍受项目级限制）
// 已存在同名规则则跳过（不改动现有规则内容，避免覆盖用户手动调整）。
func (c *Client) EnsureMailNodeFirewall(ctx context.Context) error {
	if c.projectID == "" {
		return fmt.Errorf("projectID 为空")
	}
	cli, err := compute.NewFirewallsRESTClient(ctx, option.WithTokenSource(c.tokenSource))
	if err != nil {
		return fmt.Errorf("构造 Firewalls client 失败: %w", err)
	}
	defer cli.Close()

	if err := ensureFirewallRule(ctx, cli, c.projectID, &computepb.Firewall{
		Name:         proto.String(mailNodeInboundName),
		Description:  proto.String("gcp-mailnode: inbound SSH + mail submission ports"),
		Network:      proto.String("global/networks/default"),
		Direction:    proto.String("INGRESS"),
		Priority:     proto.Int32(1000),
		TargetTags:   []string{MailNodeTag},
		SourceRanges: []string{"0.0.0.0/0"},
		Allowed: []*computepb.Allowed{
			{IPProtocol: proto.String("tcp"), Ports: []string{"22", "25", "80", "110", "143", "443", "465", "587", "993", "995", "2525", "4190"}},
			{IPProtocol: proto.String("icmp")},
		},
	}); err != nil {
		return fmt.Errorf("创建入站规则 %s 失败: %w", mailNodeInboundName, err)
	}

	if err := ensureFirewallRule(ctx, cli, c.projectID, &computepb.Firewall{
		Name:              proto.String(mailNodeOutboundName),
		Description:       proto.String("gcp-mailnode: outbound SMTP ports (25/465/587)"),
		Network:           proto.String("global/networks/default"),
		Direction:         proto.String("EGRESS"),
		Priority:          proto.Int32(1000),
		TargetTags:        []string{MailNodeTag},
		DestinationRanges: []string{"0.0.0.0/0"},
		Allowed: []*computepb.Allowed{
			{IPProtocol: proto.String("tcp"), Ports: []string{"25", "465", "587"}},
		},
	}); err != nil {
		return fmt.Errorf("创建出站规则 %s 失败: %w", mailNodeOutboundName, err)
	}

	return nil
}

// ensureFirewallRule 幂等创建。已存在则跳过（不改动现有规则）。
func ensureFirewallRule(ctx context.Context, cli *compute.FirewallsClient, project string, rule *computepb.Firewall) error {
	name := rule.GetName()
	_, err := cli.Get(ctx, &computepb.GetFirewallRequest{
		Project:  project,
		Firewall: name,
	})
	if err == nil {
		logger.Info("firewall 规则 %s 已存在，跳过", name)
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
