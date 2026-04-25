package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"gcp-mailnode/internal/logger"
	"gcp-mailnode/internal/store"
)

// VPSTemplateDTO 模板 DTO
type VPSTemplateDTO struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Regions        []string  `json:"regions"`
	AutoSpread     bool      `json:"auto_spread"`
	MachineType    string    `json:"machine_type"`
	ImageFamily    string    `json:"image_family"`
	ImageProject   string    `json:"image_project"`
	DiskSizeGB     int64     `json:"disk_size_gb"`
	DiskType       string    `json:"disk_type"` // pd-standard / pd-balanced / pd-ssd
	Tags           []string  `json:"tags"`
	MetadataScript string    `json:"metadata_script"`
	RootPassword   string    `json:"root_password"`
	DeployType     string    `json:"deploy_type"` // kumomta（纯发信）/ mailcow（收发一体）
	// v0.1.54
	ProvisioningModel string `json:"provisioning_model"` // STANDARD（默认）/ SPOT（73% off，可被抢占）
	NICCount          int    `json:"nic_count"`          // 1（默认）/ 8（仅 n1-standard-8 + Batch 2 多 NIC 启用）
	IsPreset       bool      `json:"is_preset"`
	CreatedAt      time.Time `json:"created_at"`
}

// SaveVPSTemplate 保存（id 为空则新建，否则更新）
func (a *App) SaveVPSTemplate(t VPSTemplateDTO) (string, error) {
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" {
		return "", fmt.Errorf("名称不能为空")
	}
	regionsJSON, _ := json.Marshal(t.Regions)
	tagsJSON, _ := json.Marshal(t.Tags)
	autoSpread := 0
	if t.AutoSpread {
		autoSpread = 1
	}
	isPreset := 0
	if t.IsPreset {
		isPreset = 1
	}
	if t.MachineType == "" {
		t.MachineType = "e2-micro"
	}
	if t.ImageFamily == "" {
		t.ImageFamily = "ubuntu-2204-lts"
	}
	if t.ImageProject == "" {
		t.ImageProject = "ubuntu-os-cloud"
	}
	if t.DiskSizeGB <= 0 {
		t.DiskSizeGB = 10
	}
	if t.DiskType == "" {
		t.DiskType = "pd-balanced"
	}
	if t.DeployType != "mailcow" {
		t.DeployType = "kumomta"
	}
	if t.ProvisioningModel != "SPOT" {
		t.ProvisioningModel = "STANDARD"
	}
	if t.NICCount != 8 {
		t.NICCount = 1
	}
	db, err := requireDB()
	if err != nil {
		return "", err
	}
	if t.ID == "" {
		t.ID = uuid.NewString()
		if _, err := db.Exec(
			`INSERT INTO vps_templates (id, name, regions_json, auto_spread, machine_type, image_family, image_project, disk_size_gb, disk_type, tags_json, metadata_script, root_password, deploy_type, provisioning_model, nic_count, is_preset)
             VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			t.ID, t.Name, string(regionsJSON), autoSpread, t.MachineType, t.ImageFamily, t.ImageProject, t.DiskSizeGB, t.DiskType, string(tagsJSON), t.MetadataScript, t.RootPassword, t.DeployType, t.ProvisioningModel, t.NICCount, isPreset); err != nil {
			return "", err
		}
		return t.ID, nil
	}
	if _, err := db.Exec(
		`UPDATE vps_templates SET name=?, regions_json=?, auto_spread=?, machine_type=?, image_family=?, image_project=?, disk_size_gb=?, disk_type=?, tags_json=?, metadata_script=?, root_password=?, deploy_type=?, provisioning_model=?, nic_count=?, is_preset=? WHERE id=?`,
		t.Name, string(regionsJSON), autoSpread, t.MachineType, t.ImageFamily, t.ImageProject, t.DiskSizeGB, t.DiskType, string(tagsJSON), t.MetadataScript, t.RootPassword, t.DeployType, t.ProvisioningModel, t.NICCount, isPreset, t.ID); err != nil {
		return "", err
	}
	return t.ID, nil
}

// ListVPSTemplates 列出全部模板
func (a *App) ListVPSTemplates() ([]VPSTemplateDTO, error) {
	db, err := requireDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(
		`SELECT id, name, regions_json, auto_spread, machine_type, image_family, image_project, disk_size_gb,
		        COALESCE(disk_type,'pd-balanced'), tags_json, metadata_script, root_password,
		        COALESCE(deploy_type,'kumomta'),
		        COALESCE(provisioning_model,'STANDARD'), COALESCE(nic_count,1),
		        is_preset, created_at
		   FROM vps_templates ORDER BY is_preset DESC, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VPSTemplateDTO{}
	for rows.Next() {
		var (
			t                     VPSTemplateDTO
			regionsJSON, tagsJSON string
			autoSpread, isPreset  int
		)
		if err := rows.Scan(&t.ID, &t.Name, &regionsJSON, &autoSpread, &t.MachineType, &t.ImageFamily, &t.ImageProject, &t.DiskSizeGB, &t.DiskType, &tagsJSON, &t.MetadataScript, &t.RootPassword, &t.DeployType, &t.ProvisioningModel, &t.NICCount, &isPreset, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.AutoSpread = autoSpread == 1
		t.IsPreset = isPreset == 1
		if regionsJSON != "" {
			_ = json.Unmarshal([]byte(regionsJSON), &t.Regions)
		}
		if tagsJSON != "" {
			_ = json.Unmarshal([]byte(tagsJSON), &t.Tags)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteVPSTemplate 删除
func (a *App) DeleteVPSTemplate(id string) error {
	db, err := requireDB()
	if err != nil {
		return err
	}
	_, err = db.Exec(`DELETE FROM vps_templates WHERE id=?`, id)
	return err
}

// ensurePresetTemplates 初始化预设模板（已存在则跳过）
func ensurePresetTemplates() error {
	db := store.DB()
	if db == nil {
		return fmt.Errorf("数据库未就绪")
	}
	// 清理老版本的预设（已被新预设替代）
	legacyNames := []string{
		"日本便宜机", "日本高性能", "韩国便宜机", "新加坡便宜机",
		"日本测试机（不发信）", "日本入门发信机（万级/小时）",
		"韩国主力发信机（5万/小时）", "新加坡主力发信机（5万/小时）",
		// v0.1.27 更名：增加 KumoMTA 后缀区分于新的 mailcow 模板
		"日本主力发信机（5万/小时）", "日本高性能发信机（10万/小时）",
	}
	for _, n := range legacyNames {
		_, _ = db.Exec(`DELETE FROM vps_templates WHERE is_preset=1 AND name=?`, n)
	}

	// 预设：KumoMTA 2 档（纯发信）+ mailcow 2 档（收发一体）+ Spot 短期省钱档
	presets := []VPSTemplateDTO{
		{
			Name:              "日本主力发信机 KumoMTA（5万/小时）",
			Regions:           []string{"asia-northeast1", "asia-northeast2"},
			MachineType:       "e2-standard-2",
			DiskSizeGB:        20,
			DiskType:          "pd-ssd",
			DeployType:        "kumomta",
			ProvisioningModel: "STANDARD",
			NICCount:          1,
		},
		{
			Name:              "日本高性能发信机 KumoMTA（10万/小时）",
			Regions:           []string{"asia-northeast1", "asia-northeast2"},
			MachineType:       "e2-standard-4",
			DiskSizeGB:        20,
			DiskType:          "pd-ssd",
			DeployType:        "kumomta",
			ProvisioningModel: "STANDARD",
			NICCount:          1,
		},
		// v0.1.54：3 天即抛业务用 Spot——东京 n1-standard-8 Spot 73% off
		// 抢占时 30 秒 SIGTERM 预通知 → DELETE 实例（业务模式短期，DELETE 比 STOP 省 IP 持有费）
		{
			Name:              "日本 Spot 8 核 KumoMTA（短期批量，73% off）",
			Regions:           []string{"asia-northeast1"},
			MachineType:       "n1-standard-8",
			DiskSizeGB:        30,
			DiskType:           "pd-balanced",
			DeployType:        "kumomta",
			ProvisioningModel: "SPOT",
			NICCount:          1, // Batch 1 暂不开多 NIC，下个版本启用
		},
		{
			Name:              "日本 Spot 入门 KumoMTA（最便宜，73% off）",
			Regions:           []string{"asia-northeast1"},
			MachineType:       "n1-standard-1",
			DiskSizeGB:        20,
			DiskType:          "pd-balanced",
			DeployType:        "kumomta",
			ProvisioningModel: "SPOT",
			NICCount:          1,
		},
		{
			Name:              "日本收发一体 mailcow（8GB，IMAP/SMTP）",
			Regions:           []string{"asia-northeast1", "asia-northeast2"},
			MachineType:       "e2-standard-2",
			DiskSizeGB:        40, // mailcow + docker + 邮箱存储
			DiskType:          "pd-ssd",
			DeployType:        "mailcow",
			ProvisioningModel: "STANDARD",
			NICCount:          1,
		},
		{
			Name:              "日本收发一体 mailcow Pro（16GB）",
			Regions:           []string{"asia-northeast1", "asia-northeast2"},
			MachineType:       "e2-standard-4",
			DiskSizeGB:        80,
			DiskType:          "pd-ssd",
			DeployType:        "mailcow",
			ProvisioningModel: "STANDARD",
			NICCount:          1,
		},
	}
	for _, p := range presets {
		var existingID string
		row := db.QueryRow(`SELECT id FROM vps_templates WHERE is_preset=1 AND name=?`, p.Name)
		if err := row.Scan(&existingID); err == nil && existingID != "" {
			continue
		}
		p.ID = uuid.NewString()
		p.ImageFamily = "ubuntu-2204-lts"
		p.ImageProject = "ubuntu-os-cloud"
		p.AutoSpread = true
		p.IsPreset = true
		if p.DeployType == "" {
			p.DeployType = "kumomta"
		}
		regionsJSON, _ := json.Marshal(p.Regions)
		tagsJSON, _ := json.Marshal([]string{})
		if _, err := db.Exec(
			`INSERT INTO vps_templates (id, name, regions_json, auto_spread, machine_type, image_family, image_project, disk_size_gb, disk_type, tags_json, metadata_script, root_password, deploy_type, is_preset)
             VALUES (?,?,?,1,?,?,?,?,?,?,'','',?,1)`,
			p.ID, p.Name, string(regionsJSON), p.MachineType, p.ImageFamily, p.ImageProject, p.DiskSizeGB, p.DiskType, string(tagsJSON), p.DeployType); err != nil {
			return err
		}
		logger.Info("初始化预设模板: %s (%s/%s, %dGB %s, type=%s)", p.Name, p.MachineType, p.DiskType, p.DiskSizeGB, p.DiskType, p.DeployType)
	}
	return nil
}
