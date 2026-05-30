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
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Regions        []string `json:"regions"`
	AutoSpread     bool     `json:"auto_spread"`
	MachineType    string   `json:"machine_type"`
	ImageFamily    string   `json:"image_family"`
	ImageProject   string   `json:"image_project"`
	DiskSizeGB     int64    `json:"disk_size_gb"`
	DiskType       string   `json:"disk_type"` // pd-standard / pd-balanced / pd-ssd
	Tags           []string `json:"tags"`
	MetadataScript string   `json:"metadata_script"`
	RootPassword   string   `json:"root_password"`
	DeployType     string   `json:"deploy_type"` // kumomta（纯发信）/ mailcow（收发一体）
	// v0.1.54
	ProvisioningModel string    `json:"provisioning_model"` // STANDARD（默认）/ SPOT（73% off，可被抢占）
	NICCount          int       `json:"nic_count"`          // 1（默认）/ 8（仅 n1-standard-8 + Batch 2 多 NIC 启用）
	IsPreset          bool      `json:"is_preset"`
	CreatedAt         time.Time `json:"created_at"`
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
	// v0.1.74：visible=0 隐藏老预设，UI 下拉只列可见模板；老 VPS 资源页通过 GetVPSTemplate(id) 仍能查到隐藏模板信息
	rows, err := db.Query(
		`SELECT id, name, regions_json, auto_spread, machine_type, image_family, image_project, disk_size_gb,
		        COALESCE(disk_type,'pd-balanced'), tags_json, metadata_script, root_password,
		        COALESCE(deploy_type,'kumomta'),
		        COALESCE(provisioning_model,'STANDARD'), COALESCE(nic_count,1),
		        is_preset, created_at
		   FROM vps_templates
		   WHERE COALESCE(visible,1)=1
		   ORDER BY is_preset DESC, created_at DESC`)
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

	// v0.1.74：模板大瘦身——只保留一个推荐预设（n1-standard-8 + Spot + 8 NIC）。
	// 老的 5 个预设保留代码 + DB 行但 visible=0 隐藏，UI 不再列出，老 VPS 资源页查询模板信息不受影响。
	hideOldNames := []string{
		"日本主力发信机 KumoMTA（5万/小时）",
		"日本高性能发信机 KumoMTA（10万/小时）",
		"日本 Spot 入门 KumoMTA（最便宜，73% off）",
		"日本收发一体 mailcow（8GB，IMAP/SMTP）",
		"日本收发一体 mailcow Pro（16GB）",
		// v0.1.57 老名字：旧版 Spot 8 IP 走 n2-custom，被 v0.1.74 的 n1-standard-8 替代
		"日本 Spot 8 核 16G × 8 IP KumoMTA（短期批量）",
		// v0.1.76 隐藏 v0.1.74 时期的"推荐"措辞老名字（实测 PTR 87.5% 残缺，新版重命名加警告）
		"日本 Spot 8 核 30G × 8 IP KumoMTA（推荐，n1 73% off）",
		// v0.1.79 隐藏 v0.1.76 的 Spot 预设（用户反馈刚搭好几分钟就被抢占，太频繁，改 STANDARD 长期跑）
		"日本 Spot e2-small × 单 NIC × 完美 PTR（推荐量产 48 台）",
		"日本 Spot 8 核 30G × 8 IP KumoMTA（短期暖机，PTR 仅 nic0 真生效）",
		// v0.2.8：用户决定预设统一只保留 e2-micro，软隐藏旧的 e2-micro(极省旧名)/e2-small/e2-medium 三个预设
		"日本 e2-micro × 单 NIC × 完美 PTR（极省，$8.4/月/台，1GB RAM）",
		"日本 e2-small × 单 NIC × 完美 PTR（推荐稳定，$16.8/月/台）",
		"日本 e2-medium × 单 NIC × 完美 PTR（从容，$33.6/月/台，量大用）",
	}
	for _, n := range hideOldNames {
		_, _ = db.Exec(`UPDATE vps_templates SET visible=0 WHERE is_preset=1 AND name=?`, n)
	}

	// v0.1.79：用户反馈 Spot 抢占太频繁（刚搭好几分钟就被抢占），全量改 STANDARD（不抢占）。
	//
	// 单 NIC × 完美 PTR 是主推架构（v0.1.76 起）。STANDARD 价格虽是 Spot 的 6 倍，但稳定性碾压：
	// 不抢占、不重装、不丢 IP 信誉。
	//
	// v0.2.8：用户决定预设统一只保留 e2-micro（删掉 e2-small/e2-medium 预设，旧名已在上方
	// hideOldNames 软隐藏）。仍可在 UI 手动新建其它机型的模板，新建模板的默认机型也是 e2-micro。
	//   - e2-micro  0.25c/1GB  $8.4/月  默认（KumoMTA 单 NIC 够用；同装 caddy/unsub 注意 RAM）
	presets := []VPSTemplateDTO{
		{
			Name:              "日本 e2-micro × 单 NIC × 完美 PTR（默认，$8.4/月/台）",
			Regions:           []string{"asia-northeast1"},
			MachineType:       "e2-micro",
			DiskSizeGB:        10,
			DiskType:          "pd-balanced",
			DeployType:        "kumomta",
			ProvisioningModel: "STANDARD",
			NICCount:          1,
		},
	}
	for _, p := range presets {
		if p.ProvisioningModel == "" {
			p.ProvisioningModel = "STANDARD"
		}
		if p.NICCount <= 0 {
			p.NICCount = 1
		}
		var existingID string
		row := db.QueryRow(`SELECT id FROM vps_templates WHERE is_preset=1 AND name=?`, p.Name)
		if err := row.Scan(&existingID); err == nil && existingID != "" {
			// v0.1.55: v0.1.54 的预设把 provisioning_model/nic_count 写丢了，启动时同步回来
			_, _ = db.Exec(`UPDATE vps_templates SET provisioning_model=?, nic_count=? WHERE id=? AND is_preset=1`,
				p.ProvisioningModel, p.NICCount, existingID)
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
			`INSERT INTO vps_templates (id, name, regions_json, auto_spread, machine_type, image_family, image_project, disk_size_gb, disk_type, tags_json, metadata_script, root_password, deploy_type, is_preset, provisioning_model, nic_count)
             VALUES (?,?,?,1,?,?,?,?,?,?,'','',?,1,?,?)`,
			p.ID, p.Name, string(regionsJSON), p.MachineType, p.ImageFamily, p.ImageProject, p.DiskSizeGB, p.DiskType, string(tagsJSON), p.DeployType, p.ProvisioningModel, p.NICCount); err != nil {
			return err
		}
		logger.Info("初始化预设模板: %s (%s/%s, %dGB %s, type=%s, prov=%s, nic=%d)", p.Name, p.MachineType, p.DiskType, p.DiskSizeGB, p.DiskType, p.DeployType, p.ProvisioningModel, p.NICCount)
	}
	return nil
}
