package gcp

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"cloud.google.com/go/compute/apiv1/computepb"

	"gcp-mailnode/internal/logger"

	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/proto"
)

// NICSpec 单个网络接口的配置（v0.1.57 多 NIC 用）
type NICSpec struct {
	NetworkName string // "default" / "mail-vpc-1" / ...
	SubnetURL   string // projects/<proj>/regions/<region>/subnetworks/<name>（可空）
	StaticIP    string // 该 NIC 的外部静态 IP（可空 → 临时 IP）
}

// InstanceSpec 创建 VM 时的参数
type InstanceSpec struct {
	Name          string
	Zone          string
	MachineType   string // 如 "e2-micro"、"n1-standard-1"、"n2-custom-8-16384"
	ImageFamily   string // 如 "debian-12"
	ImageProject  string // 如 "debian-cloud"
	DiskSizeGB    int64
	DiskType      string // pd-standard / pd-balanced / pd-ssd（默认 pd-balanced）
	Tags          []string
	StartupScript string
	StaticIP      string // 单 NIC 模式用；NICs 非空时忽略
	NetworkName   string // 单 NIC 模式用；NICs 非空时忽略
	// v0.1.54：Spot VM 支持（"STANDARD" 或 "SPOT"，空字符串视为 STANDARD）
	// SPOT 时 InstanceTerminationAction=DELETE（业务 3 天即抛，DELETE 比 STOP 省 IP 持有费）
	ProvisioningModel string
	// v0.1.57：多 NIC 支持。非空时取代单 NIC 路径，每元素一个 NetworkInterface。
	NICs []NICSpec
}

// NICInfo 单个 NIC 的内/外网 IP（v0.1.57 多 NIC 用）
type NICInfo struct {
	InternalIP string // GCP 子网内网 IP
	ExternalIP string // 外部静态 IP（access config NatIP）
	NICName    string // GCP 内部 NIC 名（nic0/nic1/...）
}

// InstanceInfo 实例概要
type InstanceInfo struct {
	Name            string
	Zone            string
	Status          string
	MachineType     string
	ExternalIP      string // = NICs[0].ExternalIP（兼容旧调用）
	InternalIP      string // = NICs[0].InternalIP（兼容旧调用）
	SelfLink        string
	CreatedAt       time.Time
	Tags            []string
	TagsFingerprint string // 设置 tags 时需要带上当前 fingerprint（GCP 乐观锁）
	NICs            []NICInfo
}

// CreateInstance 创建 VM
func (c *Client) CreateInstance(ctx context.Context, spec InstanceSpec) (InstanceInfo, error) {
	if c.projectID == "" {
		return InstanceInfo{}, fmt.Errorf("projectID 为空")
	}
	cli, err := c.instances(ctx)
	if err != nil {
		return InstanceInfo{}, fmt.Errorf("构造 Instances client 失败: %w", err)
	}

	network := spec.NetworkName
	if network == "" {
		network = "default"
	}

	machineTypeURL := fmt.Sprintf("zones/%s/machineTypes/%s", spec.Zone, spec.MachineType)
	sourceImage := fmt.Sprintf("projects/%s/global/images/family/%s", spec.ImageProject, spec.ImageFamily)
	networkURL := fmt.Sprintf("global/networks/%s", network)
	diskType := spec.DiskType
	if diskType == "" {
		diskType = "pd-balanced"
	}
	diskTypeURL := fmt.Sprintf("zones/%s/diskTypes/%s", spec.Zone, diskType)

	instance := &computepb.Instance{
		Name:        proto.String(spec.Name),
		MachineType: proto.String(machineTypeURL),
		Disks: []*computepb.AttachedDisk{
			{
				Boot:       proto.Bool(true),
				AutoDelete: proto.Bool(true),
				InitializeParams: &computepb.AttachedDiskInitializeParams{
					DiskSizeGb:  proto.Int64(spec.DiskSizeGB),
					SourceImage: proto.String(sourceImage),
					DiskType:    proto.String(diskTypeURL),
				},
			},
		},
		NetworkInterfaces: buildNetworkInterfaces(spec, networkURL),
	}

	if spec.StartupScript != "" {
		ss := spec.StartupScript
		instance.Metadata = &computepb.Metadata{
			Items: []*computepb.Items{
				{
					Key:   proto.String("startup-script"),
					Value: &ss,
				},
			},
		}
	}

	if len(spec.Tags) > 0 {
		instance.Tags = &computepb.Tags{Items: spec.Tags}
	}

	// Spot VM 调度配置（约 73% 折扣，可被抢占）
	if strings.EqualFold(spec.ProvisioningModel, "SPOT") {
		instance.Scheduling = &computepb.Scheduling{
			ProvisioningModel:         proto.String("SPOT"),
			InstanceTerminationAction: proto.String("DELETE"),
			AutomaticRestart:          proto.Bool(false),
			OnHostMaintenance:         proto.String("TERMINATE"),
		}
	}

	req := &computepb.InsertInstanceRequest{
		Project:          c.projectID,
		Zone:             spec.Zone,
		InstanceResource: instance,
	}

	if len(spec.NICs) > 0 {
		logger.Info("GCP CreateInstance multi-NIC name=%s nicCount=%d", spec.Name, len(spec.NICs))
	}
	logger.Info("GCP CreateInstance name=%s zone=%s machineType=%s", spec.Name, spec.Zone, spec.MachineType)
	op, err := cli.Insert(ctx, req)
	if err != nil {
		return InstanceInfo{}, fmt.Errorf("Insert 实例失败: %w", err)
	}
	if err := op.Wait(ctx); err != nil {
		// v0.2.9：Insert 已提交，实例可能已在创建——Wait 失败（含 ctx 取消）不删会留孤儿 VM 计费。
		// best-effort 删除，用 background ctx 避免取消时删不掉。
		_ = c.DeleteInstance(context.Background(), spec.Zone, spec.Name)
		return InstanceInfo{}, fmt.Errorf("等待 Insert 操作完成失败: %w", err)
	}

	logger.Info("GCP CreateInstance 完成 name=%s", spec.Name)
	return c.GetInstance(ctx, spec.Zone, spec.Name)
}

// buildNetworkInterfaces 按 spec.NICs 长度展开多 NIC；非空时优先，否则走单 NIC 兼容路径。
func buildNetworkInterfaces(spec InstanceSpec, defaultNetworkURL string) []*computepb.NetworkInterface {
	if len(spec.NICs) > 0 {
		out := make([]*computepb.NetworkInterface, 0, len(spec.NICs))
		for _, n := range spec.NICs {
			netURL := "global/networks/" + n.NetworkName
			ni := &computepb.NetworkInterface{
				Network: proto.String(netURL),
			}
			if n.SubnetURL != "" {
				ni.Subnetwork = proto.String(n.SubnetURL)
			}
			ac := &computepb.AccessConfig{
				Name: proto.String("External NAT"),
				Type: proto.String("ONE_TO_ONE_NAT"),
			}
			if n.StaticIP != "" {
				ip := n.StaticIP
				ac.NatIP = &ip
			}
			ni.AccessConfigs = []*computepb.AccessConfig{ac}
			out = append(out, ni)
		}
		return out
	}
	ni := &computepb.NetworkInterface{
		Network: proto.String(defaultNetworkURL),
		AccessConfigs: []*computepb.AccessConfig{
			{
				Name: proto.String("External NAT"),
				Type: proto.String("ONE_TO_ONE_NAT"),
				NatIP: func() *string {
					if spec.StaticIP == "" {
						return nil
					}
					s := spec.StaticIP
					return &s
				}(),
			},
		},
	}
	return []*computepb.NetworkInterface{ni}
}

// GetInstance 获取实例详情
func (c *Client) GetInstance(ctx context.Context, zone, name string) (InstanceInfo, error) {
	cli, err := c.instances(ctx)
	if err != nil {
		return InstanceInfo{}, fmt.Errorf("构造 Instances client 失败: %w", err)
	}

	req := &computepb.GetInstanceRequest{
		Project:  c.projectID,
		Zone:     zone,
		Instance: name,
	}
	inst, err := cli.Get(ctx, req)
	if err != nil {
		return InstanceInfo{}, fmt.Errorf("Get 实例失败: %w", err)
	}
	return instanceToInfo(inst), nil
}

// DeleteInstance 删除实例
func (c *Client) DeleteInstance(ctx context.Context, zone, name string) error {
	cli, err := c.instances(ctx)
	if err != nil {
		return fmt.Errorf("构造 Instances client 失败: %w", err)
	}

	logger.Info("GCP DeleteInstance name=%s zone=%s", name, zone)
	op, err := cli.Delete(ctx, &computepb.DeleteInstanceRequest{
		Project:  c.projectID,
		Zone:     zone,
		Instance: name,
	})
	if err != nil {
		return fmt.Errorf("Delete 实例失败: %w", err)
	}
	if err := op.Wait(ctx); err != nil {
		return fmt.Errorf("等待 Delete 操作完成失败: %w", err)
	}
	logger.Info("GCP DeleteInstance 完成 name=%s", name)
	return nil
}

// ListInstances 列出指定 zone 下的所有实例
func (c *Client) ListInstances(ctx context.Context, zone string) ([]InstanceInfo, error) {
	cli, err := c.instances(ctx)
	if err != nil {
		return nil, fmt.Errorf("构造 Instances client 失败: %w", err)
	}

	req := &computepb.ListInstancesRequest{
		Project: c.projectID,
		Zone:    zone,
	}
	it := cli.List(ctx, req)
	var out []InstanceInfo
	for {
		inst, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("List 实例失败: %w", err)
		}
		out = append(out, instanceToInfo(inst))
	}
	return out, nil
}

// WaitForRunning 轮询直到实例状态为 RUNNING 或超时
func (c *Client) WaitForRunning(ctx context.Context, zone, name string, timeout time.Duration) (InstanceInfo, error) {
	deadline := time.Now().Add(timeout)
	for {
		info, err := c.GetInstance(ctx, zone, name)
		if err == nil && info.Status == "RUNNING" {
			return info, nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return InstanceInfo{}, fmt.Errorf("等待实例 RUNNING 超时: %w", err)
			}
			return info, fmt.Errorf("等待实例 RUNNING 超时: 当前状态 %s", info.Status)
		}
		select {
		case <-ctx.Done():
			return InstanceInfo{}, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// instanceToInfo 将 computepb.Instance 转换为 InstanceInfo
func instanceToInfo(inst *computepb.Instance) InstanceInfo {
	info := InstanceInfo{
		Name:     inst.GetName(),
		Status:   inst.GetStatus(),
		SelfLink: inst.GetSelfLink(),
	}
	// Zone 字段通常是全 URL：".../zones/asia-northeast1-a"
	if z := inst.GetZone(); z != "" {
		info.Zone = path.Base(z)
	}
	// MachineType 同理：".../zones/xxx/machineTypes/e2-micro"
	if mt := inst.GetMachineType(); mt != "" {
		info.MachineType = path.Base(mt)
	}
	// v0.1.57：填充全部 NIC 信息（NICs[0] 是 nic0）；同时把 nic0 的 IP 投到顶层兼容字段
	for idx, ni := range inst.GetNetworkInterfaces() {
		nicInfo := NICInfo{
			InternalIP: ni.GetNetworkIP(),
			NICName:    ni.GetName(),
		}
		for _, ac := range ni.GetAccessConfigs() {
			if ip := ac.GetNatIP(); ip != "" {
				nicInfo.ExternalIP = ip
				break
			}
		}
		info.NICs = append(info.NICs, nicInfo)
		if idx == 0 {
			info.InternalIP = nicInfo.InternalIP
			info.ExternalIP = nicInfo.ExternalIP
		}
	}
	if ts := inst.GetCreationTimestamp(); ts != "" {
		// RFC3339 格式
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			info.CreatedAt = t
		} else if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			info.CreatedAt = t
		} else if strings.Contains(ts, "T") {
			// fallback：尽量解析
			if t, err := time.Parse("2006-01-02T15:04:05-07:00", ts); err == nil {
				info.CreatedAt = t
			}
		}
	}
	if tags := inst.GetTags(); tags != nil {
		info.Tags = append(info.Tags, tags.GetItems()...)
		info.TagsFingerprint = tags.GetFingerprint()
	}
	return info
}

// SetInstanceTags 补齐/覆盖 VM 的 network tags（用于修复漏打 mail-node 的机器）
// 调用前必须 GetInstance 拿到 fingerprint。传 append=true 时保留现有 tags 并追加新 tag。
func (c *Client) SetInstanceTags(ctx context.Context, zone, instanceName string, tags []string, fingerprint string) error {
	cli, err := c.instances(ctx)
	if err != nil {
		return fmt.Errorf("构造 Instances client 失败: %w", err)
	}

	logger.Info("GCP SetInstanceTags name=%s zone=%s tags=%v", instanceName, zone, tags)
	op, err := cli.SetTags(ctx, &computepb.SetTagsInstanceRequest{
		Project:  c.projectID,
		Zone:     zone,
		Instance: instanceName,
		TagsResource: &computepb.Tags{
			Items:       tags,
			Fingerprint: proto.String(fingerprint),
		},
	})
	if err != nil {
		return fmt.Errorf("SetTags 失败: %w", err)
	}
	if err := op.Wait(ctx); err != nil {
		return fmt.Errorf("等待 SetTags 完成失败: %w", err)
	}
	logger.Info("GCP SetInstanceTags 完成 name=%s", instanceName)
	return nil
}
