package dns

// DnsRecordSpec 要写入的 DNS 记录规格
type DnsRecordSpec struct {
	RR         string `json:"rr"`
	RecordType string `json:"record_type"`
	Value      string `json:"value"`
	Priority   *int   `json:"priority,omitempty"`
}

// DnsOperationResult 单条 DNS 操作结果
type DnsOperationResult struct {
	RR         string `json:"rr"`
	RecordType string `json:"record_type"`
	Action     string `json:"action"` // add, update, skip, skip-system, delete
	Message    string `json:"message"`
	RecordID   string `json:"record_id,omitempty"`
}

// AliyunRecord 阿里云 DNS 记录
type AliyunRecord struct {
	RecordID   string `json:"record_id"`
	RR         string `json:"rr"`
	RecordType string `json:"record_type"`
	Value      string `json:"value"`
	Priority   *int   `json:"priority,omitempty"`
}

// DnsProductInstance 阿里云 DNS 付费实例
type DnsProductInstance struct {
	InstanceID    string `json:"instance_id"`
	VersionName   string `json:"version_name"`
	BindCount     int    `json:"bind_count"`
	BindUsedCount int    `json:"bind_used_count"`
	Domain        string `json:"domain"`
	EndTime       string `json:"end_time"`
}

// AliyunDomainInfo 阿里云域名信息
type AliyunDomainInfo struct {
	DomainName  string `json:"domain_name"`
	InstanceID  string `json:"instance_id"`
	RecordCount int    `json:"record_count"`
	GroupName   string `json:"group_name"`
}

// DomainOpResult 域名批量操作结果
type DomainOpResult struct {
	Domain  string `json:"domain"`
	Success bool   `json:"success"`
	Message string `json:"message"`
}
