//go:build devnet

package constants

// The devnet build shortens the administrator and soft delays so a
// two-step Settlement admin call completes in minutes. It also
// shortens the minimum withdrawal delay, so a domain registered at the
// minimum delay finalizes its batches about 5 minutes after
// acceptance. Never run a devnet build on any other network.
func init() {
	MinAdministratorDelay = 12
	MinSoftDelay = 6
	WithdrawalDelayMin = 30
}
