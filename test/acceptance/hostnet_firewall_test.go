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
	"strings"
	"testing"
	"time"

	"github.com/suknna/govirta/pkg/hostnet/firewall"
	firewalllinux "github.com/suknna/govirta/pkg/hostnet/firewall/linux"
	hostlink "github.com/suknna/govirta/pkg/hostnet/link"
	linklinux "github.com/suknna/govirta/pkg/hostnet/link/linux"
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

// TestHostnetFirewallAntiSpoofingDropsSpoofedSource is the real-kernel proof
// for the reversed-interface blocker: the endpoint anti-spoofing guard must
// match the frame's receive port (iifname == guarded port) so a spoofed source
// IP on that port is dropped while the bound source still forwards. With the
// interface match reversed (iifname == bridge) the guard never fires on real
// guest frames, so the spoofed source would forward and this test would fail.
//
// Topology (no QEMU needed; veth pairs exercise the bridge forward hook):
//
//	guardNS --(guardPeer | guardPort)--> bridge <--(targetPort | targetPeer)-- targetNS
//
// The guard endpoint advertises a bound IP and a spoofed IP on the same port.
func TestHostnetFirewallAntiSpoofingDropsSpoofedSource(t *testing.T) {
	requireHostnetAcceptanceEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	firewallManager, err := firewalllinux.NewManager()
	if err != nil {
		logFirewallDiagnostics(t, ctx)
		t.Fatalf("create firewall manager: %v", err)
	}

	const (
		boundIP   = "192.168.200.10"
		spoofedIP = "192.168.200.99"
		targetIP  = "192.168.200.20"
		guardMAC  = "02:00:00:00:30:01"
	)
	bridge := uniqueFirewallInterfaceName("bg")
	guardPort := uniqueFirewallInterfaceName("pg")
	guardPeer := uniqueFirewallInterfaceName("qg")
	targetPort := uniqueFirewallInterfaceName("pt")
	targetPeer := uniqueFirewallInterfaceName("qt")
	guardNS := uniqueFirewallInterfaceName("nsg")
	targetNS := uniqueFirewallInterfaceName("nst")

	var teardown []func()
	endpointCreated := false
	var endpointInfo firewall.RuleInfo
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		var cleanupErrs []error
		if endpointCreated {
			if err := firewallManager.DeleteEndpointAntiSpoofing(cleanupCtx, endpointInfo.Ref); err != nil {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("delete endpoint anti-spoofing: %w", err))
			}
		}
		// Deleting each namespace removes its veth and the host-side peer; the
		// bridge is deleted last. Cleanup is best-effort but failures are joined.
		for i := len(teardown) - 1; i >= 0; i-- {
			teardown[i]()
		}
		if err := errors.Join(cleanupErrs...); err != nil {
			logFirewallDiagnostics(t, cleanupCtx)
			t.Errorf("cleanup spoof-drop resources: %v", err)
		}
	})

	mustRunFirewall(t, ctx, "ip", "link", "add", "name", bridge, "type", "bridge", "stp_state", "0")
	teardown = append(teardown, func() { _, _, _ = runCommand(ctx, "ip", "link", "del", bridge) })
	mustRunFirewall(t, ctx, "ip", "link", "set", bridge, "up")

	mustRunFirewall(t, ctx, "ip", "netns", "add", guardNS)
	teardown = append(teardown, func() { _, _, _ = runCommand(ctx, "ip", "netns", "del", guardNS) })
	mustRunFirewall(t, ctx, "ip", "netns", "add", targetNS)
	teardown = append(teardown, func() { _, _, _ = runCommand(ctx, "ip", "netns", "del", targetNS) })

	// Guard endpoint: veth pair, host side enslaved to the bridge, peer in guardNS.
	mustRunFirewall(t, ctx, "ip", "link", "add", guardPort, "type", "veth", "peer", "name", guardPeer)
	mustRunFirewall(t, ctx, "ip", "link", "set", guardPort, "master", bridge)
	mustRunFirewall(t, ctx, "ip", "link", "set", guardPort, "up")
	mustRunFirewall(t, ctx, "ip", "link", "set", guardPeer, "netns", guardNS)
	mustRunFirewall(t, ctx, "ip", "netns", "exec", guardNS, "ip", "link", "set", guardPeer, "address", guardMAC)
	mustRunFirewall(t, ctx, "ip", "netns", "exec", guardNS, "ip", "addr", "add", boundIP+"/24", "dev", guardPeer)
	mustRunFirewall(t, ctx, "ip", "netns", "exec", guardNS, "ip", "addr", "add", spoofedIP+"/24", "dev", guardPeer)
	mustRunFirewall(t, ctx, "ip", "netns", "exec", guardNS, "ip", "link", "set", guardPeer, "up")
	mustRunFirewall(t, ctx, "ip", "netns", "exec", guardNS, "ip", "link", "set", "lo", "up")

	// Target endpoint: veth pair, host side enslaved to the bridge, peer in targetNS.
	mustRunFirewall(t, ctx, "ip", "link", "add", targetPort, "type", "veth", "peer", "name", targetPeer)
	mustRunFirewall(t, ctx, "ip", "link", "set", targetPort, "master", bridge)
	mustRunFirewall(t, ctx, "ip", "link", "set", targetPort, "up")
	mustRunFirewall(t, ctx, "ip", "link", "set", targetPeer, "netns", targetNS)
	mustRunFirewall(t, ctx, "ip", "netns", "exec", targetNS, "ip", "addr", "add", targetIP+"/24", "dev", targetPeer)
	mustRunFirewall(t, ctx, "ip", "netns", "exec", targetNS, "ip", "link", "set", targetPeer, "up")
	mustRunFirewall(t, ctx, "ip", "netns", "exec", targetNS, "ip", "link", "set", "lo", "up")

	// Baseline: both the bound and spoofed sources reach the target before any guard.
	if !netnsPingSucceeds(t, ctx, guardNS, boundIP, targetIP) {
		logFirewallDiagnostics(t, ctx)
		t.Fatalf("baseline ping from bound source %s failed", boundIP)
	}
	if !netnsPingSucceeds(t, ctx, guardNS, spoofedIP, targetIP) {
		logFirewallDiagnostics(t, ctx)
		t.Fatalf("baseline ping from spoofed source %s failed (topology broken)", spoofedIP)
	}

	spec := firewall.EndpointAntiSpoofingSpec{
		TableName:  "gv_acc_spoof",
		ChainName:  "forward",
		RuleOwner:  firewallAcceptanceOwner,
		BridgeName: firewall.InterfaceName(bridge),
		TapName:    firewall.InterfaceName(guardPort),
		MAC:        net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x30, 0x01},
		IPv4:       netip.MustParseAddr(boundIP),
		Priority:   firewall.ExplicitPriority(-200, firewall.PriorityNameBridgeFilter),
	}
	endpointInfo, err = firewallManager.EnsureEndpointAntiSpoofing(ctx, spec)
	if err != nil {
		logFirewallDiagnostics(t, ctx)
		t.Fatalf("ensure endpoint anti-spoofing: %v", err)
	}
	endpointCreated = true

	// The bound source must still forward; the spoofed source must now be dropped.
	if !netnsPingSucceeds(t, ctx, guardNS, boundIP, targetIP) {
		logFirewallDiagnostics(t, ctx)
		t.Fatalf("ping from bound source %s was dropped after guard install", boundIP)
	}
	if err := netnsPingOnce(ctx, guardNS, spoofedIP, targetIP); err == nil {
		logFirewallDiagnostics(t, ctx)
		t.Fatalf("ping from spoofed source %s succeeded; anti-spoofing guard did not drop it", spoofedIP)
	}
}

func mustRunFirewall(t *testing.T, ctx context.Context, name string, args ...string) {
	t.Helper()

	stdout, stderr, err := runCommand(ctx, name, args...)
	if err != nil {
		t.Fatalf("%s %s: %v\nstdout:\n%s\nstderr:\n%s", name, strings.Join(args, " "), err, stdout, stderr)
	}
}

func netnsPingOnce(ctx context.Context, ns, source, target string) error {
	_, _, err := runCommand(ctx, "ip", "netns", "exec", ns, "ping", "-c", "2", "-W", "1", "-I", source, target)
	return err
}

func netnsPingSucceeds(t *testing.T, ctx context.Context, ns, source, target string) bool {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			lastErr = err
			break
		}
		if err := netnsPingOnce(ctx, ns, source, target); err == nil {
			return true
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			lastErr = ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	t.Logf("ping from %s to %s in ns %s did not succeed: %v", source, target, ns, lastErr)
	return false
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
