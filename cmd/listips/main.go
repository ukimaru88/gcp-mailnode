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
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

func main() {
	saBytes, _ := os.ReadFile(os.Args[1])
	var sa struct{ ProjectID string `json:"project_id"` }
	_ = json.Unmarshal(saBytes, &sa)

	ctx := context.Background()
	cli, _ := compute.NewAddressesRESTClient(ctx, option.WithCredentialsJSON(saBytes))
	defer cli.Close()
	instCli, _ := compute.NewInstancesRESTClient(ctx, option.WithCredentialsJSON(saBytes))
	defer instCli.Close()

	for _, region := range []string{"asia-northeast1", "asia-northeast2"} {
		fmt.Printf("\n=== %s 静态 IP ===\n", region)
		it := cli.List(ctx, &computepb.ListAddressesRequest{Project: sa.ProjectID, Region: region})
		var ips []string
		prefixCnt := map[string]int{}
		for {
			a, err := it.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				break
			}
			ip := a.GetAddress()
			ips = append(ips, fmt.Sprintf("  %-18s  %-10s  %s", ip, a.GetStatus(), strings.Join(a.GetUsers(), ", ")))
			parts := strings.Split(ip, ".")
			if len(parts) >= 2 {
				prefixCnt[parts[0]+"."]++
			}
		}
		sort.Strings(ips)
		for _, line := range ips {
			fmt.Println(line)
		}
		fmt.Printf("  汇总前缀: ")
		for p, c := range prefixCnt {
			fmt.Printf("%s×%d  ", p, c)
		}
		fmt.Println()
	}

	// 列实例
	fmt.Println("\n=== asia-northeast1 实例 ===")
	itI := instCli.AggregatedList(ctx, &computepb.AggregatedListInstancesRequest{Project: sa.ProjectID})
	for {
		pair, err := itI.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			break
		}
		if pair.Value == nil {
			continue
		}
		for _, ins := range pair.Value.Instances {
			extIP := ""
			if len(ins.GetNetworkInterfaces()) > 0 {
				for _, ac := range ins.GetNetworkInterfaces()[0].GetAccessConfigs() {
					if ac.GetNatIP() != "" {
						extIP = ac.GetNatIP()
					}
				}
			}
			fmt.Printf("  %-22s  %-10s  ext=%-18s  zone=%s\n",
				ins.GetName(), ins.GetStatus(), extIP, pair.Key)
		}
	}
}
