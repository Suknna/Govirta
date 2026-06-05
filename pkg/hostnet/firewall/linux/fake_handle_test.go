//go:build linux

package linux

import (
	"fmt"

	"github.com/google/nftables"
)

type fakeHandle struct {
	tables     []*nftables.Table
	chains     []*nftables.Chain
	rules      []*nftables.Rule
	calls      []string
	failures   map[string]error
	nextHandle uint64
}

func (f *fakeHandle) record(call string) {
	f.calls = append(f.calls, call)
}

func (f *fakeHandle) AddTable(table *nftables.Table) *nftables.Table {
	f.record(fmt.Sprintf("AddTable:%s:%s", nftFamilyName(table.Family), table.Name))
	f.tables = append(f.tables, table)
	return table
}

func (f *fakeHandle) DelTable(table *nftables.Table) {
	f.record(fmt.Sprintf("DelTable:%s:%s", nftFamilyName(table.Family), table.Name))
}

func (f *fakeHandle) AddChain(chain *nftables.Chain) *nftables.Chain {
	f.record(fmt.Sprintf("AddChain:%s:%s:%s", nftFamilyName(chain.Table.Family), chain.Table.Name, chain.Name))
	f.chains = append(f.chains, chain)
	return chain
}

func (f *fakeHandle) DelChain(chain *nftables.Chain) {
	f.record(fmt.Sprintf("DelChain:%s:%s:%s", nftFamilyName(chain.Table.Family), chain.Table.Name, chain.Name))
}

func (f *fakeHandle) AddRule(rule *nftables.Rule) *nftables.Rule {
	f.record(fmt.Sprintf("AddRule:%s:%s:%s", nftFamilyName(rule.Table.Family), rule.Table.Name, rule.Chain.Name))
	if rule.Handle == 0 {
		if f.nextHandle == 0 {
			f.nextHandle = 1
		}
		rule.Handle = f.nextHandle
		f.nextHandle++
	}
	f.rules = append(f.rules, rule)
	return rule
}

func (f *fakeHandle) DelRule(rule *nftables.Rule) error {
	f.record(fmt.Sprintf("DelRule:%s:%s:%s:%d", nftFamilyName(rule.Table.Family), rule.Table.Name, rule.Chain.Name, rule.Handle))
	if err := f.failure("DelRule"); err != nil {
		return err
	}
	for i, existing := range f.rules {
		if sameRuleIdentity(existing, rule) {
			f.rules = append(f.rules[:i], f.rules[i+1:]...)
			return nil
		}
	}
	return nil
}

func (f *fakeHandle) GetTables() ([]*nftables.Table, error) {
	f.record("GetTables")
	if err := f.failure("GetTables"); err != nil {
		return nil, err
	}
	return append([]*nftables.Table(nil), f.tables...), nil
}

func (f *fakeHandle) GetChains() ([]*nftables.Chain, error) {
	f.record("GetChains")
	if err := f.failure("GetChains"); err != nil {
		return nil, err
	}
	return append([]*nftables.Chain(nil), f.chains...), nil
}

func (f *fakeHandle) GetRules(table *nftables.Table, chain *nftables.Chain) ([]*nftables.Rule, error) {
	f.record(fmt.Sprintf("GetRules:%s:%s:%s", nftFamilyName(table.Family), table.Name, chain.Name))
	if err := f.failure("GetRules"); err != nil {
		return nil, err
	}
	var rules []*nftables.Rule
	for _, rule := range f.rules {
		if sameTable(rule.Table, table) && rule.Chain != nil && rule.Chain.Name == chain.Name {
			rules = append(rules, rule)
		}
	}
	return rules, nil
}

func (f *fakeHandle) Flush() error {
	f.record("Flush")
	return f.failure("Flush")
}

func (f *fakeHandle) failure(call string) error {
	if f.failures == nil {
		return nil
	}
	return f.failures[call]
}

func sameRuleIdentity(left, right *nftables.Rule) bool {
	return left != nil && right != nil &&
		sameTable(left.Table, right.Table) &&
		left.Chain != nil && right.Chain != nil &&
		left.Chain.Name == right.Chain.Name &&
		left.Handle == right.Handle
}

func nftFamilyName(family nftables.TableFamily) string {
	switch family {
	case nftables.TableFamilyIPv4:
		return "ip"
	case nftables.TableFamilyBridge:
		return "bridge"
	default:
		return fmt.Sprintf("family-%d", family)
	}
}
