package dns

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// AliyunDns 阿里云 DNS API 客户端
type AliyunDns struct {
	accessKeyID     string
	accessKeySecret string
	client          *http.Client
}

// NewAliyunDns 创建阿里云 DNS 客户端
func NewAliyunDns(accessKeyID, accessKeySecret string) *AliyunDns {
	return &AliyunDns{
		accessKeyID:     strings.TrimSpace(accessKeyID),
		accessKeySecret: strings.TrimSpace(accessKeySecret),
		client:          &http.Client{Timeout: 30 * time.Second},
	}
}

func (d *AliyunDns) ensureCredentials() error {
	if d.accessKeyID == "" {
		return fmt.Errorf("Aliyun AccessKey ID 为空，请在设置页填写并保存。")
	}
	if d.accessKeySecret == "" {
		return fmt.Errorf("Aliyun AccessKey Secret 为空，请在设置页填写并保存。")
	}
	return nil
}

func (d *AliyunDns) request(action string, params map[string]string) (map[string]interface{}, error) {
	if err := d.ensureCredentials(); err != nil {
		return nil, err
	}

	url := BuildSignedURL(d.accessKeyID, d.accessKeySecret, action, params)
	resp, err := d.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("Aliyun API 请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取 Aliyun API 响应失败: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Aliyun API HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("Aliyun API 响应不是有效 JSON: %v", err)
	}

	if code, ok := result["Code"].(string); ok && code != "" {
		msg, _ := result["Message"].(string)
		return nil, fmt.Errorf("Aliyun API 错误 %s: %s", code, msg)
	}

	return result, nil
}

// TestConnection 测试阿里云 API 连接
func (d *AliyunDns) TestConnection() (string, error) {
	params := map[string]string{"PageNumber": "1", "PageSize": "1"}
	resp, err := d.request("DescribeDomains", params)
	if err != nil {
		return "", err
	}
	total := jsonInt(resp, "TotalCount")
	return fmt.Sprintf("连接成功，可访问域名数量: %d", total), nil
}

// AddRecord 添加 DNS 记录
func (d *AliyunDns) AddRecord(domain, rr, recordType, value string, priority *int) (map[string]interface{}, error) {
	params := map[string]string{
		"DomainName": domain,
		"RR":         rr,
		"Type":       recordType,
		"Value":      value,
	}
	if priority != nil {
		params["Priority"] = fmt.Sprintf("%d", *priority)
	}
	return d.request("AddDomainRecord", params)
}

// UpdateRecord 更新 DNS 记录
func (d *AliyunDns) UpdateRecord(recordID, rr, recordType, value string, priority *int) (map[string]interface{}, error) {
	params := map[string]string{
		"RecordId": recordID,
		"RR":       rr,
		"Type":     recordType,
		"Value":    value,
	}
	if priority != nil {
		params["Priority"] = fmt.Sprintf("%d", *priority)
	}
	return d.request("UpdateDomainRecord", params)
}

// DeleteRecord 删除 DNS 记录
func (d *AliyunDns) DeleteRecord(recordID string) (map[string]interface{}, error) {
	params := map[string]string{"RecordId": recordID}
	return d.request("DeleteDomainRecord", params)
}

// QueryRecords 查询 DNS 记录
func (d *AliyunDns) QueryRecords(domain string, rr, recordType *string) ([]AliyunRecord, error) {
	var all []AliyunRecord
	page := 1
	for {
		records, err := d.queryRecordsPage(domain, rr, recordType, page, 500)
		if err != nil {
			return nil, err
		}
		all = append(all, records...)
		if len(records) < 500 {
			break
		}
		page++
		if page > 1000 {
			return nil, fmt.Errorf("查询 DNS 记录分页超过上限")
		}
	}
	return all, nil
}

func (d *AliyunDns) queryRecordsPage(domain string, rr, recordType *string, page, pageSize int) ([]AliyunRecord, error) {
	params := map[string]string{
		"DomainName": domain,
		"PageNumber": fmt.Sprintf("%d", page),
		"PageSize":   fmt.Sprintf("%d", pageSize),
	}
	if rr != nil {
		params["RRKeyWord"] = *rr
	}
	if recordType != nil {
		params["TypeKeyWord"] = *recordType
	}

	resp, err := d.request("DescribeDomainRecords", params)
	if err != nil {
		return nil, err
	}

	return parseRecords(resp), nil
}

// ClearDomainRecords 清空域名所有 DNS 记录（保留 NS/SOA）
func (d *AliyunDns) ClearDomainRecords(domain string) ([]DnsOperationResult, error) {
	var allRecords []AliyunRecord
	page := 1
	for {
		records, err := d.queryRecordsPage(domain, nil, nil, page, 500)
		if err != nil {
			return nil, err
		}
		if len(records) == 0 {
			break
		}
		allRecords = append(allRecords, records...)
		if len(records) < 500 {
			break
		}
		page++
		if page > 1000 {
			return nil, fmt.Errorf("清空 DNS 时分页超过上限")
		}
	}

	var results []DnsOperationResult
	for _, record := range allRecords {
		ty := strings.ToUpper(record.RecordType)
		if ty == "NS" || ty == "SOA" {
			results = append(results, DnsOperationResult{
				RR: record.RR, RecordType: record.RecordType,
				Action: "skip-system", Message: "保留 NS/SOA",
			})
			continue
		}

		_, err := d.DeleteRecord(record.RecordID)
		if err != nil {
			return results, fmt.Errorf("删除记录 %s/%s 失败: %w", record.RR, record.RecordType, err)
		}
		results = append(results, DnsOperationResult{
			RR: record.RR, RecordType: record.RecordType,
			Action: "delete", Message: "已删除",
		})
	}

	return results, nil
}

// UpsertRecord 智能写入 DNS 记录（存在则更新，否则添加）
func (d *AliyunDns) UpsertRecord(domain string, spec DnsRecordSpec) (DnsOperationResult, error) {
	rr := spec.RR
	rt := spec.RecordType

	existing, err := d.QueryRecords(domain, &rr, &rt)
	if err != nil {
		return DnsOperationResult{}, err
	}

	// 精确匹配
	for _, cur := range existing {
		if cur.RR == spec.RR && cur.RecordType == spec.RecordType {
			// 值相同则跳过
			if cur.Value == spec.Value && intPtrEqual(cur.Priority, spec.Priority) {
				return DnsOperationResult{
					RR: spec.RR, RecordType: spec.RecordType,
					Action: "skip", Message: "值相同，跳过", RecordID: cur.RecordID,
				}, nil
			}
			// 更新
			_, err := d.UpdateRecord(cur.RecordID, spec.RR, spec.RecordType, spec.Value, spec.Priority)
			if err != nil {
				return DnsOperationResult{}, err
			}
			return DnsOperationResult{
				RR: spec.RR, RecordType: spec.RecordType,
				Action: "update", Message: "已更新", RecordID: cur.RecordID,
			}, nil
		}
	}

	// 添加
	resp, err := d.AddRecord(domain, spec.RR, spec.RecordType, spec.Value, spec.Priority)
	if err != nil {
		// DomainRecordDuplicate 回退：全量查询精确匹配后更新
		if strings.Contains(err.Error(), "DomainRecordDuplicate") {
			all, err2 := d.QueryRecords(domain, nil, nil)
			if err2 != nil {
				return DnsOperationResult{}, err2
			}
			for _, cur := range all {
				if cur.RR == spec.RR && cur.RecordType == spec.RecordType {
					_, err3 := d.UpdateRecord(cur.RecordID, spec.RR, spec.RecordType, spec.Value, spec.Priority)
					if err3 != nil {
						return DnsOperationResult{}, err3
					}
					return DnsOperationResult{
						RR: spec.RR, RecordType: spec.RecordType,
						Action: "update", Message: "已更新（回退）", RecordID: cur.RecordID,
					}, nil
				}
			}
			return DnsOperationResult{}, fmt.Errorf("DomainRecordDuplicate 但全量查询找不到 %s/%s", spec.RR, spec.RecordType)
		}
		return DnsOperationResult{}, err
	}

	return DnsOperationResult{
		RR: spec.RR, RecordType: spec.RecordType,
		Action: "add", Message: "已添加", RecordID: jsonStr(resp, "RecordId"),
	}, nil
}

// SetupMailDNS 完整邮件服务器 DNS 配置
// deployUnsub=true 时才创建 unsub.<domain> A 记录，否则不创建（由 ClearDomainRecords 自动清掉旧记录）
func (d *AliyunDns) SetupMailDNS(rootDomain, subdomain, serverIP, dkimPubKey, dkimSelector string, deployUnsub bool) ([]DnsOperationResult, error) {
	// 先清空所有记录
	results, err := d.ClearDomainRecords(rootDomain)
	if err != nil {
		return results, err
	}

	sub := strings.TrimSpace(subdomain)
	var smtpRR, mailRR, mxRR, mxHost, spfRR, dkimRR, dmarcRR string
	var imapRR, pop3RR, unsubRR string

	if sub == "" {
		smtpRR = "smtp"
		mailRR = "mail"
		mxRR = "@"
		mxHost = fmt.Sprintf("mail.%s", rootDomain)
		spfRR = "@"
		dkimRR = fmt.Sprintf("%s._domainkey", dkimSelector)
		dmarcRR = "_dmarc"
		imapRR = "imap"
		pop3RR = "pop3"
		unsubRR = "unsub"
	} else {
		smtpRR = fmt.Sprintf("smtp.%s", sub)
		mailRR = fmt.Sprintf("mail.%s", sub)
		mxRR = sub
		mxHost = fmt.Sprintf("mail.%s.%s", sub, rootDomain)
		spfRR = sub
		dkimRR = fmt.Sprintf("%s._domainkey.%s", dkimSelector, sub)
		dmarcRR = fmt.Sprintf("_dmarc.%s", sub)
		imapRR = fmt.Sprintf("imap.%s", sub)
		pop3RR = fmt.Sprintf("pop3.%s", sub)
		unsubRR = fmt.Sprintf("unsub.%s", sub)
	}

	// A 记录
	aSpecs := []DnsRecordSpec{
		{RR: smtpRR, RecordType: "A", Value: serverIP},
		{RR: mailRR, RecordType: "A", Value: serverIP},
		{RR: imapRR, RecordType: "A", Value: serverIP},
		{RR: pop3RR, RecordType: "A", Value: serverIP},
	}
	if deployUnsub {
		aSpecs = append(aSpecs, DnsRecordSpec{RR: unsubRR, RecordType: "A", Value: serverIP})
	}
	for _, spec := range aSpecs {
		r, err := d.UpsertRecord(rootDomain, spec)
		if err != nil {
			return results, fmt.Errorf("A 记录写入失败 (%s/A): %w", spec.RR, err)
		}
		results = append(results, r)
	}

	// MX + TXT 记录
	mxPriority := intPtr(10)
	spfValue := fmt.Sprintf("v=spf1 ip4:%s -all", serverIP)
	dkimValue := fmt.Sprintf("v=DKIM1; k=rsa; p=%s", normalizeDKIMKey(dkimPubKey))
	dmarcValue := fmt.Sprintf("v=DMARC1; p=reject; rua=mailto:dmarc@%s", rootDomain)

	txtSpecs := []DnsRecordSpec{
		{RR: mxRR, RecordType: "MX", Value: mxHost, Priority: mxPriority},
		{RR: spfRR, RecordType: "TXT", Value: spfValue},
		{RR: dkimRR, RecordType: "TXT", Value: dkimValue},
		{RR: dmarcRR, RecordType: "TXT", Value: dmarcValue},
	}
	for _, spec := range txtSpecs {
		r, err := d.UpsertRecord(rootDomain, spec)
		if err != nil {
			return results, err
		}
		results = append(results, r)
	}

	return results, nil
}

// DescribeDnsProductInstances 获取 DNS 付费实例列表
func (d *AliyunDns) DescribeDnsProductInstances() ([]DnsProductInstance, error) {
	if err := d.ensureCredentials(); err != nil {
		return nil, err
	}

	var all []DnsProductInstance
	page := 1
	for {
		params := map[string]string{
			"PageNumber": fmt.Sprintf("%d", page),
			"PageSize":   "100",
		}
		resp, err := d.request("DescribeDnsProductInstances", params)
		if err != nil {
			return nil, err
		}

		items := parseDnsInstances(resp)
		all = append(all, items...)
		total := jsonInt(resp, "TotalCount")
		if len(all) >= total || len(items) < 100 {
			break
		}
		page++
	}
	return all, nil
}

// ListAllDomains 获取阿里云所有域名
func (d *AliyunDns) ListAllDomains() ([]AliyunDomainInfo, error) {
	if err := d.ensureCredentials(); err != nil {
		return nil, err
	}

	var all []AliyunDomainInfo
	page := 1
	for {
		params := map[string]string{
			"PageNumber": fmt.Sprintf("%d", page),
			"PageSize":   "100",
		}
		resp, err := d.request("DescribeDomains", params)
		if err != nil {
			return nil, err
		}

		items := parseDomainInfos(resp)
		all = append(all, items...)
		total := jsonInt(resp, "TotalCount")
		if len(all) >= total || len(items) < 100 {
			break
		}
		page++
	}
	return all, nil
}

// AddDomainToAliyun 添加域名到阿里云
func (d *AliyunDns) AddDomainToAliyun(domainName string) (map[string]interface{}, error) {
	params := map[string]string{"DomainName": domainName}
	return d.request("AddDomain", params)
}

// DeleteDomainFromAliyun 从阿里云删除域名
func (d *AliyunDns) DeleteDomainFromAliyun(domainName string) (map[string]interface{}, error) {
	params := map[string]string{"DomainName": domainName}
	return d.request("DeleteDomain", params)
}

// BindDomainToInstance 绑定域名到 DNS 实例
func (d *AliyunDns) BindDomainToInstance(instanceID, domainName string) (map[string]interface{}, error) {
	params := map[string]string{"InstanceId": instanceID, "DomainName": domainName}
	return d.request("BindDomainWithDnsProduct", params)
}

// UnbindDomainFromInstance 解绑域名
func (d *AliyunDns) UnbindDomainFromInstance(instanceID, domainName string) (map[string]interface{}, error) {
	params := map[string]string{"InstanceId": instanceID, "DomainName": domainName}
	return d.request("UnbindDomainFromDnsProduct", params)
}

// BatchAddAndBind 批量添加并绑定域名
func (d *AliyunDns) BatchAddAndBind(instanceID string, domainNames []string) []DomainOpResult {
	var results []DomainOpResult
	for _, domain := range domainNames {
		domain = strings.TrimSpace(strings.ToLower(domain))
		if domain == "" {
			continue
		}
		// 先添加（可能已存在）
		addResp, _ := d.AddDomainToAliyun(domain)
		nsServers := extractNSServers(addResp)
		if len(nsServers) == 0 {
			nsServers = d.queryDomainNSServers(domain)
		}

		// 再绑定
		_, err := d.BindDomainToInstance(instanceID, domain)
		if err != nil {
			results = append(results, DomainOpResult{Domain: domain, Success: false, Message: err.Error()})
		} else {
			msg := "绑定成功"
			if len(nsServers) > 0 {
				msg = fmt.Sprintf("绑定成功 | NS服务器: %s", strings.Join(nsServers, ", "))
			}
			results = append(results, DomainOpResult{Domain: domain, Success: true, Message: msg})
		}
	}
	return results
}

// BatchUnbindAndDelete 批量解绑并删除域名
func (d *AliyunDns) BatchUnbindAndDelete(instanceID string, domainNames []string) []DomainOpResult {
	var results []DomainOpResult
	for _, domain := range domainNames {
		domain = strings.TrimSpace(strings.ToLower(domain))
		if domain == "" {
			continue
		}
		if instanceID != "" {
			d.UnbindDomainFromInstance(instanceID, domain) // 忽略解绑错误
		}
		_, err := d.DeleteDomainFromAliyun(domain)
		if err != nil {
			results = append(results, DomainOpResult{Domain: domain, Success: false, Message: err.Error()})
		} else {
			results = append(results, DomainOpResult{Domain: domain, Success: true, Message: "删除成功"})
		}
	}
	return results
}

// CheckDomainNS 查询域名 NS 记录
func CheckDomainNS(domain string) ([]string, error) {
	nss, err := net.LookupNS(domain)
	if err != nil {
		return nil, fmt.Errorf("NS 查询失败: %w", err)
	}
	var results []string
	for _, ns := range nss {
		results = append(results, strings.TrimSuffix(ns.Host, "."))
	}
	return results, nil
}

// ── 内部工具 ──

func (d *AliyunDns) queryDomainNSServers(domainName string) []string {
	params := map[string]string{
		"KeyWord":    domainName,
		"SearchMode": "EXACT",
		"PageSize":   "1",
		"PageNumber": "1",
	}
	resp, err := d.request("DescribeDomains", params)
	if err != nil {
		return nil
	}
	domains := jsonArray(resp, "Domains", "Domain")
	if len(domains) == 0 {
		return nil
	}
	first, _ := domains[0].(map[string]interface{})
	return extractNSServersFromDomain(first)
}

func normalizeDKIMKey(raw string) string {
	s := strings.ReplaceAll(raw, "\"", "")
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	return strings.TrimSpace(s)
}

func intPtr(v int) *int { return &v }

func intPtrEqual(a, b *int) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func jsonInt(m map[string]interface{}, key string) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}

func jsonArray(m map[string]interface{}, keys ...string) []interface{} {
	var current interface{} = m
	for _, key := range keys {
		mm, ok := current.(map[string]interface{})
		if !ok {
			return nil
		}
		current = mm[key]
	}
	arr, _ := current.([]interface{})
	return arr
}

func parseRecords(resp map[string]interface{}) []AliyunRecord {
	rawRecords := jsonArray(resp, "DomainRecords", "Record")
	var records []AliyunRecord
	for _, raw := range rawRecords {
		m, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		r := AliyunRecord{
			RecordID:   jsonStr(m, "RecordId"),
			RR:         jsonStr(m, "RR"),
			RecordType: jsonStr(m, "Type"),
			Value:      jsonStr(m, "Value"),
		}
		if p, ok := m["Priority"]; ok {
			if pf, ok := p.(float64); ok {
				pi := int(pf)
				r.Priority = &pi
			}
		}
		records = append(records, r)
	}
	return records
}

func parseDnsInstances(resp map[string]interface{}) []DnsProductInstance {
	rawItems := jsonArray(resp, "DnsProducts", "DnsProduct")
	var items []DnsProductInstance
	for _, raw := range rawItems {
		m, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		items = append(items, DnsProductInstance{
			InstanceID:    jsonStr(m, "InstanceId"),
			VersionName:   jsonStr(m, "VersionName"),
			BindCount:     int(jsonFloat(m, "BindDomainCount")),
			BindUsedCount: int(jsonFloat(m, "BindDomainUsedCount")),
			Domain:        jsonStr(m, "Domain"),
			EndTime:       jsonStr(m, "EndTime"),
		})
	}
	return items
}

func parseDomainInfos(resp map[string]interface{}) []AliyunDomainInfo {
	rawItems := jsonArray(resp, "Domains", "Domain")
	var items []AliyunDomainInfo
	for _, raw := range rawItems {
		m, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		items = append(items, AliyunDomainInfo{
			DomainName:  jsonStr(m, "DomainName"),
			InstanceID:  jsonStr(m, "InstanceId"),
			RecordCount: int(jsonFloat(m, "RecordCount")),
			GroupName:   jsonStr(m, "GroupName"),
		})
	}
	return items
}

func extractNSServers(resp map[string]interface{}) []string {
	if resp == nil {
		return nil
	}
	return extractNSServersFromDomain(resp)
}

func extractNSServersFromDomain(m map[string]interface{}) []string {
	arr := jsonArray(m, "DnsServers", "DnsServer")
	var servers []string
	for _, v := range arr {
		if s, ok := v.(string); ok {
			servers = append(servers, s)
		}
	}
	return servers
}

func jsonStr(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func jsonFloat(m map[string]interface{}, key string) float64 {
	if v, ok := m[key]; ok {
		if f, ok := v.(float64); ok {
			return f
		}
	}
	return 0
}
