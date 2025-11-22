package com.productscience.data

import com.google.gson.JsonDeserializationContext
import com.google.gson.JsonDeserializer
import com.google.gson.JsonElement
import com.google.gson.JsonPrimitive
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

        return Duration.parse("PT${durationString}")
    }
}

class LongSerializer: JsonSerializer<java.lang.Long> {
    override fun serialize(
        src: java.lang.Long?,
        typeOfSrc: Type?,
        context: com.google.gson.JsonSerializationContext,
    ): JsonElement {
        return com.google.gson.JsonPrimitive(src?.toString())
    }
}

class LongDeserializer : JsonDeserializer<java.lang.Long> {
    override fun deserialize(
        json: JsonElement,
        typeOfT: Type?,
        context: JsonDeserializationContext?,
    ): java.lang.Long? {
        if (json.asString == "") return null
        return java.lang.Long.parseLong(json.asString.replace("_", "")) as java.lang.Long?
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

class DurationSerializer : JsonSerializer<Duration> {
    override fun serialize(
        src: Duration?,
        typeOfSrc: Type?,
        context: JsonSerializationContext
    ): JsonElement {
        if (src == null) return JsonPrimitive("")

        return when {
            src.isZero -> JsonPrimitive("0s")
            src.toDays() > 0 && src.toHours() % 24 == 0L && src.toMinutes() % 60 == 0L && src.toSeconds() % 60 == 0L ->
                JsonPrimitive("${src.toDays()}d")
            src.toHours() > 0 && src.toMinutes() % 60 == 0L && src.toSeconds() % 60 == 0L ->
                JsonPrimitive("${src.toHours()}h")
            src.toMinutes() > 0 && src.toSeconds() % 60 == 0L ->
                JsonPrimitive("${src.toMinutes()}m")
            else ->
                JsonPrimitive("${src.toSeconds()}s")
        }
    }
}

class MessageSerializer(val namingPolicy: com.google.gson.FieldNamingPolicy) : JsonSerializer<TxMessage> {
    override fun serialize(
        src: TxMessage,
        typeOfSrc: Type,
        context: JsonSerializationContext
    ): JsonElement? {
        val jsonObject = com.google.gson.JsonObject()
        jsonObject.add("@type", context.serialize(src.type))

        src::class.java.declaredFields.forEach { field ->
            field.isAccessible = true
            if (field.name != "type") {
                val value = field.get(src)
                val fieldName = if (namingPolicy == com.google.gson.FieldNamingPolicy.LOWER_CASE_WITH_UNDERSCORES) {
                    camelToSnake(field.name)
                } else field.name
                jsonObject.add(fieldName, context.serialize(value))
            }
        }

        return jsonObject
    }
}

class ConfirmationPoCPhaseDeserializer : JsonDeserializer<ConfirmationPoCPhase> {
    override fun deserialize(
        json: JsonElement,
        typeOfT: Type?,
        context: JsonDeserializationContext?
    ): ConfirmationPoCPhase {
        val intValue = json.asInt
        return ConfirmationPoCPhase.values().find { it.value == intValue }
            ?: throw IllegalArgumentException("Unknown ConfirmationPoCPhase value: $intValue")
    }
}
