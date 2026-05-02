package main

import (
	"context"
	"fmt"

	"gcp-mailnode/internal/dnsbl"
)

// BlackSegmentDTO 黑段 DTO
type BlackSegmentDTO struct {
	ID   int64  `json:"id"`
	CIDR string `json:"cidr"`
	Note string `json:"note"`
}

// ListBlackSegments 列出所有黑段
func (a *App) ListBlackSegments() ([]BlackSegmentDTO, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	segs, err := dnsbl.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]BlackSegmentDTO, 0, len(segs))
	for _, s := range segs {
		out = append(out, BlackSegmentDTO{ID: s.ID, CIDR: s.CIDR, Note: s.Note})
	}
	return out, nil
}

// AddBlackSegment 添加单条
func (a *App) AddBlackSegment(cidr, note string) error {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return dnsbl.Add(ctx, cidr, note)
}

// RemoveBlackSegment 删除
func (a *App) RemoveBlackSegment(id int64) error {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return dnsbl.Remove(ctx, id)
}

// ImportBlackSegmentsText 批量导入文本（每行一个 CIDR 或 CIDR,备注）
func (a *App) ImportBlackSegmentsText(text string) (map[string]interface{}, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	imported, duplicates, parseErrors, err := dnsbl.ImportText(ctx, text)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"imported":     imported,
		"duplicates":   duplicates,
		"parse_errors": parseErrors,
	}, nil
}

// CheckIPBlackSegment 检测 IP 是否命中黑段
func (a *App) CheckIPBlackSegment(ip string) (map[string]string, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	cidr, note, err := dnsbl.ContainsIP(ctx, ip)
	if err != nil {
		return nil, fmt.Errorf("检查失败: %w", err)
	}
	return map[string]string{
		"cidr": cidr,
		"note": note,
	}, nil
}
