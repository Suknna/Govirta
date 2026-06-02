//go:build linux

package linux

import "github.com/google/nftables"

type fakeHandle struct {
	calls []string
}

func (f *fakeHandle) record(call string) {
	f.calls = append(f.calls, call)
}

func (f *fakeHandle) AddTable(table *nftables.Table) *nftables.Table {
	f.record("AddTable")
	return table
}

func (f *fakeHandle) DelTable(table *nftables.Table) {
	f.record("DelTable")
}

func (f *fakeHandle) AddChain(chain *nftables.Chain) *nftables.Chain {
	f.record("AddChain")
	return chain
}

func (f *fakeHandle) DelChain(chain *nftables.Chain) {
	f.record("DelChain")
}

func (f *fakeHandle) AddRule(rule *nftables.Rule) *nftables.Rule {
	f.record("AddRule")
	return rule
}

func (f *fakeHandle) DelRule(rule *nftables.Rule) error {
	f.record("DelRule")
	return nil
}

func (f *fakeHandle) GetTables() ([]*nftables.Table, error) {
	f.record("GetTables")
	return nil, nil
}

func (f *fakeHandle) GetChains() ([]*nftables.Chain, error) {
	f.record("GetChains")
	return nil, nil
}

func (f *fakeHandle) GetRules(table *nftables.Table, chain *nftables.Chain) ([]*nftables.Rule, error) {
	f.record("GetRules")
	return nil, nil
}

func (f *fakeHandle) Flush() error {
	f.record("Flush")
	return nil
}
