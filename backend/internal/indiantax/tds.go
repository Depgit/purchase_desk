package indiantax

// TDS (tax deducted at source) on payments for services, under the Income Tax Act. The payer keeps back the
// rate on the value before GST (CBDT circular 23/2017) and pays the vendor the rest. Rates as of FY 2025-26.
// Yearly thresholds (for example ₹30,000 a bill under 194C) and lower-deduction certificates are not applied:
// Finance checks those before payment.

// TDSSection is one choice offered on a service item.
type TDSSection struct {
	Code  string  `json:"code"`
	Label string  `json:"label"`
	Pct   float64 `json:"pct"`
}

// TDSSections lists the sections a service item can use. "NONE" means the service has no TDS.
var TDSSections = []TDSSection{
	{"194C-IND", "194C Contractor, individual or HUF (1%)", 1},
	{"194C", "194C Contractor, company or firm (2%)", 2},
	{"194J-TECH", "194J Technical services (2%)", 2},
	{"194J-PROF", "194J Professional fees (10%)", 10},
	{"194I-PM", "194I Rent of plant or machinery (2%)", 2},
	{"194I-LB", "194I Rent of land or building (10%)", 10},
	{"194H", "194H Commission or brokerage (2%)", 2},
	{"NONE", "No TDS on this service", 0},
}

// TDSFor returns the section with this code.
func TDSFor(code string) (TDSSection, bool) {
	for _, s := range TDSSections {
		if s.Code == code {
			return s, true
		}
	}
	return TDSSection{}, false
}
