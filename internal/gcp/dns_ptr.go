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

// 约定：access config 名 "External NAT"；NIC 名 nic0/nic1/...
const (
	defaultAccessConfigName = "External NAT"
)

// nicNameFor 返回 GCP NetworkInterface 名（nic0、nic1、...）
func nicNameFor(idx int) string {
	if idx <= 0 {
		return "nic0"
	}
	return fmt.Sprintf("nic%d", idx)
}

// SetInstancePTR 给 nic0 设置公网 PTR（向后兼容）
// 等价于 SetInstancePTRForNIC(ctx, zone, instanceName, 0, ip, fqdn)
func (c *Client) SetInstancePTR(ctx context.Context, zone, instanceName, ip, fqdn string) error {
	return c.SetInstancePTRForNIC(ctx, zone, instanceName, 0, ip, fqdn)
}

// SetInstancePTRForNIC v0.1.57：给指定 NIC（nic0/nic1/...）设置公网 PTR。
// 做法（同 SetInstancePTR）：
//  1. DeleteAccessConfig 删掉该 NIC 的默认 access config（"External NAT"）
//  2. AddAccessConfig 重新添加，带 PublicPtrDomainName + SetPublicPtr=true
func (c *Client) SetInstancePTRForNIC(ctx context.Context, zone, instanceName string, nicIndex int, ip, fqdn string) error {
	if c.projectID == "" {
		return fmt.Errorf("projectID 为空")
	}
	cli, err := compute.NewInstancesRESTClient(ctx, option.WithTokenSource(c.tokenSource))
	if err != nil {
		return fmt.Errorf("构造 Instances client 失败: %w", err)
	}
	defer cli.Close()

	// 1) 删掉默认 access config
	logger.Info("GCP SetInstancePTR step1 删除 AccessConfig instance=%s", instanceName)
	delReq := &computepb.DeleteAccessConfigInstanceRequest{
		Project:          c.projectID,
		Zone:             zone,
		Instance:         instanceName,
		AccessConfig:     defaultAccessConfigName,
		NetworkInterface: nicNameFor(nicIndex),
	}
	delOp, err := cli.DeleteAccessConfig(ctx, delReq)
	if err != nil {
		return fmt.Errorf("DeleteAccessConfig 失败: %w", err)
	}
	if err := delOp.Wait(ctx); err != nil {
		return fmt.Errorf("等待 DeleteAccessConfig 完成失败: %w", err)
	}

	// 2) 重新添加带 PTR 的 access config
	logger.Info("GCP SetInstancePTR step2 添加带 PTR 的 AccessConfig instance=%s ip=%s fqdn=%s", instanceName, ip, fqdn)
	addReq := &computepb.AddAccessConfigInstanceRequest{
		Project:          c.projectID,
		Zone:             zone,
		Instance:         instanceName,
		NetworkInterface: nicNameFor(nicIndex),
		AccessConfigResource: &computepb.AccessConfig{
			Name:                proto.String(defaultAccessConfigName),
			Type:                proto.String("ONE_TO_ONE_NAT"),
			NatIP:               proto.String(ip),
			PublicPtrDomainName: proto.String(fqdn),
			SetPublicPtr:        proto.Bool(true),
		},
	}
	addOp, err := cli.AddAccessConfig(ctx, addReq)
	if err != nil {
		if restoreErr := restoreAccessConfig(ctx, cli, c.projectID, zone, instanceName, nicIndex, ip); restoreErr != nil {
			return fmt.Errorf("AddAccessConfig 失败: %w；恢复原公网 NAT 也失败: %v", err, restoreErr)
		}
		return fmt.Errorf("AddAccessConfig 失败，已恢复原公网 NAT: %w", err)
	}
	if err := addOp.Wait(ctx); err != nil {
		if restoreErr := restoreAccessConfig(ctx, cli, c.projectID, zone, instanceName, nicIndex, ip); restoreErr != nil {
			return fmt.Errorf("等待 AddAccessConfig 完成失败: %w；恢复原公网 NAT 也失败: %v", err, restoreErr)
		}
		return fmt.Errorf("等待 AddAccessConfig 完成失败，已恢复原公网 NAT: %w", err)
	}
	logger.Info("GCP SetInstancePTR 完成 instance=%s fqdn=%s", instanceName, fqdn)
	return nil
}

func restoreAccessConfig(ctx context.Context, cli *compute.InstancesClient, projectID, zone, instanceName string, nicIndex int, ip string) error {
	logger.Warn("GCP SetInstancePTR 恢复原 AccessConfig instance=%s ip=%s", instanceName, ip)
	addReq := &computepb.AddAccessConfigInstanceRequest{
		Project:          projectID,
		Zone:             zone,
		Instance:         instanceName,
		NetworkInterface: nicNameFor(nicIndex),
		AccessConfigResource: &computepb.AccessConfig{
			Name:  proto.String(defaultAccessConfigName),
			Type:  proto.String("ONE_TO_ONE_NAT"),
			NatIP: proto.String(ip),
		},
	}
	op, err := cli.AddAccessConfig(ctx, addReq)
	if err != nil {
		return err
	}
	return op.Wait(ctx)
}

// ClearInstancePTR 清除 nic0 的 PTR：重建 access config 但不带 PTR 字段
func (c *Client) ClearInstancePTR(ctx context.Context, zone, instanceName, ip string) error {
	nicIndex := 0
	if c.projectID == "" {
		return fmt.Errorf("projectID 为空")
	}
	cli, err := compute.NewInstancesRESTClient(ctx, option.WithTokenSource(c.tokenSource))
	if err != nil {
		return fmt.Errorf("构造 Instances client 失败: %w", err)
	}
	defer cli.Close()

	logger.Info("GCP ClearInstancePTR step1 删除 AccessConfig instance=%s", instanceName)
	delReq := &computepb.DeleteAccessConfigInstanceRequest{
		Project:          c.projectID,
		Zone:             zone,
		Instance:         instanceName,
		AccessConfig:     defaultAccessConfigName,
		NetworkInterface: nicNameFor(nicIndex),
	}
	delOp, err := cli.DeleteAccessConfig(ctx, delReq)
	if err != nil {
		return fmt.Errorf("DeleteAccessConfig 失败: %w", err)
	}
	if err := delOp.Wait(ctx); err != nil {
		return fmt.Errorf("等待 DeleteAccessConfig 完成失败: %w", err)
	}

	logger.Info("GCP ClearInstancePTR step2 添加不带 PTR 的 AccessConfig instance=%s ip=%s", instanceName, ip)
	addReq := &computepb.AddAccessConfigInstanceRequest{
		Project:          c.projectID,
		Zone:             zone,
		Instance:         instanceName,
		NetworkInterface: nicNameFor(nicIndex),
		AccessConfigResource: &computepb.AccessConfig{
			Name:  proto.String(defaultAccessConfigName),
			Type:  proto.String("ONE_TO_ONE_NAT"),
			NatIP: proto.String(ip),
		},
	}
	addOp, err := cli.AddAccessConfig(ctx, addReq)
	if err != nil {
		return fmt.Errorf("AddAccessConfig 失败: %w", err)
	}
	if err := addOp.Wait(ctx); err != nil {
		return fmt.Errorf("等待 AddAccessConfig 完成失败: %w", err)
	}
	logger.Info("GCP ClearInstancePTR 完成 instance=%s", instanceName)
	return nil
}
