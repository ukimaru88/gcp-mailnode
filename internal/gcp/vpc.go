package gcp

import (
	"context"
	"fmt"
	"sync"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/proto"

	"gcp-mailnode/internal/logger"
)

// v0.1.61：进程内缓存——同 (project, region, nicCount) 只跑一次 EnsureMailVPCs。
// Stage B 并发创建 N 台多 NIC 实例时，只有第 1 台真的去 GCP 创建 VPC + 子网 + 防火墙；
// 其余台共享缓存的 specs，省 N×(7×60s)= 几分钟到十几分钟的 GCP API 串行等待。
//
// singleflight 模式：第 1 个并发请求实际执行，其余请求阻塞等结果。
// key = projectID + "|" + region + "|" + nicCount（不同 nicCount 拓扑不同要分开）。
type vpcCacheEntry struct {
	once    sync.Once
	specs   []VPCSpec
	err     error
	done    chan struct{} // 用于让后续 caller 等 once 跑完
}

var (
	vpcCacheMu sync.Mutex
	vpcCache   = map[string]*vpcCacheEntry{}
)

func vpcCacheKey(projectID, region string, nicCount int) string {
	return fmt.Sprintf("%s|%s|%d", projectID, region, nicCount)
}

// VPCSpec 描述一个邮件节点专用 VPC + 区域子网
type VPCSpec struct {
	NetworkName string // "default" / "mail-vpc-1" / ...
	SubnetName  string // GCP 子网名（default 网络下子网名也是 default）
	SubnetCIDR  string // 子网 CIDR（仅新建 VPC 有意义；default VPC 由 GCP 管理）
	SubnetURL   string // projects/<proj>/regions/<region>/subnetworks/<name>
}

// ExtraVPCNames 返回 nicCount-1 个额外 VPC 名（nic0 用 default）
// nicCount<=1 → nil；nicCount=8 → mail-vpc-1..mail-vpc-7
func ExtraVPCNames(nicCount int) []string {
	if nicCount <= 1 {
		return nil
	}
	names := make([]string, nicCount-1)
	for i := 0; i < nicCount-1; i++ {
		names[i] = fmt.Sprintf("mail-vpc-%d", i+1)
	}
	return names
}

// SubnetCIDRForVPC 返回 mail-vpc-N 的子网 CIDR (10.{200+N}.0.0/24)
// 避开 GCP default 子网（asia-northeast1: 10.146.0.0/20）
func SubnetCIDRForVPC(vpcName string) string {
	var n int
	fmt.Sscanf(vpcName, "mail-vpc-%d", &n)
	return fmt.Sprintf("10.%d.0.0/24", 200+n)
}

// EnsureMailVPCs 幂等创建 default + mail-vpc-1..mail-vpc-{nicCount-1} 及对应区域子网，
// 并在每个 VPC 上部署同款 mail-node 防火墙规则。
// v0.1.61 优化：(1) 进程内 singleflight 缓存——同 (project, region, nicCount) 只跑一次；
//             (2) 7 个 VPC + 防火墙并发创建（errgroup），从 5-7 分钟降到 1-1.5 分钟。
// 返回 []VPCSpec，[0] 始终是 default。
func (c *Client) EnsureMailVPCs(ctx context.Context, region string, nicCount int) ([]VPCSpec, error) {
	if c.projectID == "" {
		return nil, fmt.Errorf("projectID 为空")
	}

	key := vpcCacheKey(c.projectID, region, nicCount)
	vpcCacheMu.Lock()
	entry, ok := vpcCache[key]
	if !ok {
		entry = &vpcCacheEntry{done: make(chan struct{})}
		vpcCache[key] = entry
	}
	vpcCacheMu.Unlock()

	entry.once.Do(func() {
		entry.specs, entry.err = c.ensureMailVPCsActual(ctx, region, nicCount)
		close(entry.done)
		if entry.err != nil {
			// 失败不 cache——下次 call 应该重试。删除 entry 让下个 caller 重新走 once.Do
			vpcCacheMu.Lock()
			delete(vpcCache, key)
			vpcCacheMu.Unlock()
		}
	})
	// 不是第一个 caller 的等到 done
	<-entry.done
	return entry.specs, entry.err
}

// ensureMailVPCsActual 真正干活：并发创建 7 个 VPC + 子网 + 防火墙
func (c *Client) ensureMailVPCsActual(ctx context.Context, region string, nicCount int) ([]VPCSpec, error) {
	specs := []VPCSpec{
		{
			NetworkName: "default",
			SubnetName:  "default",
			SubnetURL:   fmt.Sprintf("projects/%s/regions/%s/subnetworks/default", c.projectID, region),
		},
	}

	if nicCount > 1 {
		netsCli, err := compute.NewNetworksRESTClient(ctx, option.WithTokenSource(c.tokenSource))
		if err != nil {
			return nil, fmt.Errorf("构造 Networks client: %w", err)
		}
		defer netsCli.Close()
		subsCli, err := compute.NewSubnetworksRESTClient(ctx, option.WithTokenSource(c.tokenSource))
		if err != nil {
			return nil, fmt.Errorf("构造 Subnetworks client: %w", err)
		}
		defer subsCli.Close()

		// v0.1.61：7 个 VPC + 子网并发创建。每个 VPC 是独立资源（CIDR 不重叠），无相互依赖
		extraNames := ExtraVPCNames(nicCount)
		extraSpecs := make([]VPCSpec, len(extraNames))
		errs := make([]error, len(extraNames))
		var wg sync.WaitGroup
		for i, vpcName := range extraNames {
			wg.Add(1)
			go func(idx int, name string) {
				defer wg.Done()
				if err := ensureNetwork(ctx, netsCli, c.projectID, name); err != nil {
					errs[idx] = err
					return
				}
				subnetName := name + "-" + region
				cidr := SubnetCIDRForVPC(name)
				if err := ensureSubnet(ctx, subsCli, c.projectID, region, name, subnetName, cidr); err != nil {
					errs[idx] = err
					return
				}
				extraSpecs[idx] = VPCSpec{
					NetworkName: name,
					SubnetName:  subnetName,
					SubnetCIDR:  cidr,
					SubnetURL:   fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", c.projectID, region, subnetName),
				}
			}(i, vpcName)
		}
		wg.Wait()
		for _, e := range errs {
			if e != nil {
				return nil, e
			}
		}
		specs = append(specs, extraSpecs...)
	}

	// 防火墙也并发：每个 VPC 一对 INGRESS/EGRESS 规则，独立资源
	fwErrs := make([]error, len(specs))
	var fwwg sync.WaitGroup
	for i, s := range specs {
		fwwg.Add(1)
		go func(idx int, name string) {
			defer fwwg.Done()
			if err := c.EnsureMailNodeFirewall(ctx, name); err != nil {
				fwErrs[idx] = fmt.Errorf("EnsureMailNodeFirewall %s: %w", name, err)
			}
		}(i, s.NetworkName)
	}
	fwwg.Wait()
	for _, e := range fwErrs {
		if e != nil {
			return nil, e
		}
	}

	logger.Info("EnsureMailVPCs 完成: %d 个 VPC（含 default）+ 子网 + 防火墙", len(specs))
	return specs, nil
}

func ensureNetwork(ctx context.Context, cli *compute.NetworksClient, projectID, name string) error {
	_, gerr := cli.Get(ctx, &computepb.GetNetworkRequest{Project: projectID, Network: name})
	if gerr == nil {
		logger.Info("VPC %s 已存在，跳过", name)
		return nil
	}
	if !IsNotFound(gerr) {
		return fmt.Errorf("查询 VPC %s: %w", name, gerr)
	}
	logger.Info("创建 VPC %s", name)
	op, err := cli.Insert(ctx, &computepb.InsertNetworkRequest{
		Project: projectID,
		NetworkResource: &computepb.Network{
			Name:                  proto.String(name),
			AutoCreateSubnetworks: proto.Bool(false),
			Description:           proto.String("gcp-mailnode multi-NIC mail VPC"),
		},
	})
	if err != nil {
		return fmt.Errorf("创建 VPC %s: %w", name, err)
	}
	if err := op.Wait(ctx); err != nil {
		return fmt.Errorf("等待 VPC %s 创建: %w", name, err)
	}
	logger.Info("VPC %s 创建完成", name)
	return nil
}

func ensureSubnet(ctx context.Context, cli *compute.SubnetworksClient, projectID, region, network, subnetName, cidr string) error {
	_, gerr := cli.Get(ctx, &computepb.GetSubnetworkRequest{
		Project:    projectID,
		Region:     region,
		Subnetwork: subnetName,
	})
	if gerr == nil {
		logger.Info("子网 %s 已存在，跳过", subnetName)
		return nil
	}
	if !IsNotFound(gerr) {
		return fmt.Errorf("查询子网 %s: %w", subnetName, gerr)
	}
	logger.Info("创建子网 %s (%s)", subnetName, cidr)
	op, err := cli.Insert(ctx, &computepb.InsertSubnetworkRequest{
		Project: projectID,
		Region:  region,
		SubnetworkResource: &computepb.Subnetwork{
			Name:        proto.String(subnetName),
			Network:     proto.String("global/networks/" + network),
			IpCidrRange: proto.String(cidr),
			Region:      proto.String(region),
		},
	})
	if err != nil {
		return fmt.Errorf("创建子网 %s: %w", subnetName, err)
	}
	if err := op.Wait(ctx); err != nil {
		return fmt.Errorf("等待子网 %s 创建: %w", subnetName, err)
	}
	logger.Info("子网 %s 创建完成", subnetName)
	return nil
}
