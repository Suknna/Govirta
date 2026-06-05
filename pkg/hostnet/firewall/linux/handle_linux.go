//go:build linux

package linux

import "github.com/google/nftables"

type handle interface {
	AddTable(table *nftables.Table) *nftables.Table
	DelTable(table *nftables.Table)
	AddChain(chain *nftables.Chain) *nftables.Chain
	DelChain(chain *nftables.Chain)
	AddRule(rule *nftables.Rule) *nftables.Rule
	DelRule(rule *nftables.Rule) error
	GetTables() ([]*nftables.Table, error)
	GetChains() ([]*nftables.Chain, error)
	GetRules(table *nftables.Table, chain *nftables.Chain) ([]*nftables.Rule, error)
	Flush() error
}

type realHandle struct {
	conn *nftables.Conn
}

func newRealHandle() (handle, error) {
	conn, err := nftables.New()
	if err != nil {
		return nil, err
	}

	return &realHandle{conn: conn}, nil
}

func (h *realHandle) AddTable(table *nftables.Table) *nftables.Table {
	return h.conn.AddTable(table)
}

func (h *realHandle) DelTable(table *nftables.Table) {
	h.conn.DelTable(table)
}

func (h *realHandle) AddChain(chain *nftables.Chain) *nftables.Chain {
	return h.conn.AddChain(chain)
}

func (h *realHandle) DelChain(chain *nftables.Chain) {
	h.conn.DelChain(chain)
}

func (h *realHandle) AddRule(rule *nftables.Rule) *nftables.Rule {
	return h.conn.AddRule(rule)
}

func (h *realHandle) DelRule(rule *nftables.Rule) error {
	return h.conn.DelRule(rule)
}

func (h *realHandle) GetTables() ([]*nftables.Table, error) {
	return h.conn.ListTables()
}

func (h *realHandle) GetChains() ([]*nftables.Chain, error) {
	return h.conn.ListChains()
}

func (h *realHandle) GetRules(table *nftables.Table, chain *nftables.Chain) ([]*nftables.Rule, error) {
	return h.conn.GetRules(table, chain)
}

func (h *realHandle) Flush() error {
	return h.conn.Flush()
}
