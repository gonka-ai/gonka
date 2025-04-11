package com.productscience.data

import com.google.gson.JsonDeserializationContext
import com.google.gson.JsonDeserializer
import com.google.gson.JsonElement
import com.google.gson.JsonSerializationContext
import com.google.gson.JsonSerializer
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

class LongSerializer : JsonSerializer<java.lang.Long> {
    override fun serialize(
        src: java.lang.Long?,
        typeOfSrc: Type?,
        context: com.google.gson.JsonSerializationContext,
    ): JsonElement {
        return com.google.gson.JsonPrimitive(src?.toString())
    }
}

class DoubleSerializer: JsonSerializer<java.lang.Double> {
    override fun serialize(
        src: java.lang.Double?,
        typeOfSrc: Type?,
        context: com.google.gson.JsonSerializationContext,
    ): JsonElement {
        return com.google.gson.JsonPrimitive( src?.toDouble()?.toBigDecimal()?.toPlainString())
    }
}

class FloatSerializer: JsonSerializer<java.lang.Float> {
    override fun serialize(
        src: java.lang.Float?,
        typeOfSrc: Type?,
        context: com.google.gson.JsonSerializationContext,
    ): JsonElement {
        return com.google.gson.JsonPrimitive( src?.toDouble()?.toBigDecimal()?.toPlainString())
    }
}

class MessageSerializer : JsonSerializer<GovernanceMessage> {
    override fun serialize(
        src: GovernanceMessage,
        typeOfSrc: Type,
        context: JsonSerializationContext
    ): JsonElement? {
        val jsonObject = com.google.gson.JsonObject()
        jsonObject.addProperty("@type", src.type)

        src::class.java.declaredFields.forEach { field ->
            field.isAccessible = true
            if (field.name != "type") {
                val value = field.get(src)
                jsonObject.add(field.name, context.serialize(value))
            }
        }

        return jsonObject
    }
}