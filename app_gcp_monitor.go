package main

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"gcp-mailnode/internal/gcp"
)

const bytesPerGiB = 1024 * 1024 * 1024

type GCPMonitorPricing struct {
	Currency              string  `json:"currency"`
	EgressPerGB           float64 `json:"egress_per_gb"`
	VPSPerHour            float64 `json:"vps_per_hour"`
	StaticIPPerHour       float64 `json:"static_ip_per_hour"`
	UseLastHourProjection bool    `json:"use_last_hour_projection"`
}

type GCPMonitorReport struct {
	CredID                  string                  `json:"cred_id"`
	CredName                string                  `json:"cred_name"`
	ProjectID               string                  `json:"project_id"`
	GeneratedAt             string                  `json:"generated_at"`
	Hours                   int                     `json:"hours"`
	Pricing                 GCPMonitorPricing       `json:"pricing"`
	TotalVPS                int                     `json:"total_vps"`
	RunningVPS              int                     `json:"running_vps"`
	TotalStaticIPs          int                     `json:"total_static_ips"`
	InUseStaticIPs          int                     `json:"in_use_static_ips"`
	ReservedStaticIPs       int                     `json:"reserved_static_ips"`
	SentGB                  float64                 `json:"sent_gb"`
	ReceivedGB              float64                 `json:"received_gb"`
	TotalGB                 float64                 `json:"total_gb"`
	LastHourSentGB          float64                 `json:"last_hour_sent_gb"`
	LastHourReceivedGB      float64                 `json:"last_hour_received_gb"`
	ProjectedSentGB24h      float64                 `json:"projected_sent_gb_24h"`
	VPSCost24h              float64                 `json:"vps_cost_24h"`
	StaticIPCost24h         float64                 `json:"static_ip_cost_24h"`
	TrafficCost24h          float64                 `json:"traffic_cost_24h"`
	ProjectedTrafficCost24h float64                 `json:"projected_traffic_cost_24h"`
	EstimatedCost24h        float64                 `json:"estimated_cost_24h"`
	ProjectedCost24h        float64                 `json:"projected_cost_24h"`
	MetricError             string                  `json:"metric_error"`
	Warnings                []string                `json:"warnings"`
	Instances               []GCPMonitorInstanceDTO `json:"instances"`
	Hourly                  []GCPMonitorHourlyDTO   `json:"hourly"`
}

type GCPMonitorInstanceDTO struct {
	ID                 string  `json:"id"`
	GCPInstanceID      string  `json:"gcp_instance_id"`
	Name               string  `json:"name"`
	Zone               string  `json:"zone"`
	MachineType        string  `json:"machine_type"`
	Status             string  `json:"status"`
	IP                 string  `json:"ip"`
	FQDN               string  `json:"fqdn"`
	SentGB             float64 `json:"sent_gb"`
	ReceivedGB         float64 `json:"received_gb"`
	TotalGB            float64 `json:"total_gb"`
	LastHourSentGB     float64 `json:"last_hour_sent_gb"`
	LastHourReceivedGB float64 `json:"last_hour_received_gb"`
	TrafficCost24h     float64 `json:"traffic_cost_24h"`
	ProjectedCost24h   float64 `json:"projected_cost_24h"`
}

type GCPMonitorHourlyDTO struct {
	EndTime     string  `json:"end_time"`
	SentGB      float64 `json:"sent_gb"`
	ReceivedGB  float64 `json:"received_gb"`
	TotalGB     float64 `json:"total_gb"`
	TrafficCost float64 `json:"traffic_cost"`
}

type gcpMonitorInstanceRow struct {
	id            string
	gcpInstanceID string
	name          string
	zone          string
	machineType   string
	status        string
	ip            string
	fqdn          string
}

// GetGCPMonitorReport 汇总某个 GCP 凭证下 MailNode 资源的 24h 流量和费用估算。
// 精确账单需要 GCP Billing Export；这里用 Cloud Monitoring 流量 + 本地单价做实时估算。
func (a *App) GetGCPMonitorReport(credID string, hours int, pricing GCPMonitorPricing) (GCPMonitorReport, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if hours <= 0 {
		hours = 24
	}
	if hours > 168 {
		hours = 168
	}
	if strings.TrimSpace(pricing.Currency) == "" {
		pricing.Currency = "USD"
	}
	db, err := requireDB()
	if err != nil {
		return GCPMonitorReport{}, err
	}

	var credName, projectID string
	if credID == "" {
		row := db.QueryRowContext(ctx, `SELECT id, name, project_id FROM gcp_credentials WHERE enabled=1 ORDER BY created_at DESC LIMIT 1`)
		if err := row.Scan(&credID, &credName, &projectID); err != nil {
			if err == sql.ErrNoRows {
				return GCPMonitorReport{}, fmt.Errorf("没有可用的 GCP 凭证")
			}
			return GCPMonitorReport{}, err
		}
	} else {
		row := db.QueryRowContext(ctx, `SELECT name, project_id FROM gcp_credentials WHERE id=?`, credID)
		if err := row.Scan(&credName, &projectID); err != nil {
			return GCPMonitorReport{}, err
		}
	}

	report := GCPMonitorReport{
		CredID:      credID,
		CredName:    credName,
		ProjectID:   projectID,
		GeneratedAt: time.Now().Format(time.RFC3339),
		Hours:       hours,
		Pricing:     pricing,
		Warnings: []string{
			"费用为实时估算：流量来自 Cloud Monitoring，本页单价可修改；精确账单需启用 GCP Billing Export。",
		},
	}

	instances, err := loadMonitorInstances(ctx, db, credID)
	if err != nil {
		return GCPMonitorReport{}, err
	}
	report.TotalVPS = len(instances)
	for _, inst := range instances {
		if strings.EqualFold(inst.status, "running") {
			report.RunningVPS++
		}
	}
	if report.RunningVPS == 0 && len(instances) > 0 {
		report.Warnings = append(report.Warnings, "本地资源清单里没有 running 状态 VPS；固定 VPS 成本按 0 估算。")
	}

	report.TotalStaticIPs, report.InUseStaticIPs, report.ReservedStaticIPs, err = loadStaticIPCounts(ctx, db, credID)
	if err != nil {
		return GCPMonitorReport{}, err
	}

	traffic := map[string]gcp.InstanceNetworkTraffic{}
	cli, err := loadGCPClientForApp(ctx, credID)
	if err != nil {
		return GCPMonitorReport{}, err
	}
	defer cli.Close()
	if projectID == "" {
		report.ProjectID = cli.ProjectID()
	}
	if metricTraffic, err := cli.ListNetworkTraffic(ctx, hours); err != nil {
		report.MetricError = err.Error()
		report.Warnings = append(report.Warnings, "Cloud Monitoring 流量读取失败：请确认已启用 Monitoring API，且凭证有 monitoring.timeSeries.list 权限。")
	} else {
		traffic = metricTraffic
	}

	hourlyTotals := map[string]*GCPMonitorHourlyDTO{}
	for _, inst := range instances {
		dto := GCPMonitorInstanceDTO{
			ID:            inst.id,
			GCPInstanceID: inst.gcpInstanceID,
			Name:          inst.name,
			Zone:          inst.zone,
			MachineType:   inst.machineType,
			Status:        inst.status,
			IP:            inst.ip,
			FQDN:          inst.fqdn,
		}
		if t, ok := traffic[inst.gcpInstanceID]; ok {
			dto.SentGB = bytesToGB(t.SentBytes)
			dto.ReceivedGB = bytesToGB(t.ReceivedBytes)
			dto.TotalGB = dto.SentGB + dto.ReceivedGB
			dto.LastHourSentGB = bytesToGB(t.LastHourSentBytes)
			dto.LastHourReceivedGB = bytesToGB(t.LastHourReceivedBytes)
			dto.TrafficCost24h = dto.SentGB * pricing.EgressPerGB
			dto.ProjectedCost24h = dto.LastHourSentGB * 24 * pricing.EgressPerGB
			report.SentGB += dto.SentGB
			report.ReceivedGB += dto.ReceivedGB
			report.LastHourSentGB += dto.LastHourSentGB
			report.LastHourReceivedGB += dto.LastHourReceivedGB
			for _, h := range t.Hourly {
				total := hourlyTotals[h.EndTime]
				if total == nil {
					total = &GCPMonitorHourlyDTO{EndTime: h.EndTime}
					hourlyTotals[h.EndTime] = total
				}
				total.SentGB += bytesToGB(h.SentBytes)
				total.ReceivedGB += bytesToGB(h.ReceivedBytes)
			}
		} else if inst.gcpInstanceID == "" {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s 缺少 GCP instance_id，无法关联 Monitoring 流量。", inst.name))
		}
		report.Instances = append(report.Instances, dto)
	}

	for _, h := range hourlyTotals {
		h.TotalGB = h.SentGB + h.ReceivedGB
		h.TrafficCost = h.SentGB * pricing.EgressPerGB
		report.Hourly = append(report.Hourly, *h)
	}
	sort.Slice(report.Hourly, func(i, j int) bool { return report.Hourly[i].EndTime < report.Hourly[j].EndTime })
	sort.Slice(report.Instances, func(i, j int) bool { return report.Instances[i].SentGB > report.Instances[j].SentGB })

	report.TotalGB = report.SentGB + report.ReceivedGB
	report.ProjectedSentGB24h = report.LastHourSentGB * 24
	if !pricing.UseLastHourProjection && hours > 0 {
		report.ProjectedSentGB24h = report.SentGB / float64(hours) * 24
	}
	report.VPSCost24h = float64(report.RunningVPS) * pricing.VPSPerHour * 24
	report.StaticIPCost24h = float64(report.TotalStaticIPs) * pricing.StaticIPPerHour * 24
	report.TrafficCost24h = report.SentGB * pricing.EgressPerGB
	report.ProjectedTrafficCost24h = report.ProjectedSentGB24h * pricing.EgressPerGB
	report.EstimatedCost24h = report.VPSCost24h + report.StaticIPCost24h + report.TrafficCost24h
	report.ProjectedCost24h = report.VPSCost24h + report.StaticIPCost24h + report.ProjectedTrafficCost24h

	return report, nil
}

func loadMonitorInstances(ctx context.Context, db *sql.DB, credID string) ([]gcpMonitorInstanceRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, COALESCE(gcp_instance_id,''), name, zone, machine_type, status, ip, fqdn
		FROM vps_instances
		WHERE gcp_cred_id=? AND status <> 'deleted'
		ORDER BY created_at DESC`, credID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []gcpMonitorInstanceRow{}
	for rows.Next() {
		var r gcpMonitorInstanceRow
		if err := rows.Scan(&r.id, &r.gcpInstanceID, &r.name, &r.zone, &r.machineType, &r.status, &r.ip, &r.fqdn); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func loadStaticIPCounts(ctx context.Context, db *sql.DB, credID string) (total, inUse, reserved int, err error) {
	rows, err := db.QueryContext(ctx, `SELECT status, COUNT(*) FROM static_ips
		WHERE gcp_cred_id=? AND status <> 'released'
		GROUP BY status`, credID)
	if err != nil {
		return 0, 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return 0, 0, 0, err
		}
		total += n
		switch strings.ToLower(status) {
		case "in_use":
			inUse += n
		case "reserved":
			reserved += n
		}
	}
	return total, inUse, reserved, rows.Err()
}

func bytesToGB(v int64) float64 {
	return float64(v) / bytesPerGiB
}
