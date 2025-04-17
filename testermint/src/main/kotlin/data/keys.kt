package com.productscience.data

import com.google.gson.JsonDeserializationContext
import com.google.gson.annotations.SerializedName
import com.google.gson.*
import java.lang.reflect.Type

data class Validator(
    val name: String,
    val type: String,
    val address: String,
    val pubkey: Pubkey2
)

public data class Pubkey2(
    @field:SerializedName("@type") val type: String,
    val key: String
)

class Pubkey2Deserializer : JsonDeserializer<Pubkey2> {
    override fun deserialize(json: JsonElement, typeOfT: Type, context: JsonDeserializationContext): Pubkey2 {
        val jsonObject = json as? JsonObject ?: JsonParser.parseString(json.asString).asJsonObject
        val type = jsonObject.get("@type").asString
        val key = jsonObject.get("key").asString
        return Pubkey2(type, key)
    }
}



