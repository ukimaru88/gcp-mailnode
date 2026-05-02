package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"gcp-mailnode/internal/logger"
	"gcp-mailnode/internal/store"
)

// PersonaExtraHeader 额外头
type PersonaExtraHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// PersonaDTO Persona DTO
type PersonaDTO struct {
	ID               string               `json:"id"`
	Name             string               `json:"name"`
	Description      string               `json:"description"`
	ReceivedTemplate string               `json:"received_template"`
	UserAgent        string               `json:"user_agent"`
	XMailer          string               `json:"x_mailer"`
	ExtraHeaders     []PersonaExtraHeader `json:"extra_headers"`
	IsPreset         bool                 `json:"is_preset"`
	CreatedAt        time.Time            `json:"created_at"`
}

// ListPersonas
func (a *App) ListPersonas() ([]PersonaDTO, error) {
	db, err := requireDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, name, description, received_template, user_agent, x_mailer, extra_headers_json, is_preset, created_at FROM personas ORDER BY is_preset DESC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PersonaDTO{}
	for rows.Next() {
		var (
			p        PersonaDTO
			extraRaw string
			isPreset int
		)
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.ReceivedTemplate, &p.UserAgent, &p.XMailer, &extraRaw, &isPreset, &p.CreatedAt); err != nil {
			return nil, err
		}
		p.IsPreset = isPreset == 1
		if extraRaw != "" {
			_ = json.Unmarshal([]byte(extraRaw), &p.ExtraHeaders)
		}
		if p.ExtraHeaders == nil {
			p.ExtraHeaders = []PersonaExtraHeader{}
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// SavePersona 新增或更新（id 空=新增）
func (a *App) SavePersona(p PersonaDTO) (string, error) {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return "", fmt.Errorf("名称不能为空")
	}
	if strings.TrimSpace(p.ReceivedTemplate) == "" {
		return "", fmt.Errorf("Received 模板不能为空")
	}
	if p.ExtraHeaders == nil {
		p.ExtraHeaders = []PersonaExtraHeader{}
	}
	extraJSON, _ := json.Marshal(p.ExtraHeaders)
	db, err := requireDB()
	if err != nil {
		return "", err
	}
	if p.ID == "" {
		p.ID = uuid.NewString()
		if _, err := db.Exec(
			`INSERT INTO personas (id, name, description, received_template, user_agent, x_mailer, extra_headers_json, is_preset) VALUES (?,?,?,?,?,?,?,0)`,
			p.ID, p.Name, p.Description, p.ReceivedTemplate, p.UserAgent, p.XMailer, string(extraJSON),
		); err != nil {
			return "", err
		}
		return p.ID, nil
	}
	if _, err := db.Exec(
		`UPDATE personas SET name=?, description=?, received_template=?, user_agent=?, x_mailer=?, extra_headers_json=? WHERE id=? AND is_preset=0`,
		p.Name, p.Description, p.ReceivedTemplate, p.UserAgent, p.XMailer, string(extraJSON), p.ID,
	); err != nil {
		return "", err
	}
	return p.ID, nil
}

// DeletePersona 只能删非预设
func (a *App) DeletePersona(id string) error {
	db, err := requireDB()
	if err != nil {
		return err
	}
	_, err = db.Exec(`DELETE FROM personas WHERE id=? AND is_preset=0`, id)
	return err
}

// GetPersona 根据 id 读一条，orchestrator 部署时用
func GetPersona(id string) (*PersonaDTO, error) {
	db, err := requireDB()
	if err != nil {
		return nil, err
	}
	var (
		p        PersonaDTO
		extraRaw string
		isPreset int
	)
	row := db.QueryRow(`SELECT id, name, description, received_template, user_agent, x_mailer, extra_headers_json, is_preset, created_at FROM personas WHERE id=?`, id)
	if err := row.Scan(&p.ID, &p.Name, &p.Description, &p.ReceivedTemplate, &p.UserAgent, &p.XMailer, &extraRaw, &isPreset, &p.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("persona 不存在: %s", id)
		}
		return nil, err
	}
	p.IsPreset = isPreset == 1
	if extraRaw != "" {
		_ = json.Unmarshal([]byte(extraRaw), &p.ExtraHeaders)
	}
	if p.ExtraHeaders == nil {
		p.ExtraHeaders = []PersonaExtraHeader{}
	}
	return &p, nil
}

// ensurePresetPersonas 插入 8 个内置 persona
func ensurePresetPersonas() error {
	db := store.DB()
	if db == nil {
		return fmt.Errorf("数据库未就绪")
	}

	presets := []PersonaDTO{
		{
			Name:        "Gmail 网页用户",
			Description: "Gmail 网页 Webmail 发件，经 google 三跳中继",
			ReceivedTemplate: `from mail-sor-f41.google.com (mail-sor-f41.google.com. [209.85.220.41]) by smtp-relay.gmail.com with ESMTPS id {message_id}; {timestamp}
from smtp-relay.gmail.com ([74.125.82.170]) by {fqdn} with ESMTPSA id {message_id}; {timestamp}`,
			UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
			XMailer:   "",
		},
		{
			Name:        "iPhone Mail 用户",
			Description: "iPhone iOS 18 Mail 发件，经 iCloud 中继",
			ReceivedTemplate: `from pv50p00im-ztbu10072501.me.com ([17.58.63.171]) by mx-aol-mail.iCloud.com with ESMTP id {message_id}; {timestamp}
from relay.icloud.com ([17.57.154.22]) by {fqdn} with ESMTPSA id {message_id}; {timestamp}`,
			UserAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15",
			XMailer:   "iPhone Mail 18G80",
		},
		{
			Name:             "Outlook 桌面用户",
			Description:      "Windows Outlook 桌面客户端，经 Microsoft Exchange Online 中继",
			ReceivedTemplate: `from NAM10-BN7-obe.outbound.protection.outlook.com (mail-bn7nam10on2041.outbound.protection.outlook.com [40.107.212.41]) by {fqdn} with ESMTPSA id {message_id}; {timestamp}`,
			UserAgent:        "Microsoft Outlook 16.0",
			XMailer:          "Microsoft Outlook 16.0",
		},
		{
			Name:             "Thunderbird 桌面用户",
			Description:      "Thunderbird 桌面客户端，经用户本地 ISP SMTP 中继",
			ReceivedTemplate: `from [192.168.1.10] (localhost.localdomain [127.0.0.1]) by {fqdn} with ESMTPSA id {message_id}; {timestamp}`,
			UserAgent:        "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:115.0) Gecko/20100101 Thunderbird/115.11.0",
			XMailer:          "Mozilla Thunderbird 115.11.0",
		},
		{
			Name:             "yahoo.co.jp 用户",
			Description:      "Yahoo Japan Webmail 发件",
			ReceivedTemplate: `from smtp-webmail-m3.yahoo.co.jp ([183.79.102.70]) by {fqdn} with ESMTPSA id {message_id}; {timestamp}`,
			UserAgent:        "YahooMailWebService/0.8.111",
			XMailer:          "",
		},
		{
			Name:             "NTT docomo 手机",
			Description:      "NTT docomo 手机邮箱发件",
			ReceivedTemplate: `from mfsmax.docomo.ne.jp ([210.131.170.10]) by {fqdn} with ESMTPSA id {message_id}; {timestamp}`,
			UserAgent:        "",
			XMailer:          "",
		},
		{
			Name:             "au by KDDI 手机",
			Description:      "au by KDDI 手机邮箱发件",
			ReceivedTemplate: `from ms3.auone-net.jp ([106.185.51.37]) by {fqdn} with ESMTPSA id {message_id}; {timestamp}`,
			UserAgent:        "",
			XMailer:          "",
		},
		{
			Name:             "SoftBank 手机",
			Description:      "SoftBank 手机邮箱发件",
			ReceivedTemplate: `from relay.softbank.ne.jp ([202.253.96.177]) by {fqdn} with ESMTPSA id {message_id}; {timestamp}`,
			UserAgent:        "",
			XMailer:          "",
		},
	}

	for _, p := range presets {
		var existing string
		row := db.QueryRow(`SELECT id FROM personas WHERE is_preset=1 AND name=?`, p.Name)
		if err := row.Scan(&existing); err == nil && existing != "" {
			continue
		}
		p.ID = uuid.NewString()
		if p.ExtraHeaders == nil {
			p.ExtraHeaders = []PersonaExtraHeader{}
		}
		extraJSON, _ := json.Marshal(p.ExtraHeaders)
		if _, err := db.Exec(
			`INSERT INTO personas (id, name, description, received_template, user_agent, x_mailer, extra_headers_json, is_preset) VALUES (?,?,?,?,?,?,?,1)`,
			p.ID, p.Name, p.Description, p.ReceivedTemplate, p.UserAgent, p.XMailer, string(extraJSON),
		); err != nil {
			return err
		}
		logger.Info("初始化预设 Persona: %s", p.Name)
	}
	return nil
}
