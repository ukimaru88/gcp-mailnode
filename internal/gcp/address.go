package gcp

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"cloud.google.com/go/compute/apiv1/computepb"

	"gcp-mailnode/internal/logger"

	"github.com/google/uuid"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/proto"
)

// AddressInfo 静态 IP 资源概要
type AddressInfo struct {
	Name      string
	Region    string
	IP        string
	Status    string
	Users     []string
	CreatedAt time.Time
}

// ReserveStaticAddress 在指定 region 预留一个 EXTERNAL 静态 IP。
// 如果 name 为空，自动生成 "addr-<uuid8>"。
// 返回时会再 Get 一次拿到分配的 IP。
func (c *Client) ReserveStaticAddress(ctx context.Context, region, name string) (AddressInfo, error) {
	if c.projectID == "" {
		return AddressInfo{}, fmt.Errorf("projectID 为空")
	}
	cli, err := c.addresses(ctx)
	if err != nil {
		return AddressInfo{}, fmt.Errorf("构造 Addresses client 失败: %w", err)
	}

	if name == "" {
		u := uuid.NewString()
		name = "addr-" + strings.ReplaceAll(u, "-", "")[:8]
	}

	req := &computepb.InsertAddressRequest{
		Project: c.projectID,
		Region:  region,
		AddressResource: &computepb.Address{
			Name:        proto.String(name),
			AddressType: proto.String("EXTERNAL"),
		},
	}

	logger.Info("GCP ReserveStaticAddress name=%s region=%s", name, region)
	op, err := cli.Insert(ctx, req)
	if err != nil {
		return AddressInfo{}, fmt.Errorf("Insert Address 失败: %w", err)
	}
	if err := op.Wait(ctx); err != nil {
		// v0.2.9：Insert 已提交，IP 可能已预留成功——Wait 失败（含 ctx 取消）不释放会留孤儿 IP 计费。
		// best-effort 释放，用 background ctx 避免取消时释放不掉。
		_ = c.ReleaseStaticAddress(context.Background(), region, name)
		return AddressInfo{}, fmt.Errorf("等待 Insert Address 操作完成失败: %w", err)
	}

	info, err := c.GetAddress(ctx, region, name)
	if err != nil {
		return AddressInfo{}, err
	}
	logger.Info("GCP ReserveStaticAddress 完成 name=%s ip=%s", name, info.IP)
	return info, nil
}

// GetAddress 获取指定 region 下某个 Address 的详情
func (c *Client) GetAddress(ctx context.Context, region, name string) (AddressInfo, error) {
	cli, err := c.addresses(ctx)
	if err != nil {
		return AddressInfo{}, fmt.Errorf("构造 Addresses client 失败: %w", err)
	}

	addr, err := cli.Get(ctx, &computepb.GetAddressRequest{
		Project: c.projectID,
		Region:  region,
		Address: name,
	})
	if err != nil {
		return AddressInfo{}, fmt.Errorf("Get Address 失败: %w", err)
	}
	return addressToInfo(addr), nil
}

// ReleaseStaticAddress 释放静态 IP
func (c *Client) ReleaseStaticAddress(ctx context.Context, region, name string) error {
	cli, err := c.addresses(ctx)
	if err != nil {
		return fmt.Errorf("构造 Addresses client 失败: %w", err)
	}

	logger.Info("GCP ReleaseStaticAddress name=%s region=%s", name, region)
	op, err := cli.Delete(ctx, &computepb.DeleteAddressRequest{
		Project: c.projectID,
		Region:  region,
		Address: name,
	})
	if err != nil {
		return fmt.Errorf("Delete Address 失败: %w", err)
	}
	if err := op.Wait(ctx); err != nil {
		return fmt.Errorf("等待 Delete Address 操作完成失败: %w", err)
	}
	logger.Info("GCP ReleaseStaticAddress 完成 name=%s", name)
	return nil
}

// ListAddresses 列出指定 region 下所有 Address
func (c *Client) ListAddresses(ctx context.Context, region string) ([]AddressInfo, error) {
	cli, err := c.addresses(ctx)
	if err != nil {
		return nil, fmt.Errorf("构造 Addresses client 失败: %w", err)
	}

	it := cli.List(ctx, &computepb.ListAddressesRequest{
		Project: c.projectID,
		Region:  region,
	})
	var out []AddressInfo
	for {
		addr, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("List Addresses 失败: %w", err)
		}
		out = append(out, addressToInfo(addr))
	}
	return out, nil
}

// addressToInfo 转换
func addressToInfo(addr *computepb.Address) AddressInfo {
	info := AddressInfo{
		Name:   addr.GetName(),
		IP:     addr.GetAddress(),
		Status: addr.GetStatus(),
		Users:  addr.GetUsers(),
	}
	if r := addr.GetRegion(); r != "" {
		info.Region = path.Base(r)
	}
	if ts := addr.GetCreationTimestamp(); ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			info.CreatedAt = t
		} else if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			info.CreatedAt = t
		} else if t, err := time.Parse("2006-01-02T15:04:05-07:00", ts); err == nil {
			info.CreatedAt = t
		}
	}
	return info
}
