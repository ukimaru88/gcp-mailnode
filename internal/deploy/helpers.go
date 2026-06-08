package deploy

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"gcp-mailnode/internal/dns"
	"gcp-mailnode/internal/gcp"
	"gcp-mailnode/internal/sshkey"
)

type mailDNSRRs struct {
	A     string
	DKIM  string
	MX    string
	SPF   string
	DMARC string
}

func NormalizeDeploySubdomain(subdomain string) string {
	subdomain = strings.TrimSpace(strings.TrimSuffix(subdomain, "."))
	if subdomain == "" || subdomain == "@" {
		return "@"
	}
	return subdomain
}

func DNSRRsForSubdomain(subdomain string) mailDNSRRs {
	subdomain = NormalizeDeploySubdomain(subdomain)
	if subdomain == "@" {
		return mailDNSRRs{
			A:     "@",
			DKIM:  DefaultSelector + "._domainkey",
			MX:    "@",
			SPF:   "@",
			DMARC: "_dmarc",
		}
	}
	return mailDNSRRs{
		A:     subdomain,
		DKIM:  DefaultSelector + "._domainkey." + subdomain,
		MX:    subdomain,
		SPF:   subdomain,
		DMARC: "_dmarc." + subdomain,
	}
}

// SMTPEntryRR 计算 mail-toolkit 约定的 SMTP 入口 A 记录的 RR 字段（写入根域 zone）。
//   subdomain=="@" → "smtp"       （结果 smtp.根域 → IP）
//   subdomain=="mail1" → "smtp.mail1"（结果 smtp.mail1.根域 → IP）
// v0.2.27：子域模式下漏建 smtp.子域.根域 A 记录的修复。
func SMTPEntryRR(subdomain string) string {
	if subdomain == "" || subdomain == "@" {
		return "smtp"
	}
	return "smtp." + subdomain
}

func SubdomainFromFQDN(fqdn, rootDomain string) string {
	fqdn = strings.TrimSpace(strings.TrimSuffix(fqdn, "."))
	rootDomain = strings.TrimSpace(strings.TrimSuffix(rootDomain, "."))
	if fqdn == "" || rootDomain == "" || strings.EqualFold(fqdn, rootDomain) {
		return "@"
	}
	suffix := "." + rootDomain
	if strings.HasSuffix(strings.ToLower(fqdn), strings.ToLower(suffix)) {
		return NormalizeDeploySubdomain(fqdn[:len(fqdn)-len(suffix)])
	}
	return NormalizeDeploySubdomain(fqdn)
}

func mergeMailNodeTag(tags []string) []string {
	seen := make(map[string]bool, len(tags)+1)
	merged := make([]string, 0, len(tags)+1)
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		merged = append(merged, tag)
	}
	if !seen[gcp.MailNodeTag] {
		merged = append(merged, gcp.MailNodeTag)
	}
	return merged
}

func sshBootstrapSnippet() string {
	pubKey := strings.TrimSpace(sshkey.PublicSSH())
	return fmt.Sprintf(`mkdir -p /root/.ssh
chmod 700 /root/.ssh
touch /root/.ssh/authorized_keys
grep -qxF %q /root/.ssh/authorized_keys || echo %q >> /root/.ssh/authorized_keys
chmod 600 /root/.ssh/authorized_keys
sed -i 's/^#*PermitRootLogin.*/PermitRootLogin prohibit-password/' /etc/ssh/sshd_config
sed -i 's/^#*PasswordAuthentication.*/PasswordAuthentication no/' /etc/ssh/sshd_config
sed -i 's/^#*PubkeyAuthentication.*/PubkeyAuthentication yes/' /etc/ssh/sshd_config
systemctl restart ssh || systemctl restart sshd || true
`, pubKey, pubKey)
}

func buildStartupScript(metadataScript string) string {
	bootstrap := sshBootstrapSnippet()
	metadataScript = strings.TrimSpace(metadataScript)
	if metadataScript == "" {
		return "#!/bin/bash\nset -e\n" + bootstrap
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(metadataScript))
	return fmt.Sprintf(`#!/bin/bash
set -e
%s
base64 -d > /tmp/mailnode_metadata_script.sh <<'EOF_MAILNODE_METADATA'
%s
EOF_MAILNODE_METADATA
chmod +x /tmp/mailnode_metadata_script.sh
set +e
/bin/bash /tmp/mailnode_metadata_script.sh
mailnode_metadata_status=$?
set -e
%s
exit ${mailnode_metadata_status}
`, bootstrap, encoded, bootstrap)
}

func upsertAliyunRecordAndSyncLocal(ctx context.Context, db *sql.DB, aliyunDNS *dns.AliyunDns, aliyunCredID, domain, vpsID string, spec dns.DnsRecordSpec, log func(string, string, ...interface{})) error {
	result, err := aliyunDNS.UpsertRecord(domain, spec)
	if err != nil {
		return err
	}
	if log != nil {
		log("INFO", "DNS %s %s/%s record_id=%s", result.Action, spec.RR, spec.RecordType, result.RecordID)
	}
	if db != nil {
		syncLocalDNSRecord(ctx, db, aliyunCredID, domain, vpsID, spec, result.RecordID)
	}
	return nil
}

func syncLocalDNSRecord(ctx context.Context, db *sql.DB, aliyunCredID, domain, vpsID string, spec dns.DnsRecordSpec, recordID string) {
	if db == nil {
		return
	}
	res, err := db.ExecContext(ctx,
		`UPDATE dns_records
		 SET value=?, aliyun_record_id=?, related_instance_id=?
		 WHERE aliyun_cred_id=? AND domain=? AND rr=? AND record_type=? AND related_instance_id=?`,
		spec.Value, recordID, vpsID, aliyunCredID, domain, spec.RR, spec.RecordType, vpsID)
	if err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			return
		}
	}
	res, err = db.ExecContext(ctx,
		`UPDATE dns_records
		 SET value=?, aliyun_record_id=?, related_instance_id=?
		 WHERE aliyun_cred_id=? AND domain=? AND rr=? AND record_type=? AND COALESCE(related_instance_id,'')=''`,
		spec.Value, recordID, vpsID, aliyunCredID, domain, spec.RR, spec.RecordType)
	if err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			return
		}
	}
	_, _ = db.ExecContext(ctx,
		`INSERT INTO dns_records (id, aliyun_cred_id, domain, rr, record_type, value, aliyun_record_id, related_instance_id)
		 VALUES (?,?,?,?,?,?,?,?)`,
		uuid.NewString(), aliyunCredID, domain, spec.RR, spec.RecordType, spec.Value, recordID, vpsID)
}
