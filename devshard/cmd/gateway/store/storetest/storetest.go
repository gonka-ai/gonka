// Package storetest is the contract suite every store backend runs, and the benchmarks that compare them.
package storetest

import (
	"testing"

	"devshard/cmd/gateway/store"
)

// Opener opens a store on the database its Factory prepared; each call opens the same database again.
type Opener func() store.Store

// Factory prepares a fresh, empty database for one test and returns the Opener for it.
type Factory func(tb testing.TB) Opener

type contractCase struct {
	name string
	run  func(t *testing.T, open Opener)
}

var contractCases = []contractCase{
	{"CommitmentSaveLoadDeleteRoundTrip", commitmentSaveLoadDeleteRoundTrip},
	{"SaveCommitmentUpsertReplacesNotDuplicates", saveCommitmentUpsertReplacesNotDuplicates},
	{"LoadCommitmentsDeterministicOrder", loadCommitmentsDeterministicOrder},
	{"DeleteCommitmentAbsentIsNoOp", deleteCommitmentAbsentIsNoOp},
	{"LoadCommitmentsAcceptsARowWithNoCreatedAt", loadCommitmentsAcceptsARowWithNoCreatedAt},
	{"Commitments", commitments},
	{"CompletionJournal", completionJournal},
	{"DevshardCRUDLifecycle", devshardCRUDLifecycle},
	{"UpsertDevshardKeepsAQueuedSettlementWhileReplacingEveryOtherField", upsertDevshardKeepsAQueuedSettlementWhileReplacingEveryOtherField},
	{"DevshardReadsOneRowAsTheListDoes", devshardReadsOneRowAsTheListDoes},
	{"MissingDevshardReturnsSentinel", missingDevshardReturnsSentinel},
	{"ParkingForSettlementSetsBothFieldsAtOnce", parkingForSettlementSetsBothFieldsAtOnce},
	{"ParkingAnUnknownEscrowIsReported", parkingAnUnknownEscrowIsReported},
	{"ParkingIfActiveParksAServingEscrow", parkingIfActiveParksAServingEscrow},
	{"ParkingIfActiveReportsNoParkForAnUnknownEscrow", parkingIfActiveReportsNoParkForAnUnknownEscrow},
	{"ParkingIfActiveLeavesAnEscrowAlreadyOutOfServiceUntouched", parkingIfActiveLeavesAnEscrowAlreadyOutOfServiceUntouched},
	{"TheChainFactsSurviveARegistrationAndAZeroKeepsItsColumn", theChainFactsSurviveARegistrationAndAZeroKeepsItsColumn},
	{"MarkingARowGoneFromChainTakesItOutOfServiceInOneWrite", markingARowGoneFromChainTakesItOutOfServiceInOneWrite},
	{"ReactivatingARowGoneFromChainClearsTheMark", reactivatingARowGoneFromChainClearsTheMark},
	{"TheSettleStampIsRecordedAndClearedWithTheHash", theSettleStampIsRecordedAndClearedWithTheHash},
	{"ActivatingARowParkedAfterItsCheckLeavesItParked", activatingARowParkedAfterItsCheckLeavesItParked},
	{"ActivatingARowWithASettleHashLeavesItInactive", activatingARowWithASettleHashLeavesItInactive},
	{"ActivatingAnUnparkedRowActivatesIt", activatingAnUnparkedRowActivatesIt},
	{"RegisteringOverARowParkedAfterItsCheckLeavesItParked", registeringOverARowParkedAfterItsCheckLeavesItParked},
	{"RegisteringANewOrUnparkedRowWritesIt", registeringANewOrUnparkedRowWritesIt},
	{"ReRegisteringAnEscrowKeepsItsSettleTransaction", reRegisteringAnEscrowKeepsItsSettleTransaction},
	{"DevshardRegistry", devshardRegistry},
	{"AnOwedVoteOutlivesTheStoreUntilItIsPaid", anOwedVoteOutlivesTheStoreUntilItIsPaid},
	{"LedgerWritesEveryFieldItWasGiven", ledgerWritesEveryFieldItWasGiven},
	{"LedgerWritesExactlyOneRowPerRequestID", ledgerWritesExactlyOneRowPerRequestID},
	{"LedgerFindReportsUnknownAndBlankIDs", ledgerFindReportsUnknownAndBlankIDs},
	{"RetentionEvictsRowsPastTheMaxAge", retentionEvictsRowsPastTheMaxAge},
	{"RetentionEvictsTheOldestRowsPastMaxRows", retentionEvictsTheOldestRowsPastMaxRows},
	{"StoredTimestampsSortInTimeOrder", storedTimestampsSortInTimeOrder},
	{"LedgerKeepsTheNewestRows", ledgerKeepsTheNewestRows},
	{"OverridesRoundTripAndEmptyLoad", overridesRoundTripAndEmptyLoad},
	{"ModelFiltersUpsertListDelete", modelFiltersUpsertListDelete},
	{"RotationStatusUpsertByModelRoleReplaces", rotationStatusUpsertByModelRoleReplaces},
	{"RotationStatusDistinguishesRoleWithinSameModel", rotationStatusDistinguishesRoleWithinSameModel},
	{"LoadRotationStatusesDeterministicOrder", loadRotationStatusesDeterministicOrder},
	{"SuspiciousHostsSurviveAReopen", suspiciousHostsSurviveAReopen},
	{"SuspiciousHostsListInByteOrder", suspiciousHostsListInByteOrder},
	{"SuspiciousHostsToleratesRepeatedPinsAndUnknownUnpins", suspiciousHostsToleratesRepeatedPinsAndUnknownUnpins},
	{"AnAPIKeySurvivesAReopenWithItsRevokedState", anAPIKeySurvivesAReopenWithItsRevokedState},
	{"AnAPIKeyIDIsNeverReusedAndRevokingKeepsTheFirstTime", anAPIKeyIDIsNeverReusedAndRevokingKeepsTheFirstTime},
	{"OverridesKeepPerModelSettingsAndLegacyKeysAcrossAReopen", overridesKeepPerModelSettingsAndLegacyKeysAcrossAReopen},
	{"SmallTables", smallTables},
}

// Run runs the contract suite, one fresh database per case.
func Run(t *testing.T, factory Factory) {
	for _, testCase := range contractCases {
		t.Run(testCase.name, func(t *testing.T) { testCase.run(t, factory(t)) })
	}
}
