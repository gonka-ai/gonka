package com.productscience.assertions

import com.productscience.data.TxResponse
import org.assertj.core.api.AbstractAssert

/**
 * Custom AssertJ assertion for [TxResponse].
 *
 * Usage examples:
 *  - import com.productscience.assertions.assertThat
 *    assertThat(txResponse).IsSuccess()
 */
class TxResponseAssert(actual: TxResponse) :
    AbstractAssert<TxResponseAssert, TxResponse>(actual, TxResponseAssert::class.java) {

    /**
     * Verifies that the transaction was successful (i.e., result code == 0).
     */
    fun isSuccess(): TxResponseAssert {
        isNotNull()
        if (actual.code != 0) {
            failWithMessage(
                "transaction failed - code=%d,rawLog=%s",
                actual.code,
                actual.rawLog
            )
        }
        return this
    }

    fun isFailure(): TxResponseAssert {
        isNotNull()
        if (actual.code == 0) {
            failWithMessage(
                "Transaction did not fail: rawLog=%s",
                actual.rawLog
            )
        }
        return this
    }
}

/** Factory function to start assertions for [TxResponse]. */
fun assertThat(actual: TxResponse): TxResponseAssert = TxResponseAssert(actual)
