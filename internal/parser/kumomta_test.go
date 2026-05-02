package parser

import (
	"strings"
	"testing"
)

func TestParseKumoMTAStream_ExportsDeliveryOnly(t *testing.T) {
	log := `{"type":"Reception","id":"a","recipient":"bounced@gmail.com","queue":"gmail.com","response":{"code":250,"content":""}}
{"type":"Bounce","id":"a","recipient":"bounced@gmail.com","queue":"gmail.com","response":{"code":550,"content":"user unknown"}}
{"type":"Reception","id":"b","recipient":"delivered@yahoo.co.jp","queue":"yahoo.co.jp","response":{"code":250,"content":""}}
{"type":"Delivery","id":"b","recipient":"delivered@yahoo.co.jp","queue":"yahoo.co.jp","response":{"code":250,"content":"OK queued"}}
`
	r := ParseKumoMTAStream(strings.NewReader(log))

	if r.SentLines != 2 {
		t.Errorf("SentLines want 2 Reception events, got %d", r.SentLines)
	}
	if r.BouncedLines != 1 {
		t.Errorf("BouncedLines want 1, got %d", r.BouncedLines)
	}
	if len(r.Emails) != 1 || r.Emails[0] != "delivered@yahoo.co.jp" {
		t.Fatalf("Emails should contain only delivered recipients, got %v", r.Emails)
	}
}
