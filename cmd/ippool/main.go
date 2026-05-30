package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"github.com/google/uuid"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/proto"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: ippool <sa-json> <count>")
		os.Exit(1)
	}
	saBytes, _ := os.ReadFile(os.Args[1])
	var sa struct{ ProjectID string `json:"project_id"` }
	_ = json.Unmarshal(saBytes, &sa)

	count := 0
	fmt.Sscanf(os.Args[2], "%d", &count)
	if count == 0 {
		count = 5
	}

	ctx := context.Background()
	cli, err := compute.NewAddressesRESTClient(ctx, option.WithCredentialsJSON(saBytes))
	if err != nil {
		fmt.Println("client:", err)
		return
	}
	defer cli.Close()

	for _, region := range []string{"asia-northeast2", "asia-northeast3"} {
		fmt.Printf("\n=== %s 池子样本（reserve %d 个看前缀分布）===\n", region, count)
		ips := make([]string, count)
		names := make([]string, count)
		var wg sync.WaitGroup
		for i := 0; i < count; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				name := "test-pool-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
				op, err := cli.Insert(ctx, &computepb.InsertAddressRequest{
					Project: sa.ProjectID, Region: region,
					AddressResource: &computepb.Address{
						Name: proto.String(name), AddressType: proto.String("EXTERNAL"),
					},
				})
				if err != nil {
					fmt.Printf("  reserve %d 失败: %v\n", i, err)
					return
				}
				if err := op.Wait(ctx); err != nil {
					fmt.Printf("  wait %d 失败: %v\n", i, err)
					return
				}
				addr, err := cli.Get(ctx, &computepb.GetAddressRequest{
					Project: sa.ProjectID, Region: region, Address: name,
				})
				if err == nil {
					ips[i] = addr.GetAddress()
					names[i] = name
				}
			}(i)
		}
		wg.Wait()

		prefixCnt := map[string]int{}
		for _, ip := range ips {
			if ip == "" {
				continue
			}
			parts := strings.Split(ip, ".")
			if len(parts) >= 2 {
				prefix := parts[0] + "."
				prefixCnt[prefix]++
				fullPrefix := parts[0] + "." + parts[1] + "."
				fmt.Printf("  %-18s  前缀=%s\n", ip, fullPrefix)
				_ = prefix
			}
		}
		fmt.Printf("  汇总: ")
		for p, c := range prefixCnt {
			fmt.Printf("%s×%d  ", p, c)
		}
		fmt.Println()

		// 释放
		for _, name := range names {
			if name == "" {
				continue
			}
			op, _ := cli.Delete(ctx, &computepb.DeleteAddressRequest{
				Project: sa.ProjectID, Region: region, Address: name,
			})
			if op != nil {
				_ = op.Wait(ctx)
			}
		}
	}
}
