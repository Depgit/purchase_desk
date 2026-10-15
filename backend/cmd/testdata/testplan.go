package main

import (
	"fmt"
	"strings"

	"purchasedesk/internal/spreadsheet"
)

// testPlan is the workbook that says how to load the data set and what to test with it: load order, setup done on
// screens, logins, scenarios with the expected result, and things that must be refused.
func testPlan(codes []string) file {
	code := func(name string) string {
		for i, it := range items {
			if it.name == name {
				return codes[i]
			}
		}
		return "?"
	}
	rates := 0
	for _, it := range items {
		if it.rate == 0 {
			continue
		}
		rates++
		if strings.Contains("GA CC DG DY", it.sc) {
			rates++
		}
	}
	load := [][]string{
		{"0", "(none)", "Start a fresh test copy: an empty database, SEED_DEMO=false. Sign in as the System Admin from .env.", "Admin", "Do not load this into the live database: the test names and codes would mix with real data."},
		{"1", "01-cities-clusters.xlsx", "Settings › Cities & Clusters › Bulk upload / edit", "Admin", "3 cities and 4 clusters ready"},
		{"2", "02-categories-subcategories.xlsx", "Settings › Item Setup › Categories & sub-categories › Bulk upload / edit", "Admin", "5 categories and 11 sub-categories ready"},
		{"3", "03-pack-sizes.xlsx", "Settings › Item Setup › Pack sizes › Bulk upload", "Admin", fmt.Sprintf("%d pack sizes ready", len(packSizes))},
		{"4", "04-properties.xlsx", "Settings › Properties › Bulk upload / edit properties", "Admin", "10 properties ready; they get the codes P01 to P10"},
		{"5", "05-users.xlsx", "Settings › Users & Rights › People › Bulk upload / edit users", "Admin", fmt.Sprintf("%d rows ready (Pooja Batra has two roles). Every password is %s", len(users), password)},
		{"6", "06-cluster-managers.xlsx", "Settings › Cities & Clusters › Bulk upload / edit", "Admin", "4 clusters updated with their Cluster Manager"},
		{"7", "07-property-managers.xlsx", "Settings › Properties › Bulk upload / edit properties", "Admin", "7 properties updated with their Property Manager; 3 unchanged"},
		{"8", "(Manual setup sheet, part A)", "Settings › Bill Types: add the 5 bill types", "Admin", "Needed before step 9: utility and AMC vendors name them"},
		{"9", "08-vendors-items-rates.xlsx", "Vendor Mapping › SKU rates › Bulk upload / edit vendor rates", "Admin", fmt.Sprintf("Vendors sheet %d rows, Items sheet %d rows, Vendor rates sheet %d rows ready", len(vendors), len(items), rates)},
		{"10", "(Manual setup sheet, part B)", "Vendors › Verify bank details for every vendor", "Rohan Mehra (Finance)", "A vendor can be used only once its bank details are verified"},
		{"11", "09-vendor-mapping.xlsx", "Vendor Mapping › Bulk upload / edit vendor mapping", "Admin", fmt.Sprintf("%d mappings ready", len(mappings))},
		{"12", "10-warehouse-rates.xlsx", "Vendor Mapping › SKU rates › Bulk upload / edit vendor rates", "Admin", fmt.Sprintf("%d Central Warehouse rates ready", len(warehouseStock))},
		{"12b", "(no file)", "Masters › Masters to approve: tick all, Approve selected", "Rohan Mehra (Finance)", "Every category, sub-category, item, property and bill type loaded above waits for Finance: nothing can be ordered or budgeted, and nothing goes to Zoho, until approved. Categories go before their sub-categories, and those before their items (Approve selected does that order itself)."},
		{"13", "11-budgets.xlsx", "Settings › Budgets › Bulk upload / edit budgets", "Admin or Amit Bansal (Cost Controller)", "77 monthly budgets for the current month"},
		{"14", "12-fnb-sales.xlsx", "Settings › Budgets › F&B sales › Upload F&B sales", "Admin or Amit Bansal", "One row per live property per day of this month so far"},
		{"15", "13-warehouse-opening-stock.xlsx", "Warehouse › Upload opening stock", "Vikram Singh (Warehouse Manager)", fmt.Sprintf("%d items; %s starts below its reorder level", len(warehouseStock), code("A4 paper 75 GSM"))},
		{"16", "(Manual setup sheet, part C)", "Corporate Setup, Warehouse delivery partners, Flow Settings", "Admin", "Needed for the corporate and warehouse scenarios"},
		{"17", "14-request-item-list-P01.xlsx", "Used in scenario PR-03 (Upload item list on New purchase request)", "Riya Sharma", "Rows ready; PVC pipe carries an estimated rate"},
	}
	manual := [][]string{
		{"A", "Settings › Bill Types › + Add bill type", "Electricity · Utility · GST 0%"},
		{"A", "", "Water · Utility · GST 0%"},
		{"A", "", "Internet / broadband · Utility · GST 18%"},
		{"A", "", "Lift AMC · Maintenance AMC · GST 18%"},
		{"A", "", "Pest control · Maintenance AMC · GST 18%"},
		{"B", "Vendors (as Rohan Mehra, Finance)", "Verify bank details of V001 to V017"},
		{"C", "Settings › Corporate Setup › + Add department", "Admin · L1 Rakesh Jain · L2 Anil Sethi"},
		{"C", "", "IT · L1 Vivek Nair · L2 Anil Sethi"},
		{"C", "Settings › Corporate Setup › Expense heads", "IT Equipment; Office Supplies; Professional Fees; Travel and Conveyance; Repairs and Maintenance - Office"},
		{"C", "Settings › Corporate Setup › Rules", "L1 alone is enough below ₹10,000"},
		{"C", "Warehouse › Delivery partners › + Add", "Own vehicle 1 (Tata Ace) · Own vehicle · Ramesh · 9810050001"},
		{"C", "", "QuickMove Logistics · Delivery partner · Assigned per trip · 9810050002"},
		{"C", "Settings › Flow Settings", "Also send to the Cluster Manager above ₹25,000 (for scenario AP-04). Keep the rest as it is."},
		{"D (optional)", "Settings › Zoho Books", "Only with a Zoho test organisation: connect, Sync now, map the property tags, accounts, taxes and vendors."},
	}
	logins := [][]string{}
	for _, u := range users {
		login := u.email
		if login == "" {
			login = u.mobile + " (mobile, one-time code)"
		}
		logins = append(logins, []string{u.name, login, password, u.role, u.access, u.categories})
	}
	P := func(code string) string {
		for _, p := range properties {
			if p.code == code {
				return code + " " + strings.Split(p.name, ",")[0]
			}
		}
		return code
	}
	sc := [][]string{
		// purchase requests
		{"PR-01", "Purchase request", "Riya Sharma", "+ Purchase request › " + P("P01") + ". Tap Soap bar (" + code("Soap bar 20 g") + ") 3 times, Floor cleaner once, A4 paper twice. Send for approval.", "Items from different vendors become separate requests: one per vendor (Central Warehouse items become a warehouse order). Numbers start PR_TA_0001, split as _01, _02 ..."},
		{"PR-02", "Purchase request", "Riya Sharma", "Open + Purchase request. Riya only has the Housekeeping category: look at the tabs.", "Only Housekeeping items are offered (category limit on her role)."},
		{"PR-03", "Purchase request", "Riya Sharma", "New request › Upload item list › 14-request-item-list-P01.xlsx › Check file › Add rows. Send.", "Rows land in the cart; PVC pipe shows its estimated rate (est.)."},
		{"PR-04", "Purchase request", "Riya Sharma", "Add 5 items, press Save draft, leave. Open Purchase Requests.", "'Your saved drafts, not sent yet' lists it; Open restores the cart."},
		{"PR-05", "Purchase request", "Mohan Lal", "New request at " + P("P03") + ": tap Key card sleeve (" + code("Key card sleeve") + ").", "Available (its scope is P01, P02, P03)."},
		{"PR-06", "Purchase request", "Pooja Batra", "New request at " + P("P05") + ": search Key card sleeve.", "Not offered: not available at P05."},
		{"PR-07", "Purchase request", "Lakshmi N", "New request at " + P("P07") + ": open the Food & Beverage tab, Beverages.", "Beverage tiles say not mapped (Bengaluru has no beverage vendor on purpose)."},
		{"PR-08", "Purchase request", "Riya Sharma", "Raise a request, then Edit it before anyone approves: add an item from another vendor.", "The edit splits off a new PR for the other vendor; the original is renumbered _01 and logged."},
		{"PR-09", "Purchase request", "Riya Sharma", "On an own request: Clone.", "A new request opens with the same property and items."},
		{"PR-10", "Purchase request", "Arun Verma", "Raise at " + P("P02") + " with Soap bar × 3 (Guest Amenities budget is only ₹3,000 there).", "The cart shows Over budget; approvers see the flag."},
		// approvals
		{"AP-01", "Approvals", "Amit Bansal", "Approvals: approve one of Riya's requests; change a quantity on another before approving.", "Approved; the requester sees 'Quantity changed by approver'."},
		{"AP-02", "Approvals", "Amit Bansal", "Return a request with reason 'Quantity too high'. Then, as Riya, change and resubmit.", "Returned with the reason; resubmitted request starts its route again."},
		{"AP-03", "Approvals", "Amit Bansal", "Reject a request with reason 'Other' and no remark, then with a remark.", "Without a remark it is refused; with one it is rejected and closed."},
		{"AP-04", "Approvals", "Riya Sharma › Neeraj Gupta › Amit Bansal", "Raise at P01 above ₹25,000 (e.g. Basmati rice × 12). Approve as Neeraj, then Amit.", "Goes to the Cluster Manager first, then the Cost Controller."},
		{"AP-05", "Approvals", "Riya Sharma", "Raise with 'Ordering extra for an event' ticked, reason and details.", "Routed to the Cluster Manager first."},
		{"AP-06", "Approvals", "Amit Bansal", "Send a request 'To CM' with a reason.", "Goes to the Cluster Manager; once approved there it goes straight to PO."},
		{"AP-07", "Approvals", "Amit Bansal", "Group by Cluster; tick two groups; bulk approve.", "All selected approved, results per request."},
		// purchase orders and advances
		{"PO-01", "Purchase order", "Amit Bansal", "Approve a request from Daily Harvest (V004, Net 15): the button says 'Approve and raise PO'.", "Approving raises PO_TA_0001 at once (same base as the PR, on the vendor's Net 15 terms, deliver by today + Flow Settings days); vendor emailed. No Purchase step."},
		{"PO-01b", "Push to vendor", "Riya Sharma › Amit Bansal", "After PO-01, look at the request in Purchase Requests (Riya). Then as Amit: Vendor Order Sheets (badge), open Daily Harvest, Send.", "Riya sees 'Push to vendor' until the sheet goes, then 'Sent to vendor'; the PO history says which sheet. The vendor portal shows the PO only after that."},
		{"PO-02", "Purchase order", "Amit Bansal", "Approve an Electrical request from BrightVolt (V007, terms Advance): the Approve box asks the advance. Pick 50% and a reason, approve.", "PO is NOT raised yet: 'PO waiting for advance approval'; the advance waits for Finance. Approving without an advance is refused."},
		{"PO-03", "Advance", "Rohan Mehra", "Payments › Advances: approve the BrightVolt advance.", "The PO is raised now under its held number and sent to the vendor."},
		{"PO-04", "Advance", "Rohan Mehra", "Mark the advance paid (UTR, date).", "Advance shows Paid; vendor emailed."},
		{"PO-05", "Advance", "Amit Bansal › Rohan Mehra", "Another Advance-terms request; Finance rejects the advance.", "PO not raised; the request stays approved with its PO pending (the Admin can issue it, or set Flow Settings back to a Purchase step)."},
		{"PO-06", "Approvals", "Amit Bansal", "Approvals: tick several requests (one with PVC pipe, estimated rate) and Approve selected.", "Each approved one gets its PO; the PVC pipe one is refused: open it to confirm the rate."},
		{"PO-07", "Purchase order", "Amit Bansal", "Open a request with PVC pipe (estimated rate): the Approve box asks its rate. Enter the agreed rate, Approve and raise PO.", "PO goes out at that rate; 'Rate confirmed' is logged."},
		// goods receipt
		{"GR-01", "Goods receipt", "Rahul Yadav", "Open PO_TA_0001, receive everything with invoice no., date, amount, photo.", "GRN_TA_0001 saved, voucher PV_0001; the PO moves to invoice match."},
		{"GR-02", "Goods receipt", "Rahul Yadav", "On another PO receive part, keep the balance open; later receive the rest.", "GRN _01 and _02, one PO stays open across deliveries."},
		{"GR-03", "Goods receipt", "Rahul Yadav", "On a PO: 'Vendor won't deliver the rest' › reject the balance with a reason.", "Balance rejected; vendor emailed; budget released."},
		{"GR-04", "Goods receipt", "Rahul Yadav", "Fill the receive form, Save draft, reopen the PO.", "The draft refills the form, invoice photo included."},
		{"GR-05", "Goods receipt", "Rahul Yadav", "Receive the BrightVolt PO (advance approved and paid, PO-04).", "Saved; the invoice can go on to payment."},
		{"GR-06", "Goods receipt", "Riya Sharma", "Open her own PO at " + P("P01") + " (one she raised) under Goods Receipts and receive it.", "It is her step: her Goods Receipts badge counts it and the PO page says the next step is with her (Flow Settings: goods are received by the person who raised the request). Rahul, who has the Receiving / Store role at P01, can still receive it; she cannot reject the balance alone."},
		{"GR-07", "Goods receipt", "Rahul Yadav", "Receive less than due. Look at 'Less than due arrived', then save.", "It starts on 'Nothing more is coming: close the PO short'. After saving, a popup 'No more items needed' says the PO is closed short. (Pick 'More is coming' to keep the balance open instead.)"},
		{"GR-08", "Goods receipt", "Rahul Yadav › Kavya Joshi", "Receive a PO with one quantity lowered. As Kavya, Invoices › send it back to the store. As Rahul, open the PO again.", "The receive form comes back with Rahul's quantities, invoice number, date, amount, invoice file and photos, and Kavya's reason on top."},
		{"GR-09", "Goods receipt", "Rahul Yadav", "Save a receipt as draft, then open Goods Receipts.", "'Your saved goods receipts, not submitted yet' lists it; Open goes to its PO with the draft filled in."},
		// automatic PO, daily consumption
		{"AU-01", "Flow Settings", "Admin", "Flow Settings › untick 'The final approval raises the PO'. Approve a request as Amit.", "It stays 'Approved, PO pending' for the Purchase Team (Priya issues it). Only Stationery (ticked in Item Setup) still gets its PO at approval. Tick the setting again afterwards."},
		{"AU-02", "Vendor order sheets", "Neeraj Gupta", "Vendor Order Sheets: open PaperPro, Send by email.", "The Cluster Manager (an approver) can send it; a store user cannot."},
		{"DC-01", "Daily consumption", "Arun Verma", "New request at " + P("P01") + ": tap Extension board (" + code("Extension board") + "). In the cart, open its vendor list.", "Tile says '2 vendors · pick in the cart'; the cart offers BrightVolt and FlowLine with their rates. Pick FlowLine: the request goes to FlowLine."},
		{"DC-02", "Daily consumption", "Admin", "Items › Extension board › Edit › Daily consumption: Yes.", "Refused while two vendors hold it; a daily item has one vendor at a property."},
		// vendor portal
		{"VP-01", "Vendor portal", "Admin", "Vendors › V001 › Edit › Vendor portal › Give sign-in.", "A one-time password is shown once (and emailed when email is set up)."},
		{"VP-02", "Vendor portal", "V001 (vendor)", "In a private window open /vendor, sign in with that email and password.", "Asked to choose a new password, then sees its POs; 'New, to accept' on fresh ones."},
		{"VP-03", "Vendor portal", "V001 (vendor)", "Open a new PO › Accept with a dispatch date. Then upload an invoice for it.", "On the PO page staff see 'Awaiting delivery, dispatch <date>' and the uploaded invoice; Rahul's receive form starts from it."},
		{"VP-04", "Vendor portal", "V001 (vendor) › Amit Bansal", "Decline another PO with a reason. As Amit (who approved it): Purchase Orders badge, open it, 'Vendor confirmed by phone or email'.", "Shows 'Declined by vendor' with the reason; Amit confirms with what was agreed and it awaits delivery."},
		{"VP-05", "Vendor portal", "V001 (vendor)", "Payments tab.", "Each invoice with where it stands (being checked, approved with pay-by date, paid with reference) and advances."},
		{"VP-06", "Vendor portal", "Admin", "Vendor portal › Turn off; refresh the vendor's window.", "The vendor is signed out and cannot sign in again."},
		// invoices and payments
		{"IN-01", "Invoice match", "Kavya Joshi", "Invoices: confirm a match within 2%.", "Matched, sent to Finance."},
		{"IN-02", "Invoice match", "Kavya Joshi", "An invoice ₹500 above the goods received: accept the difference with a remark, or send it back to the store.", "Without a remark it is refused."},
		{"IN-03", "Payment", "Rohan Mehra", "Payments: approve payment of a matched invoice.", "Pay-by date from the terms; MSME vendors (V001, V004, V005, V009, V017) capped at 45 days."},
		{"IN-04", "Payment", "Rohan Mehra", "Approve the BrightVolt invoice (paid advance).", "Advance adjusted: net payable = amount − advance."},
		{"IN-05", "Payment", "Rohan Mehra", "On a PO with an advance approved but not paid: try to approve the invoice payment.", "Refused: 'waits for the advance … approved but not paid yet'."},
		{"IN-06", "Payment", "Rohan Mehra", "A service invoice (AC servicing, TDS 194C 2%).", "TDS shows on the invoice; net payable = amount − TDS."},
		{"IN-07", "Payment", "Admin", "Phase 1 only: mark a payment-approved invoice paid with a reference.", "Paid; the document closes once all its invoices are paid."},
		// bills
		{"BL-01", "Utility bill", "Arun Verma", "+ Bill › Electricity at P01, vendor Haryana Power, this month, invoice, file.", "BR- number; goes to the Cluster Manager (or Cost Controller per Flow Settings)."},
		{"BL-02", "Utility bill", "Arun Verma", "Enter the same Electricity bill for the same month again.", "Refused: a bill already covers this period."},
		{"BL-03", "AMC bill", "Sameer Khan", "AMC bill: Lift AMC at P05, 12 months.", "Period shown as 12 months; approved by the Cluster Manager."},
		{"BL-04", "Utility bill", "Arun Verma", "Open BL-01, Clone.", "New bill with the next month filled in."},
		// corporate
		{"CO-01", "Corporate", "Meera Kapoor", "+ Corporate purchase › Admin department, PaperPro, 2 lines with expense heads, ₹18,000.", "Goes to Rakesh Jain (L1) then Anil Sethi (L2)."},
		{"CO-02", "Corporate", "Meera Kapoor", "Corporate purchase of ₹6,000.", "L1 alone is enough below ₹10,000."},
		{"CO-03", "Corporate", "Rakesh Jain", "Raise a corporate purchase in his own department.", "Skips L1 (he is the L1) and goes straight to L2."},
		{"CO-04", "Corporate", "Meera Kapoor", "+ Corporate bill with invoice, Save draft, reopen from the Corporate list.", "Draft restored with the invoice file."},
		// warehouse
		{"WH-01", "Warehouse", "Riya Sharma", "Request Bed sheet (" + code("Bed sheet double, white") + ") at P01.", "Becomes a warehouse transfer WT_TA_… (the Central Warehouse is mapped for it at P01–P04)."},
		{"WH-02", "Warehouse", "Vikram Singh", "Warehouse › To dispatch: schedule the delivery (partner, date, slot), then dispatch.", "Dispatch is refused until scheduled; then In transit."},
		{"WH-03", "Warehouse", "Rahul Yadav", "Receive the transfer one short.", "Shortage booked as pilferage; stock ledger updated."},
		{"WH-04", "Warehouse", "Arun Verma › Vikram Singh", "Request A4 paper × 20 at P01 (Riya cannot: her role is limited to Housekeeping); dispatch.", "Only 12 in stock: dispatch cannot exceed stock; dashboard shows it below reorder level."},
		{"WH-05", "Warehouse", "Ajay Thakur › Vikram Singh", "As Ajay (Warehouse Staff) raise a stock order for the warehouse (P08) from V002 Linen. Approve as Vikram.", "Goes to the Warehouse Manager (Vikram), not the Pre-opening Team; his approval raises the PO. Vikram himself cannot raise at the warehouse; Ajay receives the goods."},
		{"WH-07", "Warehouse", "Riya Sharma › Amit Bansal › Vikram Singh", "Order Bed sheet double × 600 at " + P("P01") + " (stocked at warehouse; the warehouse holds 420). Approve. Then Warehouse › Stock ledger.", "The request goes to the Central Warehouse, not a vendor. The ledger shows 600 ordered and 180 short; Vikram ticks it and Raise purchase for shortfall (his request goes to the Cost Controller). He sends the 420 now; the rest stays open on the property's request until it arrives."},
		{"CH-01", "Kitchen", "Karan Kapoor", "+ Purchase request at " + P("P01") + ".", "Property Chef: only Food & Beverage items are offered (his role is limited to F&B; untick it on his user to give all items). The request follows the usual route."},
		{"CH-02", "Kitchen", "Nisha Pillai", "Open Purchase Requests, Purchase Orders and a request at P01.", "Head Chef: sees everything at the Gurugram properties, cannot raise or approve anything."},
		{"WH-06", "Pre-opening", "Vikram Singh › Anjali Rao", "Raise for " + P("P09") + " (pre-opening).", "No budget check; approved by the Pre-opening Team."},
		// masters, roles, reports
		{"FA-01", "Finance approval", "Admin › Rohan Mehra", "Item Setup: add a sub-category and an item under it. Try to order the item; then, as Rohan, Masters to approve.", "The item shows 'Waits for Finance approval' and cannot be ordered; its rates and mapping can be prepared. Rohan sees both (the item says 'Approve the sub-category first'); approving them makes the item orderable and sends it (and the sub-category's new expense account) to Zoho."},
		{"FA-02", "Finance approval", "Admin › Rohan Mehra", "Add a property; open a new request there. Then Rohan rejects a new category with a reason.", "The property is not offered until Finance approves it; the rejected category shows as rejected with the reason."},
		{"MS-01", "Users & Rights", "Admin", "Roles tab: set Receiving / Store › Goods Receipts to View. Sign in as Rahul Yadav.", "Rahul can open Goods Receipts but not receive (and the server refuses it)."},
		{"MS-02", "Users & Rights", "Admin", "Give Finance 'Can add users' with role Property Staff. As Rohan, + Add user at P05.", "Rohan adds staff only at properties inside his access; sees only users he added."},
		{"MS-03", "Users & Rights", "Admin", "Access by property: give Mohan Lal Receiving / Store at P04.", "Mohan can now receive at P04."},
		{"MS-04", "Vendor Mapping", "Admin", "Check a property: " + P("P07") + ".", "Beverages show as not mapped; everything else has a vendor."},
		{"MS-05", "Masters", "Admin", "Mark V006 Fizz inactive (see the impact text), then raise a beverage request at P01.", "Beverages no longer orderable; mark active again."},
		{"MS-06", "Bulk upload", "Admin", "Download current data on any bulk upload, change one row, upload.", "Only the changed row is updated; the rest show No change."},
		{"RP-01", "Reports", "Amit Bansal", "Reports: Budget against actual, Purchase register (PR/PO/GRN/Invoice/Voucher), Download report pack.", "Figures match the scenarios above; the pack downloads as Excel."},
		{"DB-01", "Dashboard", "Each role", "Open the dashboard as Riya, Amit, Rohan, Vikram.", "Each sees their own tiles and 'Needs attention' alerts."},
		{"MO-01", "Phone", "Riya Sharma", "Do PR-01 and GR-01 on a phone.", "Tiles two to a row, cart bar at the bottom, nothing wider than the screen."},
	}
	refused := [][]string{
		{"Cities & Clusters › + Add city", "State code ZZ", "Refused: not a real state code"},
		{"Item Setup › + Add sub-category", "Code PO", "Refused: letters used for document numbers"},
		{"Item Setup › Pack sizes › Bulk upload", "Row 'BOX-OF-500'", "Refused: already exists as box of 500"},
		{"Properties › Edit P01", "Change the document code after a request was raised", "Refused: fixed once used"},
		{"Vendors › Onboard vendor", "PAN of an existing vendor (e.g. " + vendors[0].pan + ")", "Refused: duplicate PAN"},
		{"Approvals (as Riya Sharma, staff)", "Open Approvals", "No access: staff do not approve"},
		{"Issue PO", "Advance terms with no advance entered", "Refused: enter the advance to ask Finance for"},
		{"Payments", "Approve an invoice whose PO advance is still pending", "Refused: waits for the advance"},
		{"Goods receipt", "Invoice number already used for the same vendor", "Refused"},
		{"Goods receipt (as Riya Sharma)", "Receive a PO raised by someone else", "Not offered, and the server refuses it"},
		{"Vendor portal", "Sign in to /vendor with a staff email and password", "Refused: the portal has its own sign-in"},
		{"Items › Extension board", "Daily consumption back to Yes while two vendors hold it", "Refused"},
		{"Utility bill", "Second bill for the same property, type and month", "Refused"},
		{"Warehouse › Dispatch", "More than stock on hand", "Refused"},
		{"Users & Rights (as a role manager)", "Give a screen level above your own", "Refused: at most your own level"},
		{"Bulk upload › Vendors", "A row with bad GSTIN or IFSC", "Row shown with the error; nothing saved from it"},
	}
	return file{"00-test-plan.xlsx", []spreadsheet.Sheet{
		sheet("Load order", []string{"Step", "File", "Where to upload", "Signed in as", "What to expect"}, load),
		sheet("Manual setup", []string{"Part", "Where", "What to enter"}, manual),
		sheet("Logins", []string{"Name", "Signs in with", "Password", "Role", "Property access", "Category limit"}, logins),
		sheet("Test scenarios", []string{"ID", "Area", "Signed in as", "Steps", "Expected result", "Result (pass / fail)", "Notes"}, padRows(sc, 7)),
		sheet("Should be refused", []string{"Where", "Try", "Expected"}, refused),
	}}
}

func padRows(rows [][]string, n int) [][]string {
	for i := range rows {
		for len(rows[i]) < n {
			rows[i] = append(rows[i], "")
		}
	}
	return rows
}
