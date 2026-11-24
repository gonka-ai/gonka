import com.google.gson.Gson
import com.productscience.cosmosJson
import com.productscience.data.DevshardHostStatsEntry
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

class DevshardAssignedSerializationTest {
    @Test
    fun `settlement host stats retain assigned across CLI JSON roundtrip`() {
        val wire = """{"slot_id":1,"missed":1,"invalid":0,"cost":10,"assigned":3,"validated":1,"finished":2}"""
        val parsed = cosmosJson.fromJson(wire, DevshardHostStatsEntry::class.java)
        val reserialized = Gson().toJson(parsed)
        val roundTrip = cosmosJson.fromJson(reserialized, DevshardHostStatsEntry::class.java)
        assertThat(roundTrip.assigned).isEqualTo(3L)
        assertThat(roundTrip.validated).isEqualTo(1)
        assertThat(roundTrip.finished).isEqualTo(2)
    }
}
