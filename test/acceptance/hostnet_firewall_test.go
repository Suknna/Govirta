//go:build acceptance && linux

package acceptance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/suknna/govirta/internal/hostnet/firewall"
	firewalllinux "github.com/suknna/govirta/internal/hostnet/firewall/linux"
	hostlink "github.com/suknna/govirta/internal/hostnet/link"
	linklinux "github.com/suknna/govirta/internal/hostnet/link/linux"
)

const firewallAcceptanceOwner firewall.RuleOwner = "govirta-acceptance"

func TestHostnetFirewallMasqueradePrimitives(t *testing.T) {
	requireHostnetAcceptanceEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	manager, err := firewalllinux.NewManager()
	if err != nil {
		logFirewallDiagnostics(t, ctx)
		t.Fatalf("create firewall manager: %v", err)
	}

	spec := firewall.MasqueradeSpec{
		TableName:           "gv_acc_nat",
		ChainName:           "postrouting",
		RuleOwner:           firewallAcceptanceOwner,
		GuestCIDR:           netip.MustParsePrefix("192.168.100.0/24"),
		EgressInterfaceName: "lo",
		Priority:            firewall.ExplicitPriority(100, firewall.PriorityNameSrcNAT),
	}
	filter := firewall.RuleFilter{
		Owner:   firewall.FilterOwner(spec.RuleOwner),
		Purpose: firewall.FilterPurpose(firewall.RulePurposeMasquerade),
		Family:  firewall.FilterFamily(firewall.TableFamilyIPv4),
		Table:   firewall.FilterTable(spec.TableName),
		Chain:   firewall.FilterChain(spec.ChainName),
	}

	info, err := manager.EnsureMasquerade(ctx, spec)
	if err != nil {
		logFirewallDiagnostics(t, ctx)
		t.Fatalf("ensure masquerade: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if t.Failed() {
			logFirewallDiagnostics(t, cleanupCtx)
		}
		if err := manager.DeleteMasquerade(cleanupCtx, info.Ref); err != nil {
			logFirewallDiagnostics(t, cleanupCtx)
			t.Errorf("cleanup masquerade: %v", err)
		}
	})

	assertFirewallRuleCount(t, ctx, manager, filter, 1)
	stdout := nftRuleset(t, ctx)
	assertBytesContain(t, stdout, "gv_acc_nat", "nft ruleset")
	assertBytesContain(t, stdout, "postrouting", "nft ruleset")
	assertBytesContain(t, stdout, "masquerade", "nft ruleset")

	if err := manager.DeleteMasquerade(ctx, info.Ref); err != nil {
		logFirewallDiagnostics(t, ctx)
		t.Fatalf("delete masquerade: %v", err)
	}
	assertFirewallRuleCount(t, ctx, manager, filter, 0)
}

func TestHostnetFirewallAntiSpoofingPrimitives(t *testing.T) {
	requireHostnetAcceptanceEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	firewallManager, err := firewalllinux.NewManager()
	if err != nil {
		logFirewallDiagnostics(t, ctx)
		t.Fatalf("create firewall manager: %v", err)
	}
	linkManager := linklinux.NewManager()
	bridgeName := hostlink.Name(uniqueFirewallInterfaceName("gvfb"))
	tapName := hostlink.Name(uniqueFirewallInterfaceName("gvft"))
	if err := cleanupHostLinks(ctx, linkManager, tapName, bridgeName); err != nil {
		t.Fatalf("initial cleanup host links tap=%q bridge=%q: %v", tapName, bridgeName, err)
	}

	var endpointInfo firewall.RuleInfo
	endpointCreated := false
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if t.Failed() {
			logFirewallDiagnostics(t, cleanupCtx)
		}

		var cleanupErrs []error
		if endpointCreated {
			if err := firewallManager.DeleteEndpointAntiSpoofing(cleanupCtx, endpointInfo.Ref); err != nil {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("delete endpoint anti-spoofing: %w", err))
			}
		}
		if err := cleanupHostLinks(cleanupCtx, linkManager, tapName, bridgeName); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("delete host links: %w", err))
		}
		if err := errors.Join(cleanupErrs...); err != nil {
			logFirewallDiagnostics(t, cleanupCtx)
			t.Errorf("cleanup firewall anti-spoofing resources: %v", err)
		}
	})

	if _, err := linkManager.EnsureBridge(ctx, hostlink.BridgeSpec{
		Name:        bridgeName,
		GatewayCIDR: "192.168.100.1/24",
		MTU:         1500,
		MAC:         net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x20, 0x00},
	}); err != nil {
		logFirewallDiagnostics(t, ctx)
		t.Fatalf("ensure bridge %q: %v", bridgeName, err)
	}
	if _, err := linkManager.EnsureTap(ctx, hostlink.TapSpec{
		Name:       tapName,
		BridgeName: bridgeName,
		OwnerUID:   hostlink.ExplicitUID(0),
		OwnerGID:   hostlink.ExplicitGID(0),
		MTU:        1500,
		MAC:        net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x20, 0x01},
		VNetHeader: hostlink.VNetHeaderEnabled,
	}); err != nil {
		logFirewallDiagnostics(t, ctx)
		t.Fatalf("ensure tap %q: %v", tapName, err)
	}

	spec := firewall.EndpointAntiSpoofingSpec{
		TableName:  "gv_acc_filter",
		ChainName:  "forward",
		RuleOwner:  firewallAcceptanceOwner,
		BridgeName: firewall.InterfaceName(bridgeName),
		TapName:    firewall.InterfaceName(tapName),
		MAC:        net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x20, 0x01},
		IPv4:       netip.MustParseAddr("192.168.100.10"),
		Priority:   firewall.ExplicitPriority(-200, firewall.PriorityNameBridgeFilter),
	}
	filter := firewall.RuleFilter{
		Owner:   firewall.FilterOwner(spec.RuleOwner),
		Purpose: firewall.FilterPurpose(firewall.RulePurposeEndpointAntiSpoofing),
		Family:  firewall.FilterFamily(firewall.TableFamilyBridge),
		Table:   firewall.FilterTable(spec.TableName),
		Chain:   firewall.FilterChain(spec.ChainName),
	}

	endpointInfo, err = firewallManager.EnsureEndpointAntiSpoofing(ctx, spec)
	if err != nil {
		logFirewallDiagnostics(t, ctx)
		t.Fatalf("ensure endpoint anti-spoofing: %v", err)
	}
	endpointCreated = true

	assertFirewallRuleCount(t, ctx, firewallManager, filter, 1)
	stdout := nftRuleset(t, ctx)
	assertBytesContain(t, stdout, string(spec.TableName), "nft ruleset")
	assertBytesContain(t, stdout, string(spec.ChainName), "nft ruleset")
	assertBytesContain(t, stdout, string(spec.TapName), "nft ruleset")
	assertBytesContain(t, stdout, spec.MAC.String(), "nft ruleset")
	assertBytesContain(t, stdout, spec.IPv4.String(), "nft ruleset")
	assertBytesContain(t, stdout, "drop", "nft ruleset")

	if err := firewallManager.DeleteEndpointAntiSpoofing(ctx, endpointInfo.Ref); err != nil {
		logFirewallDiagnostics(t, ctx)
		t.Fatalf("delete endpoint anti-spoofing: %v", err)
	}
	endpointCreated = false
	if err := cleanupHostLinks(ctx, linkManager, tapName, bridgeName); err != nil {
		logFirewallDiagnostics(t, ctx)
		t.Fatalf("delete host links tap=%q bridge=%q: %v", tapName, bridgeName, err)
	}
	assertFirewallRuleCount(t, ctx, firewallManager, filter, 0)
}

func assertFirewallRuleCount(t *testing.T, ctx context.Context, manager firewall.Manager, filter firewall.RuleFilter, want int) {
	t.Helper()

	rules, err := manager.ListRules(ctx, filter)
	if err != nil {
		logFirewallDiagnostics(t, ctx)
		t.Fatalf("list firewall rules with filter %+v: %v", filter, err)
	}
	if len(rules) != want {
		logFirewallDiagnostics(t, ctx)
		t.Fatalf("firewall rule count with filter %+v = %d, want %d: %+v", filter, len(rules), want, rules)
	}
}

func nftRuleset(t *testing.T, ctx context.Context) []byte {
	t.Helper()

	stdout, stderr, err := runCommand(ctx, "nft", "list", "ruleset")
	if err != nil {
		t.Fatalf("nft list ruleset: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	return stdout
}

func assertBytesContain(t *testing.T, haystack []byte, needle string, subject string) {
	t.Helper()

	if !bytes.Contains(haystack, []byte(needle)) {
		t.Fatalf("%s does not contain %q:\n%s", subject, needle, haystack)
	}
}

func uniqueFirewallInterfaceName(prefix string) string {
	unique := (uint64(os.Getpid()) << 32) ^ uint64(time.Now().UnixNano())
	return fmt.Sprintf("%s%010x", prefix, unique&0xffffffffff)
}
