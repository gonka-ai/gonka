package com.productscience.data

import kotlin.reflect.*
import kotlin.reflect.full.primaryConstructor

// ---------------------
// PART 1: Generic parser for the custom format

class ProtoParser(private val input: String) {
    private var pos = 0

    private fun skipWhitespace() {
        while (pos < input.length && input[pos].isWhitespace()) {
            pos++
        }
    }

    // Parse an object: expects '{', then key-value pairs, then '}'
    fun parseObject(): Map<String, Any> {
        skipWhitespace()
        if (pos >= input.length || input[pos] != '{')
            throw IllegalArgumentException("Expected '{' at position $pos")
        pos++ // skip '{'
        skipWhitespace()

        val result = mutableMapOf<String, Any>()
        while (pos < input.length && input[pos] != '}') {
            val key = parseKey()
            skipWhitespace()
            if (pos >= input.length || input[pos] != ':')
                throw IllegalArgumentException("Expected ':' after key at position $pos")
            pos++ // skip ':'
            skipWhitespace()
            val value = parseValue()
            result[key] = value
            skipWhitespace()
        }
        if (pos >= input.length || input[pos] != '}')
            throw IllegalArgumentException("Expected '}' at position $pos")
        pos++ // skip '}'
        return result
    }

    // Simple key parser: accepts letters, digits, and underscores.
    private fun parseKey(): String {
        val start = pos
        while (pos < input.length && (input[pos].isLetterOrDigit() || input[pos] == '_')) {
            pos++
        }
        if (start == pos)
            throw IllegalArgumentException("Expected key at position $pos")
        return input.substring(start, pos)
    }

    // Decide whether the next value is a nested object or a number.
    private fun parseValue(): Any {
        skipWhitespace()
        return if (pos < input.length && input[pos] == '{') {
            parseObject()
        } else {
            parseNumber()
        }
    }

    // Parse a number (supports negative numbers and decimals).
    private fun parseNumber(): Number {
        val start = pos
        if (pos < input.length && input[pos] == '-') {
            pos++
        }
        while (pos < input.length && input[pos].isDigit()) {
            pos++
        }
        var isFloat = false
        if (pos < input.length && input[pos] == '.') {
            isFloat = true
            pos++
            while (pos < input.length && input[pos].isDigit()) {
                pos++
            }
        }
        val numberStr = input.substring(start, pos)
        return if (isFloat) numberStr.toDouble() else numberStr.toInt()
    }
}

fun parseCustomFormat(input: String): Map<String, Any> {
    val parser = ProtoParser(input)
    return parser.parseObject()
}

// ---------------------
// PART 2: Generic mapper from Map to any data class
// This function uses Kotlin reflection to inspect the primary constructor of the target class
// and then calls it with values from the map.

inline fun <reified T : Any> mapToDataClass(map: Map<String, Any>): T {
    return mapToDataClass(map, T::class)
}

inline fun <reified T : Any> parseProto(input: String): T {
    val parser = ProtoParser(input)
    val map = parser.parseObject()
    return mapToDataClass(map, T::class)
}

fun <T : Any> mapToDataClass(map: Map<String, Any>, klass: KClass<T>): T {
    val constructor = klass.primaryConstructor
        ?: throw IllegalArgumentException("No primary constructor for ${klass.simpleName}")
    val args = mutableMapOf<KParameter, Any?>()

    // For each constructor parameter, try to find a matching key in the map.
    for (param in constructor.parameters) {
        // Parameter name (likely in camelCase)
        val camelName = param.name ?: continue

        // Try the exact name first...
        var value: Any? = map[camelName]
        // ...or fall back to converting the camelCase name to snake_case.
        if (value == null) {
            value = map[camelToSnake(camelName)]
        }

        // If the value is itself a map and the parameter type is a data class, do a recursive conversion.
        if (value is Map<*, *> && param.type.classifier is KClass<*> &&
            (param.type.classifier as KClass<*>).isData
        ) {
            @Suppress("UNCHECKED_CAST")
            args[param] = mapToDataClass(value as Map<String, Any>, param.type.classifier as KClass<Any>)
        } else if (value != null) {
            args[param] = convertValue(value, param.type)
        } else {
            // If no value is found, we'll leave it as null so that default values (if any) are used.
            args[param] = null
        }
    }
    return constructor.callBy(args)
}

// Helper function to convert camelCase to snake_case.
fun camelToSnake(name: String): String {
    return name.replace(Regex("([a-z])([A-Z])"), "$1_$2").toLowerCase()
}

// Basic conversion: if the expected type is a primitive like Int or Double, do the conversion.
fun convertValue(value: Any, expectedType: KType): Any {
    return when (expectedType.classifier) {
        Int::class -> (value as Number).toInt()
        Double::class -> (value as Number).toDouble()
        Float::class -> (value as Number).toFloat()
        Long::class -> (value as Number).toLong()
        Boolean::class -> value as Boolean
        String::class -> value.toString()
        else -> value // Extend this as needed.
    }
}

// ---------------------
// Example data classes (using camelCase)

fun main() {
    // Note: the input keys are in snake_case.
    val input = """
        {
          epoch_params:{epoch_length:20  epoch_multiplier:1  epoch_new_coin:1048576  coin_halving_interval:100}  
          validation_params:{false_positive_rate:0.05  min_ramp_up_measurements:10  pass_value:0.99  min_validation_average:0.1  max_validation_average:1}  
          poc_params:{default_difficulty:5}  
          tokenomics_params:{subsidy_reduction_interval:0.05  subsidy_reduction_amount:0.2  current_subsidy_percentage:0.9}
        }
    """.trimIndent()

    val params: InferenceParams = parseProto(input)

    println(params)
}
