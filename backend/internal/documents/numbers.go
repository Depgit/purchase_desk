package documents

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"purchasedesk/internal/database"
	"purchasedesk/internal/httpjson"
)

// Request numbers carry the property's document code and its own count: PR_HZ_0001. When one submission splits into a
// request per vendor, each gets a suffix: PR_HZ_0001_01, PR_HZ_0001_02. The PO (or warehouse transfer) and GRN copy
// the request's number with their own prefix; see Derived. Older numbers (PR-1001) and corporate ones (CR-1001) keep
// the sequences in NextNumber.
var requestNumber = regexp.MustCompile(`^PR_[A-Z0-9]{2}_[0-9]{4,}(_[0-9]{2,})?$`)

// RequestNumbers reserves the next request number at a property: one number for a single vendor, or count suffixed
// numbers (_01, _02, ...) when the submission splits into count requests. It refuses until the property has a code.
func RequestNumbers(ctx context.Context, q database.Q, propertyID int64, count int) ([]string, error) {
	var code string
	var n int
	err := q.QueryRow(ctx, `UPDATE property SET doc_seq=doc_seq+1 WHERE id=$1 AND coalesce(doc_code,'')<>'' RETURNING doc_code, doc_seq`, propertyID).Scan(&code, &n)
	if err != nil {
		var name string
		if q.QueryRow(ctx, `SELECT name FROM property WHERE id=$1`, propertyID).Scan(&name) != nil {
			return nil, err
		}
		return nil, httpjson.Bad("%s has no document code yet. Ask the admin to set its 2-letter code under Settings → Properties: every PR, PO and GRN number starts with it.", name)
	}
	base := fmt.Sprintf("PR_%s_%04d", code, n)
	if count <= 1 {
		return []string{base}, nil
	}
	out := make([]string, count)
	for i := range out {
		out[i] = fmt.Sprintf("%s_%02d", base, i+1)
	}
	return out, nil
}

// IsRequestNumber reports whether number follows the property-coded format (PR_HZ_0001 or PR_HZ_0001_02).
func IsRequestNumber(number string) bool {
	return requestNumber.MatchString(number)
}

// RequestBase splits a request number into its base and split suffix: PR_HZ_0001_02 gives PR_HZ_0001 and 2,
// PR_HZ_0001 gives PR_HZ_0001 and 0.
func RequestBase(number string) (string, int) {
	parts := strings.Split(number, "_")
	if len(parts) == 4 {
		n, _ := strconv.Atoi(parts[3])
		return strings.Join(parts[:3], "_"), n
	}
	return number, 0
}

// NextSplits gives count more suffixed numbers under a request base, after the highest suffix already used:
// PR_HZ_0001_03 and _04 when _01 and _02 exist.
func NextSplits(ctx context.Context, q database.Q, base string, count int) ([]string, error) {
	var last int
	if err := q.QueryRow(ctx, `SELECT coalesce(max(substring(number from length($1)+2)::int),0) FROM document
		WHERE starts_with(number, $1||'_') AND substring(number from length($1)+2) ~ '^[0-9]+$'`, base).Scan(&last); err != nil {
		return nil, err
	}
	out := make([]string, count)
	for i := range out {
		out[i] = fmt.Sprintf("%s_%02d", base, last+i+1)
	}
	return out, nil
}

// Derived gives the number of a document made from a request, with its own prefix: PO_HZ_0001_01 for PR_HZ_0001_01,
// WT_HZ_0001 or GRN_HZ_0001 for PR_HZ_0001. A warehouse balance order (PR_HZ_0001_B1) gives WT_HZ_0001_B1. ok is
// false for an older or corporate number; the caller then uses NextNumber.
func Derived(number, prefix string) (string, bool) {
	if !strings.HasPrefix(number, "PR_") {
		return "", false
	}
	return prefix + strings.TrimPrefix(number, "PR"), true
}

// AdvanceNumber numbers the nth advance on a PO: ADV_HZ_0001_01 for PO_HZ_0001, ADV_HZ_0001_01_02 for the second
// advance on PO_HZ_0001_01. An older or corporate PO (PO-2001) gives ADV-2001_01.
func AdvanceNumber(po string, n int) string {
	if strings.HasPrefix(po, "PO_") {
		return fmt.Sprintf("ADV_%s_%02d", strings.TrimPrefix(po, "PO_"), n)
	}
	return fmt.Sprintf("ADV-%s_%02d", strings.TrimPrefix(po, "PO-"), n)
}
