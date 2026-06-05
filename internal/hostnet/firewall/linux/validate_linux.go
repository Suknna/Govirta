//go:build linux

package linux

import (
	"context"
	"fmt"

	"github.com/suknna/govirta/internal/hostnet/firewall"
	"github.com/suknna/govirta/internal/hostnet/firewall/firewallerr"
	"github.com/suknna/govirta/internal/hostnet/link"
)

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return firewallerr.ErrInvalidRequest
	}
	return ctx.Err()
}

// Canonical Govirta firewall chain priorities, fixed per purpose. Each Govirta
// behavior installs its rules in a chain at exactly one priority so the relative
// ordering of NAT, bridge-filter, and forward-filter processing is deterministic
// and not a caller-tunable knob. validatePriority enforces these exact values.
const (
	canonicalSrcNATPriority        = 100
	canonicalBridgeFilterPriority  = -200
	canonicalForwardFilterPriority = 0
)

func validateMasqueradeSpec(ctx context.Context, spec firewall.MasqueradeSpec) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := validateSafeName("table", string(spec.TableName)); err != nil {
		return err
	}
	if err := validateSafeName("chain", string(spec.ChainName)); err != nil {
		return err
	}
	if err := validateSafeName("owner", string(spec.RuleOwner)); err != nil {
		return err
	}
	if !spec.GuestCIDR.IsValid() || !spec.GuestCIDR.Addr().Is4() || spec.GuestCIDR.Bits() == 0 {
		return invalidRequest("guest CIDR must be a non-zero IPv4 prefix")
	}
	if err := validateInterfaceName("egress interface", spec.EgressInterfaceName); err != nil {
		return err
	}
	return validatePriority(spec.Priority, firewall.PriorityNameSrcNAT)
}

func validateForwardAcceptSpec(ctx context.Context, spec firewall.ForwardAcceptSpec) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := validateSafeName("table", string(spec.TableName)); err != nil {
		return err
	}
	if err := validateSafeName("chain", string(spec.ChainName)); err != nil {
		return err
	}
	if err := validateSafeName("owner", string(spec.RuleOwner)); err != nil {
		return err
	}
	if !spec.GuestCIDR.IsValid() || !spec.GuestCIDR.Addr().Is4() || spec.GuestCIDR.Bits() == 0 {
		return invalidRequest("guest CIDR must be a non-zero IPv4 prefix")
	}
	if err := validateInterfaceName("egress interface", spec.EgressInterfaceName); err != nil {
		return err
	}
	return validatePriority(spec.Priority, firewall.PriorityNameForwardFilter)
}

func validateEndpointAntiSpoofingSpec(ctx context.Context, spec firewall.EndpointAntiSpoofingSpec) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := validateSafeName("table", string(spec.TableName)); err != nil {
		return err
	}
	if err := validateSafeName("chain", string(spec.ChainName)); err != nil {
		return err
	}
	if err := validateSafeName("owner", string(spec.RuleOwner)); err != nil {
		return err
	}
	if err := validateInterfaceName("bridge", spec.BridgeName); err != nil {
		return err
	}
	if err := validateInterfaceName("tap", spec.TapName); err != nil {
		return err
	}
	if len(spec.MAC) != 6 || spec.MAC[0]&1 != 0 {
		return invalidRequest("endpoint MAC must be a unicast 6-byte address")
	}
	if !spec.IPv4.IsValid() || !spec.IPv4.Is4() || !spec.IPv4.IsGlobalUnicast() {
		return invalidRequest("endpoint IPv4 must be a usable unicast IPv4 address")
	}
	return validatePriority(spec.Priority, firewall.PriorityNameBridgeFilter)
}

func validateRuleRef(ctx context.Context, ref firewall.RuleRef, purpose firewall.RulePurpose) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if ref.Purpose != purpose {
		return invalidRequest("rule purpose does not match operation")
	}
	if err := validateSafeName("owner", string(ref.Owner)); err != nil {
		return err
	}
	if err := validateFamilyForPurpose(ref.Family, ref.Purpose); err != nil {
		return err
	}
	if err := validateSafeName("table", string(ref.TableName)); err != nil {
		return err
	}
	if err := validateSafeName("chain", string(ref.ChainName)); err != nil {
		return err
	}
	if ref.Handle == 0 {
		return invalidRequest("rule handle must be non-zero")
	}
	return nil
}

// validateGroupDeleteRef adds the group-delete precondition on top of the basic
// rule ref validity check: a multi-rule group delete (endpoint anti-spoofing,
// forward-accept) resolves the group by its stable logical GroupKey, so the ref
// must carry one. GetRule is intentionally not subject to this check because it
// selects a single observed rule by handle.
func validateGroupDeleteRef(ctx context.Context, ref firewall.RuleRef, purpose firewall.RulePurpose) error {
	if err := validateRuleRef(ctx, ref, purpose); err != nil {
		return err
	}
	if ref.GroupKey == "" {
		return invalidRequest("rule group key must be set for group rule deletes")
	}
	return nil
}

func validateRuleQuery(ctx context.Context, query firewall.RuleQuery) error {
	switch query.Ref.Purpose {
	case firewall.RulePurposeMasquerade, firewall.RulePurposeEndpointAntiSpoofing, firewall.RulePurposeForwardAccept:
		return validateRuleRef(ctx, query.Ref, query.Ref.Purpose)
	default:
		if err := checkContext(ctx); err != nil {
			return err
		}
		return invalidRequest("rule query must use a supported purpose")
	}
}

func validateRuleFilter(ctx context.Context, filter firewall.RuleFilter) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := validateOwnerFilter(filter.Owner); err != nil {
		return err
	}
	if err := validatePurposeFilter(filter.Purpose); err != nil {
		return err
	}
	if err := validateFamilyFilter(filter.Family); err != nil {
		return err
	}
	if err := validateTableFilter(filter.Table); err != nil {
		return err
	}
	return validateChainFilter(filter.Chain)
}

func validateSafeName(kind string, value string) error {
	if value == "" || value == "." || value == ".." {
		return invalidRequest("%s name must be non-empty and not dot-relative", kind)
	}
	for i := 0; i < len(value); i++ {
		b := value[i]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '.' || b == '-' {
			continue
		}
		return invalidRequest("%s name contains unsafe byte", kind)
	}
	return nil
}

func validateInterfaceName(kind string, value firewall.InterfaceName) error {
	if err := validateSafeName(kind, string(value)); err != nil {
		return err
	}
	if len(value) > link.MaxInterfaceNameLength {
		return invalidRequest("%s name exceeds Linux interface length", kind)
	}
	return nil
}

func validatePriority(priority firewall.Priority, expected firewall.PriorityName) error {
	if !priority.Set {
		return invalidRequest("priority must be explicit")
	}
	if priority.Name != expected {
		return invalidRequest("priority name does not match operation")
	}
	switch expected {
	case firewall.PriorityNameSrcNAT:
		if priority.Value != canonicalSrcNATPriority {
			return invalidRequest("srcnat priority must be %d", canonicalSrcNATPriority)
		}
	case firewall.PriorityNameBridgeFilter:
		if priority.Value != canonicalBridgeFilterPriority {
			return invalidRequest("bridge filter priority must be %d", canonicalBridgeFilterPriority)
		}
	case firewall.PriorityNameForwardFilter:
		if priority.Value != canonicalForwardFilterPriority {
			return invalidRequest("forward filter priority must be %d", canonicalForwardFilterPriority)
		}
	default:
		return invalidRequest("unsupported priority name")
	}
	return nil
}

func validateOwnerFilter(filter firewall.OwnerFilter) error {
	switch filter.Mode {
	case firewall.OwnerAny:
		if filter.Value != "" {
			return invalidRequest("owner any filter must not carry a value")
		}
		return nil
	case firewall.OwnerValue:
		return validateSafeName("owner", string(filter.Value))
	default:
		return invalidRequest("owner filter mode must be explicit")
	}
}

func validatePurposeFilter(filter firewall.PurposeFilter) error {
	switch filter.Mode {
	case firewall.PurposeAny:
		if filter.Value != "" {
			return invalidRequest("purpose any filter must not carry a value")
		}
		return nil
	case firewall.PurposeValue:
		return validatePurpose(filter.Value)
	default:
		return invalidRequest("purpose filter mode must be explicit")
	}
}

func validateFamilyFilter(filter firewall.FamilyFilter) error {
	switch filter.Mode {
	case firewall.FamilyAny:
		if filter.Value != "" {
			return invalidRequest("family any filter must not carry a value")
		}
		return nil
	case firewall.FamilyValue:
		return validateFamily(filter.Value)
	default:
		return invalidRequest("family filter mode must be explicit")
	}
}

func validateTableFilter(filter firewall.TableFilter) error {
	switch filter.Mode {
	case firewall.TableAny:
		if filter.Value != "" {
			return invalidRequest("table any filter must not carry a value")
		}
		return nil
	case firewall.TableValue:
		return validateSafeName("table", string(filter.Value))
	default:
		return invalidRequest("table filter mode must be explicit")
	}
}

func validateChainFilter(filter firewall.ChainFilter) error {
	switch filter.Mode {
	case firewall.ChainAny:
		if filter.Value != "" {
			return invalidRequest("chain any filter must not carry a value")
		}
		return nil
	case firewall.ChainValue:
		return validateSafeName("chain", string(filter.Value))
	default:
		return invalidRequest("chain filter mode must be explicit")
	}
}

func validateFamilyForPurpose(family firewall.TableFamily, purpose firewall.RulePurpose) error {
	switch purpose {
	case firewall.RulePurposeMasquerade, firewall.RulePurposeForwardAccept:
		if family != firewall.TableFamilyIPv4 {
			return invalidRequest("masquerade and forward-accept rules must use IPv4 table family")
		}
	case firewall.RulePurposeEndpointAntiSpoofing:
		if family != firewall.TableFamilyBridge {
			return invalidRequest("endpoint anti-spoofing rules must use bridge table family")
		}
	default:
		return invalidRequest("unsupported rule purpose")
	}
	return nil
}

func validatePurpose(purpose firewall.RulePurpose) error {
	switch purpose {
	case firewall.RulePurposeMasquerade, firewall.RulePurposeEndpointAntiSpoofing, firewall.RulePurposeForwardAccept:
		return nil
	default:
		return invalidRequest("unsupported rule purpose")
	}
}

func validateFamily(family firewall.TableFamily) error {
	switch family {
	case firewall.TableFamilyIPv4, firewall.TableFamilyBridge:
		return nil
	default:
		return invalidRequest("unsupported table family")
	}
}

func invalidRequest(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{firewallerr.ErrInvalidRequest}, args...)...)
}
