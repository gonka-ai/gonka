package types

const (
	// LegacyMsgStartInferenceTypeURL is the marker warm keys were identified by
	// before v0.2.15. The message no longer exists, so authz MsgGrant rejects
	// this type URL and no new grant can carry it. Existing grants are left in
	// place; nothing revokes them.
	LegacyMsgStartInferenceTypeURL = "/inference.inference.MsgStartInference"

	// WarmKeyGrantMarkerTypeURL identifies a cold->warm ML ops pair. Unlike the
	// legacy marker this is a live permission, so revoking it also de-warms the
	// key for devshard. It also inherits the 1-year expiration that
	// GrantMLOperationalKeyPermissionsToAccount sets, and GranteesByMessageType
	// filters expired grants, so a lapsed host reads as not-warm until it
	// re-runs grant-ml-ops-permissions.
	WarmKeyGrantMarkerTypeURL = "/inference.inference.MsgClaimRewards"
)
