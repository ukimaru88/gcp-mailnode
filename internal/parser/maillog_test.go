package parser

import (
	"strings"
	"testing"
)

// TestParsePostfix_ExcludesLocalRelay 回归测试（v0.2.38）：
// Postfix 每收到一封外部退信，会产生一条退信回执投递到 info@本域
// （relay=local, status=sent, delivered to maildir）。这种本地投递不是
// 真正对外发送成功的目标地址，必须排除，否则提取结果被自己域名污染。
//
// 样本取自用户真实节点 xrwxptqxbtflwwd.top 的 mail.log。
func TestParsePostfix_ExcludesLocalRelay(t *testing.T) {
	log := strings.Join([]string{
		// 真·外部发送成功（relay=外部 MX）—— 应被提取
		`Jun 28 04:04:51 host postfix/smtp[12734]: C67A84C844: to=<akazs822@tcn.zaq.ne.jp>, relay=mgw1.mx.zaq.ne.jp[111.96.116.214]:25, delay=6.2, delays=5.8/0/0.02/0.36, dsn=2.0.0, status=sent (250 <6951E0443EB3FB0C> Mail accepted)`,
		// 本地 maildir 投递（退信回执）relay=local —— 必须排除
		`Jun 28 04:04:31 host postfix/local[13240]: 264E44C83D: to=<info@xrwxptqxbtflwwd.top>, relay=local, delay=0.01, delays=0.01/0/0/0, dsn=2.0.0, status=sent (delivered to maildir)`,
		`Jun 28 04:04:36 host postfix/local[13204]: 47E0D4C842: to=<info@xrwxptqxbtflwwd.top>, relay=local, delay=0.01, delays=0/0/0/0, dsn=2.0.0, status=sent (delivered to maildir)`,
		// 退信（554 收件人拒绝）—— 不提取，但计入 BouncedLines
		`Jun 28 04:04:36 host postfix/smtp[12778]: 92F3E4C83C: to=<obcp@aqua.plala.or.jp>, relay=mx.plala.or.jp[220.156.64.233]:25, delay=4.7, delays=3.6/0/0.06/1, dsn=5.7.1, status=bounced (host mx.plala.or.jp[220.156.64.233] said: 554 5.7.1 Recipient rejected (in reply to RCPT TO command))`,
		// 临时拒绝（421）—— 不提取，但计入 DeferredLines
		`Jun 28 04:04:19 host postfix/error[13237]: 2D705485E0: to=<o420ezu@cameo.plala.or.jp>, relay=none, delay=63526, delays=63525/1.2/0/0.01, dsn=4.0.0, status=deferred (delivery temporarily suspended: host mx.plala.or.jp[220.156.64.233] refused to talk to me: 421 IP address deferred)`,
		// 另一个真·外部成功
		`Jun 28 04:00:00 host postfix/smtp[1]: AAA: to=<taro@example.co.jp>, relay=mx.example.co.jp[1.2.3.4]:25, delay=1, status=sent (250 ok)`,
	}, "\n")

	r := ParseMailLog(log)

	// 只应提取 2 个外部 sent 收件人
	if len(r.Emails) != 2 {
		t.Fatalf("应提取 2 个外部收件人，实际 %d: %v", len(r.Emails), r.Emails)
	}

	got := map[string]bool{}
	for _, e := range r.Emails {
		got[e] = true
	}
	if !got["akazs822@tcn.zaq.ne.jp"] {
		t.Errorf("缺少外部成功收件人 akazs822@tcn.zaq.ne.jp")
	}
	if !got["taro@example.co.jp"] {
		t.Errorf("缺少外部成功收件人 taro@example.co.jp")
	}
	// 关键断言：本地 info@ 绝不能被提取
	if got["info@xrwxptqxbtflwwd.top"] {
		t.Errorf("relay=local 的本地投递 info@xrwxptqxbtflwwd.top 不该被提取（数据污染）")
	}

	// SentLines 只计外部 sent（2 条），不计 2 条 relay=local
	if r.SentLines != 2 {
		t.Errorf("SentLines 应为 2（仅外部），实际 %d", r.SentLines)
	}
	if r.BouncedLines != 1 {
		t.Errorf("BouncedLines 应为 1，实际 %d", r.BouncedLines)
	}
	if r.DeferredLines != 1 {
		t.Errorf("DeferredLines 应为 1，实际 %d", r.DeferredLines)
	}
}
