package gcp

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	monitoring "google.golang.org/api/monitoring/v3"
	"google.golang.org/api/option"
)

type NetworkTrafficPoint struct {
	EndTime       string `json:"end_time"`
	SentBytes     int64  `json:"sent_bytes"`
	ReceivedBytes int64  `json:"received_bytes"`
}

type InstanceNetworkTraffic struct {
	InstanceID            string                `json:"instance_id"`
	SentBytes             int64                 `json:"sent_bytes"`
	ReceivedBytes         int64                 `json:"received_bytes"`
	LastHourSentBytes     int64                 `json:"last_hour_sent_bytes"`
	LastHourReceivedBytes int64                 `json:"last_hour_received_bytes"`
	Hourly                []NetworkTrafficPoint `json:"hourly"`
}

// ListNetworkTraffic 返回近 hours 小时内每台 GCE 实例的网络字节数。
// GCP Monitoring 的 network/*_bytes_count 是累计指标，这里按 1h ALIGN_DELTA 后求和。
func (c *Client) ListNetworkTraffic(ctx context.Context, hours int) (map[string]InstanceNetworkTraffic, error) {
	if c.projectID == "" {
		return nil, fmt.Errorf("projectID 为空")
	}
	if hours <= 0 {
		hours = 24
	}
	if hours > 168 {
		hours = 168
	}
	sent, err := c.listNetworkMetric(ctx, "compute.googleapis.com/instance/network/sent_bytes_count", hours)
	if err != nil {
		return nil, err
	}
	recv, err := c.listNetworkMetric(ctx, "compute.googleapis.com/instance/network/received_bytes_count", hours)
	if err != nil {
		return nil, err
	}

	out := map[string]InstanceNetworkTraffic{}
	for instanceID, points := range sent {
		item := out[instanceID]
		item.InstanceID = instanceID
		for end, bytes := range points {
			hour := ensureHour(&item, end)
			hour.SentBytes += bytes
			item.SentBytes += bytes
		}
		out[instanceID] = item
	}
	for instanceID, points := range recv {
		item := out[instanceID]
		item.InstanceID = instanceID
		for end, bytes := range points {
			hour := ensureHour(&item, end)
			hour.ReceivedBytes += bytes
			item.ReceivedBytes += bytes
		}
		out[instanceID] = item
	}

	for id, item := range out {
		sort.Slice(item.Hourly, func(i, j int) bool {
			return item.Hourly[i].EndTime < item.Hourly[j].EndTime
		})
		if len(item.Hourly) > 0 {
			last := item.Hourly[len(item.Hourly)-1]
			item.LastHourSentBytes = last.SentBytes
			item.LastHourReceivedBytes = last.ReceivedBytes
		}
		out[id] = item
	}
	return out, nil
}

func (c *Client) listNetworkMetric(ctx context.Context, metricType string, hours int) (map[string]map[string]int64, error) {
	svc, err := monitoring.NewService(ctx, option.WithTokenSource(c.tokenSource))
	if err != nil {
		return nil, fmt.Errorf("构造 Monitoring client 失败: %w", err)
	}
	end := time.Now().UTC()
	start := end.Add(-time.Duration(hours) * time.Hour)
	filter := fmt.Sprintf(`metric.type="%s" AND resource.type="gce_instance"`, metricType)
	out := map[string]map[string]int64{}

	call := svc.Projects.TimeSeries.List("projects/" + c.projectID).
		Filter(filter).
		IntervalStartTime(start.Format(time.RFC3339)).
		IntervalEndTime(end.Format(time.RFC3339)).
		AggregationAlignmentPeriod("3600s").
		AggregationPerSeriesAligner("ALIGN_DELTA").
		AggregationCrossSeriesReducer("REDUCE_SUM").
		AggregationGroupByFields("resource.label.instance_id").
		View("FULL").
		PageSize(1000)

	err = call.Pages(ctx, func(resp *monitoring.ListTimeSeriesResponse) error {
		for _, ts := range resp.TimeSeries {
			if ts == nil || ts.Resource == nil {
				continue
			}
			instanceID := ts.Resource.Labels["instance_id"]
			if instanceID == "" {
				continue
			}
			if _, ok := out[instanceID]; !ok {
				out[instanceID] = map[string]int64{}
			}
			for _, p := range ts.Points {
				if p == nil || p.Value == nil || p.Interval == nil || p.Interval.EndTime == "" {
					continue
				}
				v := typedValueBytes(p.Value)
				if v <= 0 {
					continue
				}
				out[instanceID][p.Interval.EndTime] += v
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("读取 Monitoring 指标失败 %s: %w", metricType, err)
	}
	return out, nil
}

func ensureHour(item *InstanceNetworkTraffic, end string) *NetworkTrafficPoint {
	for i := range item.Hourly {
		if item.Hourly[i].EndTime == end {
			return &item.Hourly[i]
		}
	}
	item.Hourly = append(item.Hourly, NetworkTrafficPoint{EndTime: end})
	return &item.Hourly[len(item.Hourly)-1]
}

func typedValueBytes(v *monitoring.TypedValue) int64 {
	if v.Int64Value != nil {
		return *v.Int64Value
	}
	if v.DoubleValue != nil {
		return int64(math.Round(*v.DoubleValue))
	}
	return 0
}
