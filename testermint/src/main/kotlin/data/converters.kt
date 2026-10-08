package com.productscience.data

import com.google.gson.JsonDeserializationContext
import com.google.gson.JsonDeserializer
import com.google.gson.JsonElement
import com.google.gson.JsonParseException
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


class IntDeserializer : JsonDeserializer<Int> {
    override fun deserialize(
        json: JsonElement,
        typeOfT: Type?,
        context: JsonDeserializationContext?,
    ): Int? {
        if (json.asString == "") return null
        return try {
            json.asString.replace("_", "").toInt()
        } catch (e: NumberFormatException) {
            try {
                // Handle "5000.0" case
               json.asDouble.toInt()
            } catch (e2: Exception) {
               // Fallback to original parsing (w/o replace) or just let it throw
               json.asInt
            }
        }
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

/**
 * Converts fee-rule oneofs returned by the CLI's Amino JSON encoder back to
 * protobuf JSON before Testermint writes Params into a governance proposal.
 *
 * Amino emits a selected rule as:
 *   "Func": {"type": "...", "value": {"stored_delta": {...}}}
 * while the proposal parser expects:
 *   "stored_delta": {...}
 */
class FeeParamsDataSerializer : JsonSerializer<FeeParamsData> {
    override fun serialize(
        src: FeeParamsData,
        typeOfSrc: Type,
        context: JsonSerializationContext,
    ): JsonElement {
        return com.google.gson.JsonObject().apply {
            add("min_gas_price_ngonka", context.serialize(src.minGasPriceNgonka))
            add("base_validation_gas", context.serialize(src.baseValidationGas))
            add("gas_per_poc_count", context.serialize(src.gasPerPocCount))
            add("enabled_fee_groups", context.serialize(src.enabledFeeGroups))
            src.groups?.let { add("groups", feeGroupsForProtoJson(it)) }
        }
    }
}

internal fun feeGroupsForProtoJson(groups: com.google.gson.JsonArray): com.google.gson.JsonArray {
    val normalized = groups.deepCopy()

    normalized.forEach groupLoop@{ groupElement ->
        if (!groupElement.isJsonObject) return@groupLoop
        val messages = groupElement.asJsonObject.getAsJsonArray("msgs") ?: return@groupLoop

        messages.forEach messageLoop@{ messageElement ->
            if (!messageElement.isJsonObject) return@messageLoop
            val message = messageElement.asJsonObject
            val upperWrapper = message.remove("Func")
            val lowerWrapper = message.remove("func")
            if (upperWrapper != null && lowerWrapper != null) {
                throw JsonParseException("fee rule contains both Func and func oneof wrappers")
            }

            val wrapper = upperWrapper ?: lowerWrapper ?: return@messageLoop
            if (!wrapper.isJsonObject) {
                throw JsonParseException("fee rule oneof wrapper must be an object")
            }
            val value = wrapper.asJsonObject.get("value")
            if (value == null || !value.isJsonObject || value.asJsonObject.entrySet().isEmpty()) {
                throw JsonParseException("fee rule oneof wrapper must contain an object value")
            }

            value.asJsonObject.entrySet().forEach { (fieldName, fieldValue) ->
                val existing = message.get(fieldName)
                if (existing != null && existing != fieldValue) {
                    throw JsonParseException("fee rule contains conflicting $fieldName values")
                }
                message.add(fieldName, fieldValue.deepCopy())
            }
        }
    }

    return normalized
}

class ConfirmationPoCPhaseDeserializer : JsonDeserializer<ConfirmationPoCPhase> {
    override fun deserialize(
        json: JsonElement,
        typeOfT: Type?,
        context: JsonDeserializationContext?
    ): ConfirmationPoCPhase {
        if (json.isJsonPrimitive && json.asJsonPrimitive.isString) {
            val str = json.asString
            return try {
                ConfirmationPoCPhase.valueOf(str)
            } catch (e: Exception) {
                // Try numeric string
                val num = str.toIntOrNull()
                if (num != null) {
                    ConfirmationPoCPhase.values().find { it.value == num }
                        ?: throw IllegalArgumentException("Unknown ConfirmationPoCPhase value: $num")
                } else {
                    throw e
                }
            }
        }
        val intValue = json.asInt
        return ConfirmationPoCPhase.values().find { it.value == intValue }
            ?: throw IllegalArgumentException("Unknown ConfirmationPoCPhase value: $intValue")
    }
}

class InferenceStatusDeserializer : JsonDeserializer<InferenceStatus> {
    override fun deserialize(
        json: JsonElement,
        typeOfT: Type?,
        context: JsonDeserializationContext?
    ): InferenceStatus {
        return InferenceStatus.fromAny(
            if (json.isJsonPrimitive && json.asJsonPrimitive.isString) {
                json.asString
            } else if (json.isJsonPrimitive && json.asJsonPrimitive.isNumber) {
                json.asInt
            } else {
                null
            }
        )
    }
}

class DevshardInferenceStatusDeserializer : JsonDeserializer<DevshardInferenceStatus> {
    override fun deserialize(
        json: JsonElement,
        typeOfT: Type?,
        context: JsonDeserializationContext?
    ): DevshardInferenceStatus {
        return DevshardInferenceStatus.fromAny(
            when {
                !json.isJsonPrimitive -> null
                json.asJsonPrimitive.isNumber -> json.asInt
                json.asJsonPrimitive.isString -> json.asString
                else -> null
            }
        )
    }
}

class ProposalStatusDeserializer : JsonDeserializer<ProposalStatus> {
    override fun deserialize(
        json: JsonElement,
        typeOfT: Type?,
        context: JsonDeserializationContext?
    ): ProposalStatus {
        return ProposalStatus.fromAny(
            if (json.isJsonPrimitive && json.asJsonPrimitive.isString) {
                json.asString
            } else if (json.isJsonPrimitive && json.asJsonPrimitive.isNumber) {
                json.asInt
            } else {
                null
            }
        )
    }
}
