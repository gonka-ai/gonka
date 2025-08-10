package com.productscience.mockserver.model

import org.slf4j.LoggerFactory

/**
 * Enum representing the possible states of the model.
 */
enum class ModelState {
    POW,
    INFERENCE,
    TRAIN,
    STOPPED;

    companion object {
        private val logger = LoggerFactory.getLogger(ModelState::class.java)
        // Default initial state
        private var currentState: ModelState = STOPPED

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

enum class PowState {
    POW_IDLE,
    POW_NO_CONTROLLER,
    POW_LOADING,
    POW_GENERATING,
    POW_VALIDATING,
    POW_STOPPED,
    POW_MIXED;

    companion object {
        private val logger = LoggerFactory.getLogger(PowState::class.java)
        private var currentState: PowState = POW_STOPPED

        fun getCurrentState(): PowState {
            return currentState
        }

        fun updateState(newState: PowState) {
            logger.debug("POW state changing from $currentState to $newState")
            currentState = newState
            logger.debug("POW state changed to $newState")
        }
    }
}
