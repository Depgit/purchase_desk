// Package screens lists the app's screens (one per menu item) and works out a role's level on each: no access, view
// or edit. A role stores only the levels someone has set; any screen not set takes its starting level from the role's
// rights and flow step, which matches how the app behaved before screen levels existed.
package screens

// Levels.
const (
	None = 0
	View = 1
	Edit = 2
)

// LevelName and LevelOf convert between the stored words and the numbers.
var LevelName = map[int]string{None: "none", View: "view", Edit: "edit"}

func LevelOf(s string) (int, bool) {
	switch s {
	case "none":
		return None, true
	case "view":
		return View, true
	case "edit":
		return Edit, true
	}
	return 0, false
}

// Screen is one menu item. Phase is the release phase it opens in.
type Screen struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Group string `json:"group"`
	Phase int    `json:"phase"`
	// ViewOnly screens have nothing a role may change there: their changes stay with the System Admin.
	ViewOnly bool `json:"view_only"`
}

// All is every screen, in menu order.
var All = []Screen{
	{"approvals", "Approvals", "Purchase flow", 1, false},
	{"requests", "Purchase Requests", "Purchase flow", 1, false},
	{"purchase_orders", "Purchase Orders", "Purchase flow", 1, false},
	{"vendor_sheets", "Vendor Order Sheets", "Purchase flow", 4, false},
	{"goods_receipts", "Goods Receipts", "Purchase flow", 1, false},
	{"invoices", "Invoices", "Purchase flow", 1, false},
	{"payments", "Payments and advances", "Purchase flow", 1, false},
	{"bills_utility", "Utility Bills", "Bills, corporate and warehouse", 3, false},
	{"bills_amc", "AMC Bills", "Bills, corporate and warehouse", 3, false},
	{"corporate", "Corporate", "Bills, corporate and warehouse", 3, false},
	{"warehouse", "Warehouse", "Bills, corporate and warehouse", 4, false},
	{"vendors", "Vendors", "Masters", 1, false},
	{"items", "Items (SKU)", "Masters", 1, false},
	{"vendor_mapping", "Vendor Mapping", "Masters", 1, false},
	{"reports", "Reports", "Reports", 5, true},
	{"properties", "Properties", "Settings", 1, false},
	{"cities", "Cities & Clusters", "Settings", 1, false},
	{"budgets", "Budgets", "Settings", 3, false},
	{"corporate_setup", "Corporate Setup", "Settings", 3, false},
	{"bill_types", "Bill Types", "Settings", 3, false},
	{"item_setup", "Item Setup", "Settings", 1, false},
	{"users", "Users & Rights", "Settings", 1, false},
	{"flow", "Flow Settings", "Settings", 1, true},
	{"zoho", "Zoho Books", "Settings", 2, false},
	{"templates", "Message Templates", "Settings", 1, true},
	{"activity", "Activity Log", "Settings", 1, true},
}

var known = func() map[string]bool {
	m := map[string]bool{}
	for _, s := range All {
		m[s.Key] = true
	}
	return m
}()

// Known reports whether key is a screen.
func Known(key string) bool { return known[key] }

// IsViewOnly reports whether a role can at most view the screen.
func IsViewOnly(key string) bool {
	for _, s := range All {
		if s.Key == key {
			return s.ViewOnly
		}
	}
	return false
}

// Label gives a screen's name for messages.
func Label(key string) string {
	for _, s := range All {
		if s.Key == key {
			return s.Label
		}
	}
	return key
}

// Defaults gives a role's starting level on every screen from its rights and flow step: edit where its rights let it
// act, view where the screen was simply visible, and no access where the menu used to hide it.
func Defaults(rights []string, step string) map[string]int {
	has := func(r string) bool {
		for _, x := range rights {
			if x == r {
				return true
			}
		}
		return false
	}
	at := func(steps ...string) bool {
		for _, s := range steps {
			if s == step {
				return true
			}
		}
		return false
	}
	lv := func(edit bool) int {
		if edit {
			return Edit
		}
		return View
	}
	m := map[string]int{
		"approvals":       None,
		"requests":        lv(has("raise")),
		"purchase_orders": lv(has("issue_po")),
		"vendor_sheets":   None,
		"goods_receipts":  lv(has("receive")),
		"invoices":        lv(has("match")),
		"payments":        lv(has("pay")),
		"bills_utility":   lv(has("raise")),
		"bills_amc":       lv(has("raise")),
		"corporate":       lv(at("creq", "dh", "capp")),
		"warehouse":       lv(at("wh")),
		"vendors":         lv(has("masters") || at("finance", "cc")),
		"items":           lv(has("masters")),
		"vendor_mapping":  lv(has("masters")),
		"reports":         View,
		"properties":      lv(has("masters") || at("cc")),
		"cities":          lv(has("masters")),
		"budgets":         lv(at("cc")),
		"corporate_setup": lv(has("masters")),
		"bill_types":      lv(has("masters")),
		"item_setup":      lv(has("masters")),
		"users":           None,
		"flow":            View,
		"zoho":            lv(at("finance")),
		"templates":       View,
		"activity":        None,
	}
	if at("l2", "cc", "preop") {
		m["approvals"] = lv(has("approve"))
	}
	if at("cc", "purchase") {
		m["vendor_sheets"] = Edit
	}
	if has("masters") {
		m["users"] = View
	}
	if has("masters") || at("finance", "cc") {
		m["activity"] = View
	}
	if step == "admin" {
		for k := range m {
			m[k] = Edit
		}
	}
	return m
}

// Effective is a role's level on every screen: what is set on the role, and the starting level for the rest. A role
// that may add users can always open Users & Rights to do so (edit); one that manages roles can at least view it.
func Effective(set map[string]string, rights []string, step string, canUsers, canRoles bool) map[string]int {
	m := Defaults(rights, step)
	for k, v := range set {
		if l, ok := LevelOf(v); ok && known[k] {
			m[k] = l
		}
	}
	for _, x := range All {
		if x.ViewOnly && m[x.Key] > View && step != "admin" {
			m[x.Key] = View
		}
	}
	if canUsers {
		m["users"] = Edit
	} else if canRoles && m["users"] < View {
		m["users"] = View
	}
	return m
}
