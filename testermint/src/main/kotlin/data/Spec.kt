package com.productscience.data

import com.google.gson.FieldNamingStrategy
import com.google.gson.Gson
import com.google.gson.GsonBuilder
import org.tinylog.kotlin.Logger
import kotlin.reflect.KClass
import kotlin.reflect.KProperty1
import kotlin.reflect.jvm.javaField

// Spec class with JSON serialization support using Gson
class Spec<T : Any>(private val constraints: MutableMap<KProperty1<T, *>, Any?>, val klass: KClass<T>) {

    fun matches(instance: T): Boolean {
        return constraints.all { (property, expectedValue) ->
            val actualValue = property.get(instance)

            when (expectedValue) {
                is Spec<*> -> {
                    @Suppress("UNCHECKED_CAST")
                    (expectedValue as Spec<Any>).matches(actualValue ?: return false)
                }

                else -> {
                    val isMatch = actualValue == expectedValue
                    if (!isMatch) {
                        Logger.debug("Mismatch for ${property.name}: expected $expectedValue, got $actualValue")
                    }
                    isMatch
                }
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


    // Merges this Spec with another Spec and returns a new Spec containing constraints of both
    fun merge(other: Spec<T>): Spec<T> {
        val mergedConstraints = this.constraints.toMutableMap()

        other.constraints.forEach { (property, otherValue) ->
            val thisValue = mergedConstraints[property]

            mergedConstraints[property] = when {
                thisValue is Spec<*> && otherValue is Spec<*> -> {
                    @Suppress("UNCHECKED_CAST")
                    (thisValue as Spec<Any>).merge(otherValue as Spec<Any>)
                }

                else -> otherValue ?: thisValue
            }
        }

        @Suppress("UNCHECKED_CAST")
        return Spec(mergedConstraints, this.klass)
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
    constraints.block()
    constraints.forEach { (property, value) ->
        if (value is Spec<*>) {
            val returnType = property.returnType.classifier
            val klass = value.klass
            require(
               returnType == klass
            ) {
                "Type mismatch for Spec property '${property.name}': expected ${property.returnType}, but got ${value::class}"
            }
        } else {
            require(value == null || 
                    property.returnType.classifier!! == value::class || 
                    (property.returnType.toString().startsWith("kotlin.collections.") && value is Collection<*>)) {
                "Type mismatch for property '${property.name}': expected ${property.returnType}, but got ${value!!::class}"
            }
        }
    }

    return Spec(constraints, T::class)
}
