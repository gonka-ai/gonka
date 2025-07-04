package com.productscience.mockserver.model

import org.slf4j.LoggerFactory

/**
 * Enum representing the possible states of the model.
 */
enum class ModelState {
    STARTED,
    POW,
    INFERENCE,
    TRAIN,
    STOPPED;

    companion object {
        private val logger = LoggerFactory.getLogger(ModelState::class.java)
        // Default initial state
        private var currentState: ModelState = STARTED

        /**
         * Get the current state of the model.
         */
        fun getCurrentState(): ModelState {
            return currentState
        }

        /**
         * Update the current state of the model.
         */
        fun updateState(newState: ModelState) {
            logger.debug("Model state changing from $currentState to $newState")
            currentState = newState
            logger.debug("Model state changed to $newState")
        }
    }
}
