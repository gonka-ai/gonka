package com.productscience.data

import com.google.gson.annotations.SerializedName

// -----------------------
// Maintenance Query Response Types
// -----------------------

data class MaintenanceCreditResponse(
    @SerializedName("credit_blocks")
    val creditBlocks: Long = 0,
    val found: Boolean = false,
)

data class MaintenanceScheduledResponse(
    val reservation: MaintenanceReservation? = null,
    val found: Boolean = false,
)

data class MaintenanceActiveResponse(
    val reservations: List<MaintenanceReservation> = emptyList(),
)

data class MaintenanceStatusResponse(
    val state: MaintenanceState? = null,
    @SerializedName("active_reservation")
    val activeReservation: MaintenanceReservation? = null,
    @SerializedName("scheduled_reservation")
    val scheduledReservation: MaintenanceReservation? = null,
    val found: Boolean = false,
)

data class MaintenanceSchedulabilityResponse(
    val schedulable: Boolean = false,
    @SerializedName("rejection_reason")
    val rejectionReason: String = "",
)

data class MaintenanceConcurrencyResponse(
    @SerializedName("concurrent_count")
    val concurrentCount: Int = 0,
    @SerializedName("concurrent_power_bps")
    val concurrentPowerBps: Long = 0,
)

// -----------------------
// Maintenance State Types
// -----------------------

data class MaintenanceReservation(
    @SerializedName("reservation_id")
    val reservationId: Long = 0,
    val participant: String = "",
    @SerializedName("start_height")
    val startHeight: Long = 0,
    @SerializedName("duration_blocks")
    val durationBlocks: Long = 0,
    @SerializedName("created_by")
    val createdBy: String = "",
    val status: String = "",
    @SerializedName("activation_warning")
    val activationWarning: String = "",
)

data class MaintenanceState(
    val participant: String = "",
    @SerializedName("credit_blocks")
    val creditBlocks: Long = 0,
    @SerializedName("last_maintenance_epoch")
    val lastMaintenanceEpoch: Long = 0,
    @SerializedName("active_reservation_id")
    val activeReservationId: Long = 0,
    @SerializedName("scheduled_reservation_id")
    val scheduledReservationId: Long = 0,
)
