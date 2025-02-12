package com.productscience.data

import com.google.gson.JsonDeserializationContext
import com.google.gson.JsonDeserializer
import com.google.gson.JsonElement
import java.lang.reflect.Type
import java.time.Duration
import java.time.Instant


class InstantDeserializer : JsonDeserializer<Instant> {
    override fun deserialize(
        json: JsonElement,
        typeOfT: Type?,
        context: JsonDeserializationContext?,
    ): Instant? {
        if (json.asString == "") return null
        return Instant.parse(json.asString)
    }
}

class DurationDeserializer : JsonDeserializer<Duration> {
    override fun deserialize(json: JsonElement, typeOfT: Type?, context: JsonDeserializationContext?): Duration {
        val durationString = json.asString
        if (durationString.isBlank()) return Duration.ZERO

        return when {
            durationString.endsWith("s") -> Duration.ofSeconds(durationString.removeSuffix("s").toLong())
            durationString.endsWith("m") -> Duration.ofMinutes(durationString.removeSuffix("m").toLong())
            durationString.endsWith("h") -> Duration.ofHours(durationString.removeSuffix("h").toLong())
            durationString.endsWith("d") -> Duration.ofDays(durationString.removeSuffix("d").toLong())
            else -> throw IllegalArgumentException("Invalid duration format: $durationString")
        }
    }
}
