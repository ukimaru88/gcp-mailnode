package deploy

import (
	"strings"
	"testing"
)

func TestRenderInitLua_HideClientIPUsesTraceHeaders(t *testing.T) {
	v := BuildDeployVars("example.com", "mail", "1.2.3.4")
	v.HideClientIP = true

	out, err := RenderInitLua(v)
	if err != nil {
		t.Fatalf("RenderInitLua error: %v", err)
	}

	for _, placeholder := range []string{"{FQDN}", "{DOMAIN}", "{SELECTOR}", "{BIND_IP}", "{TRACE_RECEIVED}", "{TRACE_SUPPLEMENTAL}"} {
		if strings.Contains(out, placeholder) {
			t.Fatalf("init.lua has unreplaced placeholder %s:\n%s", placeholder, out)
		}
	}
	if got := strings.Count(out, "trace_headers = {"); got != 2 {
		t.Fatalf("expected trace_headers on both 25 and 587 listeners, got %d", got)
	}
	if !strings.Contains(out, "received_header = false") || !strings.Contains(out, "supplemental_header = false") {
		t.Fatalf("hide-client-IP mode must disable KumoMTA trace headers:\n%s", out)
	}
	if containsActiveLuaCall(out, "msg:remove_all_named_headers") {
		t.Fatalf("init.lua must not remove all Received headers; that can delete brutal-mailer persona chains:\n%s", out)
	}
	if !strings.Contains(out, "get_egress_path_config") {
		t.Fatalf("init.lua must define get_egress_path_config for KumoMTA 2026.03+:\n%s", out)
	}
}

func containsActiveLuaCall(src, call string) bool {
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		if strings.Contains(trimmed, call) {
			return true
		}
	}
	return false
}

func TestRenderInitLua_VisibleModeKeepsTraceHeaders(t *testing.T) {
	v := BuildDeployVars("example.com", "@", "1.2.3.4")
	v.HideClientIP = false

	out, err := RenderInitLua(v)
	if err != nil {
		t.Fatalf("RenderInitLua error: %v", err)
	}
	if !strings.Contains(out, "received_header = true") || !strings.Contains(out, "supplemental_header = true") {
		t.Fatalf("visible mode must keep KumoMTA trace headers:\n%s", out)
	}
}

func TestRenderInitLua_SourceAddressUsesProvidedInternalIP(t *testing.T) {
	v := BuildDeployVars("example.com", "mail", "10.146.0.12")

	out, err := RenderInitLua(v)
	if err != nil {
		t.Fatalf("RenderInitLua error: %v", err)
	}
	if !strings.Contains(out, "source_address = '10.146.0.12'") {
		t.Fatalf("KumoMTA source_address must use VM internal IP:\n%s", out)
	}
	if strings.Contains(out, "source_address = '35.") {
		t.Fatalf("KumoMTA source_address must not use external NAT IP:\n%s", out)
	}
}

func TestRenderInstallKumoMTAIncludesZstd(t *testing.T) {
	out, err := RenderInstallKumoMTA(BuildDeployVars("example.com", "mail", "1.2.3.4"))
	if err != nil {
		t.Fatalf("RenderInstallKumoMTA error: %v", err)
	}
	if !strings.Contains(out, " zstd") {
		t.Fatalf("install_kumomta.sh must install zstd for archived log extraction:\n%s", out)
	}
}
