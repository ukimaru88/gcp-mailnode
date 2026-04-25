package gcp

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"

	"gcp-mailnode/internal/logger"

	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/proto"
)

// InstanceSpec 创建 VM 时的参数
type InstanceSpec struct {
	Name          string
	Zone          string
	MachineType   string // 如 "e2-micro"、"n1-standard-1"
	ImageFamily   string // 如 "debian-12"
	ImageProject  string // 如 "debian-cloud"
	DiskSizeGB    int64
	DiskType      string // pd-standard / pd-balanced / pd-ssd（默认 pd-balanced）
	Tags          []string
	StartupScript string
	StaticIP      string // 外部 IP 字符串（已经预留）
	NetworkName   string // 一般填 "default"
	// v0.1.54：Spot VM 支持（"STANDARD" 或 "SPOT"，空字符串视为 STANDARD）
	// SPOT 时 InstanceTerminationAction=DELETE（业务 3 天即抛，DELETE 比 STOP 省 IP 持有费）
	ProvisioningModel string
}

// InstanceInfo 实例概要
type InstanceInfo struct {
	Name            string
	Zone            string
	Status          string
	MachineType     string
	ExternalIP      string
	InternalIP      string
	SelfLink        string
	CreatedAt       time.Time
	Tags            []string
	TagsFingerprint string // 设置 tags 时需要带上当前 fingerprint（GCP 乐观锁）
}

// CreateInstance 创建 VM
func (c *Client) CreateInstance(ctx context.Context, spec InstanceSpec) (InstanceInfo, error) {
	if c.projectID == "" {
		return InstanceInfo{}, fmt.Errorf("projectID 为空")
	}
	cli, err := compute.NewInstancesRESTClient(ctx, option.WithTokenSource(c.tokenSource))
	if err != nil {
		return InstanceInfo{}, fmt.Errorf("构造 Instances client 失败: %w", err)
	}
	defer cli.Close()

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
		NetworkInterfaces: []*computepb.NetworkInterface{
			{
				Network: proto.String(networkURL),
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
			},
		},
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

	logger.Info("GCP CreateInstance name=%s zone=%s machineType=%s", spec.Name, spec.Zone, spec.MachineType)
	op, err := cli.Insert(ctx, req)
	if err != nil {
		return InstanceInfo{}, fmt.Errorf("Insert 实例失败: %w", err)
	}
	if err := op.Wait(ctx); err != nil {
		return InstanceInfo{}, fmt.Errorf("等待 Insert 操作完成失败: %w", err)
	}

	logger.Info("GCP CreateInstance 完成 name=%s", spec.Name)
	return c.GetInstance(ctx, spec.Zone, spec.Name)
}

// GetInstance 获取实例详情
func (c *Client) GetInstance(ctx context.Context, zone, name string) (InstanceInfo, error) {
	cli, err := compute.NewInstancesRESTClient(ctx, option.WithTokenSource(c.tokenSource))
	if err != nil {
		return InstanceInfo{}, fmt.Errorf("构造 Instances client 失败: %w", err)
	}
	defer cli.Close()

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
	cli, err := compute.NewInstancesRESTClient(ctx, option.WithTokenSource(c.tokenSource))
	if err != nil {
		return fmt.Errorf("构造 Instances client 失败: %w", err)
	}
	defer cli.Close()

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
	cli, err := compute.NewInstancesRESTClient(ctx, option.WithTokenSource(c.tokenSource))
	if err != nil {
		return nil, fmt.Errorf("构造 Instances client 失败: %w", err)
	}
	defer cli.Close()

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
	// 取第一个 AccessConfig 的 NatIP
	for _, ni := range inst.GetNetworkInterfaces() {
		if info.InternalIP == "" {
			info.InternalIP = ni.GetNetworkIP()
		}
		for _, ac := range ni.GetAccessConfigs() {
			if ip := ac.GetNatIP(); ip != "" {
				info.ExternalIP = ip
				break
			}
		}
		if info.ExternalIP != "" {
			break
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
	cli, err := compute.NewInstancesRESTClient(ctx, option.WithTokenSource(c.tokenSource))
	if err != nil {
		return fmt.Errorf("构造 Instances client 失败: %w", err)
	}
	defer cli.Close()

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
