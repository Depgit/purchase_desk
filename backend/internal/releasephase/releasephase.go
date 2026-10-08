// Package releasephase decides which delivery phase is switched on (RELEASE_PHASE in .env)
// and which features belong to which phase. Phase N switches on that phase and every phase before it.
//
//	1 core purchase cycle: masters, vendors, mapping, requests, approvals, PO, receipt, match, payment approval
//	2 Zoho Books
//	3 utility and AMC bills, budgets, corporate
//	4 warehouse and vendor order sheets
//	5 dashboards and reports
package releasephase

import "sync/atomic"

const (
	Core      = 1
	Zoho      = 2
	Bills     = 3 // utility and AMC bills
	Budgets   = 3
	Corporate = 3
	Warehouse = 4
	Sheets    = 4 // vendor order sheets
	Dashboard = 5
	Reports   = 5
	Last      = 5
)

var current atomic.Int32

func init() { current.Store(Last) }

// Set stores the phase read from RELEASE_PHASE. Values outside 1 to 5 mean "everything on".
func Set(n int) {
	if n < Core || n > Last {
		n = Last
	}
	current.Store(int32(n))
}

// Current is the switched-on phase.
func Current() int { return int(current.Load()) }

// On reports whether the phase that a feature belongs to is switched on.
func On(phase int) bool { return Current() >= phase }

// RoleOn reports whether a standard role's flow step is in use yet.
func RoleOn(step string) bool {
	switch step {
	case "wh":
		return On(Warehouse)
	case "creq", "dh", "capp":
		return On(Corporate)
	}
	return true
}

// VendorTypeOn reports whether vendors of this type can be created yet.
func VendorTypeOn(kind string) bool {
	switch kind {
	case "utility", "amc", "other":
		return On(Bills)
	case "warehouse":
		return On(Warehouse)
	}
	return true
}

// PropertyKindOn reports whether properties of this kind can be created yet.
func PropertyKindOn(kind string) bool {
	switch kind {
	case "warehouse":
		return On(Warehouse)
	case "corporate":
		return On(Corporate)
	}
	return true
}

// NotYet is the message shown when someone reaches a feature of a later phase.
const NotYet = "This part of Purchase Desk opens in a later phase of the rollout."
