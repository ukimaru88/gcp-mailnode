// cmd/ippool: 端到端模拟 v0.2.15 dirtyIPHolder 机制——批量 reserve + hold 不释放，
// 看 GCP IP 池子翻新效果，验证 asia-northeast1 能否筛到非 34./35. IP。
//
// 流程：
//   1. 第 1 批：并发 reserve 50 个 IP，hold 不释放，看前缀分布
//   2. 第 2 批：再 reserve 50 个（累计 hold 100），看新分布（验证池子翻新效果）
//   3. 第 3 批：再 reserve 50 个（累计 hold 150 ≈ 配额 175 顶），看终态分布
//   4. 总结非 34./35. 命中率，无论结果如何统一 release 全部
//
// 安全：defer 全 release；ctrl-c 信号触发提前 release；最长持有 5 分钟。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"github.com/google/uuid"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/proto"
)

type held struct {
	name string
	ip   string
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: ippool <sa-json>")
		os.Exit(1)
	}
	saBytes, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Println("read SA:", err)
		os.Exit(1)
	}
	var sa struct {
		ProjectID string `json:"project_id"`
	}
	_ = json.Unmarshal(saBytes, &sa)
	fmt.Printf("project=%s region=asia-northeast1\n\n", sa.ProjectID)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	cli, err := compute.NewAddressesRESTClient(ctx, option.WithCredentialsJSON(saBytes))
	if err != nil {
		fmt.Println("client:", err)
		os.Exit(1)
	}
	defer cli.Close()

	region := "asia-northeast3" // 韩国首尔

	var (
		mu       sync.Mutex
		allHeld  []held
		batchCnt = []int{50, 50, 50} // 三批 reserve，累计 hold 150
	)

	// 信号 / defer 兜底 release
	releaseAll := func() {
		mu.Lock()
		toRelease := append([]held(nil), allHeld...)
		mu.Unlock()
		if len(toRelease) == 0 {
			return
		}
		fmt.Printf("\n=== 释放 %d 个 IP ===\n", len(toRelease))
		var rwg sync.WaitGroup
		relCtx, relCancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer relCancel()
		for _, h := range toRelease {
			rwg.Add(1)
			go func(h held) {
				defer rwg.Done()
				op, err := cli.Delete(relCtx, &computepb.DeleteAddressRequest{
					Project: sa.ProjectID, Region: region, Address: h.name,
				})
				if err == nil && op != nil {
					_ = op.Wait(relCtx)
				}
			}(h)
		}
		rwg.Wait()
		fmt.Println("已全部释放")
	}
	defer releaseAll()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\n收到中断信号，释放...")
		releaseAll()
		os.Exit(130)
	}()

	for batchIdx, n := range batchCnt {
		fmt.Printf("=== 批次 %d：再 reserve %d 个（累计 hold %d → %d）===\n",
			batchIdx+1, n, len(allHeld), len(allHeld)+n)
		batchStart := time.Now()
		newHeld := make([]held, n)
		var wg sync.WaitGroup
		sem := make(chan struct{}, 20) // 限并发 20 同 v0.2.15
		for i := 0; i < n; i++ {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int) {
				defer wg.Done()
				defer func() { <-sem }()
				name := "ippool-test-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
				op, err := cli.Insert(ctx, &computepb.InsertAddressRequest{
					Project: sa.ProjectID, Region: region,
					AddressResource: &computepb.Address{
						Name: proto.String(name), AddressType: proto.String("EXTERNAL"),
					},
				})
				if err != nil {
					if strings.Contains(err.Error(), "exceeded") || strings.Contains(err.Error(), "Quota") {
						fmt.Printf("  worker %d: QUOTA_EXCEEDED（配额顶到）\n", i)
					}
					return
				}
				if err := op.Wait(ctx); err != nil {
					return
				}
				addr, err := cli.Get(ctx, &computepb.GetAddressRequest{
					Project: sa.ProjectID, Region: region, Address: name,
				})
				if err == nil {
					newHeld[i] = held{name: name, ip: addr.GetAddress()}
				}
			}(i)
		}
		wg.Wait()

		// 汇总此批前缀
		prefixCnt := map[string]int{}
		nonStandard := []string{}
		batchCount := 0
		for _, h := range newHeld {
			if h.ip == "" {
				continue
			}
			batchCount++
			parts := strings.Split(h.ip, ".")
			p1 := parts[0] + "."
			prefixCnt[p1]++
			if p1 != "34." && p1 != "35." {
				nonStandard = append(nonStandard, h.ip)
			}
		}
		mu.Lock()
		for _, h := range newHeld {
			if h.ip != "" {
				allHeld = append(allHeld, h)
			}
		}
		mu.Unlock()

		fmt.Printf("  本批成功 %d 个，耗时 %.1fs\n", batchCount, time.Since(batchStart).Seconds())
		fmt.Printf("  前缀分布: ")
		for p, c := range prefixCnt {
			fmt.Printf("%s×%d  ", p, c)
		}
		fmt.Println()
		if len(nonStandard) > 0 {
			fmt.Printf("  🎯 非 34./35. (%d 个): %s\n", len(nonStandard), strings.Join(nonStandard, " "))
		} else {
			fmt.Printf("  ⚠️  本批 0 个非 34./35.\n")
		}
		fmt.Println()
		time.Sleep(2 * time.Second) // 给 GCP 池子缓一下
	}

	// 最终汇总
	allPrefix := map[string]int{}
	allNon := []string{}
	for _, h := range allHeld {
		parts := strings.Split(h.ip, ".")
		p1 := parts[0] + "."
		allPrefix[p1]++
		if p1 != "34." && p1 != "35." {
			allNon = append(allNon, h.ip)
		}
	}
	fmt.Println("=== 最终汇总（hold 100% 行为模拟）===")
	fmt.Printf("总 reserve 成功: %d 个\n", len(allHeld))
	fmt.Printf("前缀分布: ")
	for p, c := range allPrefix {
		fmt.Printf("%s×%d  ", p, c)
	}
	fmt.Println()
	fmt.Printf("🎯 非 34./35. 总命中: %d / %d (%.1f%%)\n", len(allNon), len(allHeld), float64(len(allNon))*100/float64(len(allHeld)))
	if len(allNon) > 0 {
		fmt.Printf("具体: %s\n", strings.Join(allNon, " "))
	}
}
