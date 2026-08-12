package commands

import "sync"

// The submit guard allows at most ONE concurrent Submit Scores run per tenant
// (the OCR is the expensive part - a burst of concurrent submissions from one
// server could otherwise monopolize the process). A plain in-memory map
// suffices: the bot is a single process. Different tenants never block each
// other.
var submitInFlightMu sync.Mutex
var submitInFlight = map[string]bool{}

// tryAcquireSubmit reserves the tenant's submission slot. It returns false
// when a submission is already being processed for that tenant; on true the
// caller MUST releaseSubmit when done.
func tryAcquireSubmit(tenantID string) bool {
	submitInFlightMu.Lock()
	defer submitInFlightMu.Unlock()
	if submitInFlight[tenantID] {
		return false
	}
	submitInFlight[tenantID] = true
	return true
}

// releaseSubmit frees the tenant's submission slot.
func releaseSubmit(tenantID string) {
	submitInFlightMu.Lock()
	defer submitInFlightMu.Unlock()
	delete(submitInFlight, tenantID)
}

// submitBusyMessage is the ephemeral reply for the second concurrent run.
const submitBusyMessage = "A submission is already being processed for this server - try again in a moment."
