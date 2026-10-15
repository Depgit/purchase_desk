// Package zohowake lets a change that has something for Zoho (a PO issued, a bill approved for payment, an advance
// paid, a vendor verified, an item or property added) start the zoho-send job at once instead of at its next minute.
package zohowake

var kick = make(chan struct{}, 1)

// Now asks the zoho-send job to run soon. It never blocks; several asks before the run make one run.
func Now() {
	select {
	case kick <- struct{}{}:
	default:
	}
}

// C is what the job waits on besides its timer.
func C() <-chan struct{} { return kick }
