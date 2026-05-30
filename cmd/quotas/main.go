package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/option"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: quotas <sa-json>")
		os.Exit(1)
	}
	saBytes, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Println("read SA:", err)
		return
	}
	var sa struct {
		ProjectID   string `json:"project_id"`
		ClientEmail string `json:"client_email"`
	}
	_ = json.Unmarshal(saBytes, &sa)
	fmt.Printf("project=%s sa=%s\n\n", sa.ProjectID, sa.ClientEmail)

	ctx := context.Background()

	// === 1. Project-level quotas（全局）===
	projCli, err := compute.NewProjectsRESTClient(ctx, option.WithCredentialsJSON(saBytes))
	if err != nil {
		fmt.Println("projects client:", err)
		return
	}
	defer projCli.Close()
	proj, err := projCli.Get(ctx, &computepb.GetProjectRequest{Project: sa.ProjectID})
	if err != nil {
		fmt.Println("get project:", err)
		return
	}

	fmt.Println("=== 项目级配额（全局） ===")
	// 只展示与邮件部署相关的关键项
	keyGlobal := map[string]bool{
		"NETWORKS": true, "FIREWALLS": true, "ROUTES": true,
		"GLOBAL_INTERNAL_ADDRESSES": true,
	}
	printQuotas(proj.GetQuotas(), keyGlobal)

	// === 2. Region-level quotas（重点 asia-northeast1）===
	regCli, err := compute.NewRegionsRESTClient(ctx, option.WithCredentialsJSON(saBytes))
	if err != nil {
		fmt.Println("regions client:", err)
		return
	}
	defer regCli.Close()
	for _, regionName := range []string{"asia-northeast1", "asia-northeast2", "asia-northeast3"} {
	region, err := regCli.Get(ctx, &computepb.GetRegionRequest{
		Project: sa.ProjectID, Region: regionName,
	})
	if err != nil {
		fmt.Println("get region:", err)
		continue
	}
	fmt.Printf("\n=== %s（%s）区域级配额 ===\n", regionName, regionLabel(regionName))
	keyRegional := map[string]bool{
		"STATIC_ADDRESSES":          true,
		"IN_USE_ADDRESSES":          true,
		"INSTANCES":                 true,
		"CPUS":                      true,
		"DISKS_TOTAL_GB":            true,
		"SSD_TOTAL_GB":              true,
		"PREEMPTIBLE_CPUS":          true,
		"INTERNAL_ADDRESSES":        true,
		"PUBLIC_ADVERTISED_PREFIXES": true,
		"E2_CPUS":                   true,
	}
	printQuotas(region.GetQuotas(), keyRegional)
	}

	// === 3. 当前已使用的静态 IP 数 ===
	fmt.Println("\n=== asia-northeast1 当前静态 IP 占用 ===")
	addrCli, err := compute.NewAddressesRESTClient(ctx, option.WithCredentialsJSON(saBytes))
	if err != nil {
		fmt.Println("addresses client:", err)
		return
	}
	defer addrCli.Close()
	it := addrCli.List(ctx, &computepb.ListAddressesRequest{
		Project: sa.ProjectID,
		Region:  "asia-northeast1",
	})
	statusCount := map[string]int{}
	total := 0
	for {
		a, err := it.Next()
		if err != nil {
			break
		}
		total++
		statusCount[a.GetStatus()]++
	}
	fmt.Printf("总计：%d 个\n", total)
	keys := make([]string, 0, len(statusCount))
	for k := range statusCount {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  %-12s %d\n", k, statusCount[k])
	}
}

func regionLabel(r string) string {
	switch r {
	case "asia-northeast1":
		return "东京"
	case "asia-northeast2":
		return "大阪"
	case "asia-northeast3":
		return "首尔"
	}
	return r
}

func printQuotas(qs []*computepb.Quota, keep map[string]bool) {
	rows := make([][]string, 0)
	for _, q := range qs {
		metric := q.GetMetric()
		if len(keep) > 0 && !keep[metric] {
			continue
		}
		limit := q.GetLimit()
		usage := q.GetUsage()
		pct := 0.0
		if limit > 0 {
			pct = usage / limit * 100
		}
		flag := "  "
		if pct > 80 {
			flag = "⚠️"
		}
		if limit < 50 && (metric == "STATIC_ADDRESSES" || metric == "IN_USE_ADDRESSES" || metric == "CPUS" || metric == "INSTANCES") {
			flag = "🔴"
		}
		rows = append(rows, []string{
			flag, metric, fmt.Sprintf("%.0f / %.0f", usage, limit), fmt.Sprintf("%.0f%%", pct),
		})
	}
	maxName := 0
	for _, r := range rows {
		if len(r[1]) > maxName {
			maxName = len(r[1])
		}
	}
	for _, r := range rows {
		fmt.Printf("%s  %-*s  %20s  %5s\n",
			r[0], maxName, r[1],
			strings.Repeat(" ", 20-len(r[2]))+r[2], r[3])
	}
}
