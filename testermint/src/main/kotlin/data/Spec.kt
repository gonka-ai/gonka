package com.productscience.data

import com.google.gson.FieldNamingStrategy
import com.google.gson.Gson
import com.google.gson.GsonBuilder
import kotlin.reflect.KProperty1
import kotlin.reflect.jvm.javaField

// Spec class with JSON serialization support using Gson
class Spec<T : Any>(private val constraints: Map<KProperty1<T, *>, Any?>) {

    fun matches(instance: T): Boolean {
        return constraints.all { (property, expectedValue) ->
            val actualValue = property.get(instance)

            when (expectedValue) {
                is Spec<*> -> {
                    @Suppress("UNCHECKED_CAST")
                    (expectedValue as Spec<Any>).matches(actualValue ?: return false)
                }

                else -> actualValue == expectedValue
            }
        }
    }

    fun assertMatches(instance: T) {
        constraints.forEach { (property, expectedValue) ->
            val actualValue = property.get(instance)

            when (expectedValue) {
                is Spec<*> -> {
                    @Suppress("UNCHECKED_CAST")
                    (expectedValue as Spec<Any>).assertMatches(
                        actualValue ?: error("Expected ${property.name} to be non-null")
                    )
                }

                else -> require(actualValue == expectedValue) {
                    "Mismatch for ${property.name}: expected $expectedValue, got $actualValue"
                }
            }
        }
    }

    // Converts Spec<T> into a Map<String, Any?> for JSON serialization
    fun toMap(fieldNamingStrategy: FieldNamingStrategy): Map<String, Any?> {
        return constraints.mapKeys { fieldNamingStrategy.translateName(it.key.javaField) }.mapValues { (_, value) ->
            when (value) {
                is Spec<*> -> value.toMap(fieldNamingStrategy) // Recursively convert nested specs
                else -> value
            }
        }
    }

    // Serializes Spec<T> to JSON using Gson
    fun toJson(gson: Gson? = null): String {
        val actualGson: Gson = gson ?: GsonBuilder().setPrettyPrinting().create()
        return actualGson.toJson(toMap(actualGson.fieldNamingStrategy()))
    }
}

// Builder function to create a spec (Fix: Properly stores constraints)
inline fun <reified T : Any> spec(block: MutableMap<KProperty1<T, *>, Any?>.() -> Unit): Spec<T> {
    val constraints = mutableMapOf<KProperty1<T, *>, Any?>()
    constraints.block() // <-- This is the crucial fix: Actually mutate the constraints map!
    return Spec(constraints)
}
