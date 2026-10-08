// Package indiantax checks Indian tax and bank identifiers (GSTIN, PAN, IFSC, Udyam, HSN)
// and maps GST state codes, which decide CGST + SGST versus IGST.
package indiantax

import "regexp"

var (
	GSTIN  = regexp.MustCompile(`^\d{2}[A-Z]{5}\d{4}[A-Z][A-Z\d]Z[A-Z\d]$`)
	PAN    = regexp.MustCompile(`^[A-Z]{5}\d{4}[A-Z]$`)
	IFSC   = regexp.MustCompile(`^[A-Z]{4}0[A-Z\d]{6}$`)
	Udyam  = regexp.MustCompile(`^UDYAM-[A-Z]{2}-\d{2}-\d{7}$`)
	PIN    = regexp.MustCompile(`^\d{6}$`)
	Mobile = regexp.MustCompile(`^[6-9]\d{9}$`)
	Email  = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	Acct   = regexp.MustCompile(`^\d{9,18}$`)
	HSN    = regexp.MustCompile(`^\d{4,8}$`)
)

// Rates are the GST rates a SKU or bill may carry.
var Rates = []float64{0, 5, 12, 18, 28, 40}

func ValidRate(r float64) bool {
	for _, x := range Rates {
		if x == r {
			return true
		}
	}
	return false
}

var stateByGSTCode = map[string]string{
	"01": "JK", "02": "HP", "03": "PB", "04": "CH", "05": "UK", "06": "HR", "07": "DL", "08": "RJ", "09": "UP",
	"10": "BR", "11": "SK", "12": "AR", "13": "NL", "14": "MN", "15": "MZ", "16": "TR", "17": "ML", "18": "AS",
	"19": "WB", "20": "JH", "21": "OD", "22": "CG", "23": "MP", "24": "GJ", "26": "DN", "27": "MH", "29": "KA",
	"30": "GA", "31": "LD", "32": "KL", "33": "TN", "34": "PY", "35": "AN", "36": "TS", "37": "AP", "38": "LA",
}

// StateFromGSTIN returns the two-letter state code from the first two digits of a GSTIN.
func StateFromGSTIN(g string) string {
	if len(g) < 2 {
		return ""
	}
	return stateByGSTCode[g[:2]]
}
