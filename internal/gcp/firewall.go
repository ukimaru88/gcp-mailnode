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
	// v2: 加 IMAP/POP3/993/995/4190 支持 mailcow 收件
	mailNodeInboundName  = "mailnode-mail-ports-v2"
	mailNodeOutboundName = "mailnode-smtp-out"
	// v3: 25 端口单独走全开规则（让对方 MX bounce 回执可达）
	mailNodeMxInboundName = "mailnode-mx-in-v3"
	// IAP 浏览器 SSH 段（GCP Console 用），收紧白名单时默认追加，作为锁死自救通道
	gcpIAPSourceRange = "35.235.240.0/20"
)

// EnsureMailNodeFirewall 确保 project 下存在 mail-node 入/出站防火墙规则。
//
// allowedSourceIPs 为空（nil 或 len==0）时：维持历史"全开"行为，v2 入站规则 SourceRanges = 0.0.0.0/0，
// 不创建 v3 mx-in 规则——保证首次部署能 SSH 进 VPS 部署 KumoMTA。
//
// allowedSourceIPs 非空时（用户主动收紧）：
//   - v2 入站规则 SourceRanges 改为 allowedSourceIPs ∪ {35.235.240.0/20}（IAP 段，自救通道），端口列表保持原样
//   - v3 入站规则 mailnode-mx-in-v3 创建：SourceRanges=0.0.0.0/0、tcp:25 only（bounce 回执用）
//   - 出站 mailnode-smtp-out 完全不动
//
// 已存在规则时：检测 SourceRanges 差异，不一致时 Patch（不删不重建，避免 SSH 中断窗口）。
func (c *Client) EnsureMailNodeFirewall(ctx context.Context, allowedSourceIPs []string) error {
	if c.projectID == "" {
		return fmt.Errorf("projectID 为空")
	}
	cli, err := compute.NewFirewallsRESTClient(ctx, option.WithTokenSource(c.tokenSource))
	if err != nil {
		return fmt.Errorf("构造 Firewalls client 失败: %w", err)
	}
	defer cli.Close()

	// 决定 v2 入站的 SourceRanges
	v2Sources := []string{"0.0.0.0/0"}
	if len(allowedSourceIPs) > 0 {
		v2Sources = append([]string{}, allowedSourceIPs...)
		v2Sources = append(v2Sources, gcpIAPSourceRange)
		v2Sources = dedupStrings(v2Sources)
	}

	if err := ensureFirewallRule(ctx, cli, c.projectID, &computepb.Firewall{
		Name:         proto.String(mailNodeInboundName),
		Description:  proto.String("gcp-mailnode: inbound SSH + mail submission ports"),
		Network:      proto.String("global/networks/default"),
		Direction:    proto.String("INGRESS"),
		Priority:     proto.Int32(1000),
		TargetTags:   []string{MailNodeTag},
		SourceRanges: v2Sources,
		Allowed: []*computepb.Allowed{
			{IPProtocol: proto.String("tcp"), Ports: []string{"22", "25", "80", "110", "143", "443", "465", "587", "993", "995", "2525", "4190"}},
			{IPProtocol: proto.String("icmp")},
		},
	}); err != nil {
		return fmt.Errorf("处理入站规则 %s 失败: %w", mailNodeInboundName, err)
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
		return fmt.Errorf("处理出站规则 %s 失败: %w", mailNodeOutboundName, err)
	}

	// v3 mx-in：仅在收紧模式下需要（让 25 入站继续可达）；
	// 全开模式下 v2 已经放行 25，v3 创建反而引入冗余规则。
	if len(allowedSourceIPs) > 0 {
		if err := ensureFirewallRule(ctx, cli, c.projectID, &computepb.Firewall{
			Name:         proto.String(mailNodeMxInboundName),
			Description:  proto.String("gcp-mailnode: inbound 25 only (MX / bounce DSN)"),
			Network:      proto.String("global/networks/default"),
			Direction:    proto.String("INGRESS"),
			Priority:     proto.Int32(1000),
			TargetTags:   []string{MailNodeTag},
			SourceRanges: []string{"0.0.0.0/0"},
			Allowed: []*computepb.Allowed{
				{IPProtocol: proto.String("tcp"), Ports: []string{"25"}},
			},
		}); err != nil {
			return fmt.Errorf("处理入站规则 %s 失败: %w", mailNodeMxInboundName, err)
		}
	} else {
		// 全开模式下若历史上创建过 v3，删掉避免冗余
		if err := deleteFirewallRuleIfExists(ctx, cli, c.projectID, mailNodeMxInboundName); err != nil {
			return fmt.Errorf("删除冗余规则 %s 失败: %w", mailNodeMxInboundName, err)
		}
	}

	return nil
}

// RestoreLegacyFirewall 把入站规则恢复为全开（SourceRanges=0.0.0.0/0）并删除 v3 mx-in 规则。
// 用作"恢复全开"按钮的后端实现，本质等价于 EnsureMailNodeFirewall(ctx, nil)。
func (c *Client) RestoreLegacyFirewall(ctx context.Context) error {
	return c.EnsureMailNodeFirewall(ctx, nil)
}

// ensureFirewallRule 幂等创建/更新。
// 已存在时：比对 SourceRanges，不一致就 Patch；一致就跳过。
// 不存在时：Insert。
func ensureFirewallRule(ctx context.Context, cli *compute.FirewallsClient, project string, rule *computepb.Firewall) error {
	name := rule.GetName()
	existing, err := cli.Get(ctx, &computepb.GetFirewallRequest{
		Project:  project,
		Firewall: name,
	})
	if err == nil {
		if sourceRangesEqual(existing.SourceRanges, rule.SourceRanges) {
			logger.Info("firewall 规则 %s 已存在且 SourceRanges 一致，跳过", name)
			return nil
		}
		logger.Info("firewall 规则 %s 已存在但 SourceRanges 不一致，Patch 更新", name)
		op, err := cli.Patch(ctx, &computepb.PatchFirewallRequest{
			Project:          project,
			Firewall:         name,
			FirewallResource: rule,
		})
		if err != nil {
			return fmt.Errorf("patch firewall %s 失败: %w", name, err)
		}
		if err := op.Wait(ctx); err != nil {
			return fmt.Errorf("等待 firewall %s patch 完成失败: %w", name, err)
		}
		logger.Info("firewall 规则 %s patch 完成", name)
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
		return fmt.Errorf("插入 firewall %s 失败: %w", name, err)
	}
	if err := op.Wait(ctx); err != nil {
		return fmt.Errorf("等待 firewall %s 插入完成失败: %w", name, err)
	}
	logger.Info("firewall 规则 %s 创建完成", name)
	return nil
}

// deleteFirewallRuleIfExists 删除指定 firewall 规则，不存在则视为成功。
func deleteFirewallRuleIfExists(ctx context.Context, cli *compute.FirewallsClient, project, name string) error {
	op, err := cli.Delete(ctx, &computepb.DeleteFirewallRequest{
		Project:  project,
		Firewall: name,
	})
	if err != nil {
		if IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("删除 firewall %s 失败: %w", name, err)
	}
	if err := op.Wait(ctx); err != nil {
		if IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("等待 firewall %s 删除完成失败: %w", name, err)
	}
	logger.Info("firewall 规则 %s 已删除", name)
	return nil
}

// sourceRangesEqual 比较两个 CIDR 列表是否等价（顺序无关、去重后等长且全部相同）。
func sourceRangesEqual(a, b []string) bool {
	a2 := dedupStrings(a)
	b2 := dedupStrings(b)
	if len(a2) != len(b2) {
		return false
	}
	for i := range a2 {
		if a2[i] != b2[i] {
			return false
		}
	}
	return true
}

// dedupStrings 返回去重并排序后的字符串切片（用于比对/规范化）。
func dedupStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
