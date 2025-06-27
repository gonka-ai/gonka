# Mock Server

This is a mock server subproject for the testermint project. It provides a simple HTTP server with various endpoints for testing purposes.

## Integration with IntelliJ IDEA

This project is set up as a Gradle subproject of the main testermint project. To properly load it in IntelliJ IDEA:

1. Open the main testermint project in IntelliJ IDEA
2. Make sure you have the Gradle plugin enabled
3. Refresh the Gradle project by clicking on the Gradle refresh button in the Gradle tool window
4. IntelliJ should automatically recognize the mock_server as a subproject

If the subproject is not recognized automatically:

1. Go to File > Project Structure
2. Select Modules
3. Click the + button and select "Import Module"
4. Navigate to the mock_server directory and select it
5. Choose "Import module from external model" and select Gradle
6. Follow the prompts to complete the import

## Running the Server

### Using Gradle

To run the server from the command line using Gradle:

```bash
./gradlew :mock_server:run
```

The server will start on port 8080 by default.

### Using Docker

A Dockerfile is provided in the root directory of the project. To build and run the server using Docker:

1. Build the Docker image:

```bash
docker build -t inference-mock-server .
```

2. Run the Docker container:

```bash
docker run -p 8080:8080 inference-mock-server
```

This will start the server and expose it on port 8080 of your host machine.

## Endpoints

### State Management Endpoints
- `/status` - Returns a simple status response with version and timestamp
- `GET /api/v1/state` - Returns the current state of the model
- `GET /api/v1/pow/status` - Returns the current status for PoC subprocess
- `POST /api/v1/pow/init/generate` - Generates POC and transitions to POW state
- `POST /api/v1/pow/init/validate` - Validates POC (requires POW state)
- `POST /api/v1/pow/validate` - Validates POC batch (requires POW state)
- `POST /api/v1/inference/up` - Transitions to INFERENCE state (requires STOPPED state)
- `POST /api/v1/train/start` - Transitions to TRAIN state (requires STOPPED state)
- `POST /api/v1/stop` - Transitions to STOPPED state
- `GET /health` - Returns 200 OK if the state is INFERENCE, otherwise 503 Service Unavailable

### Response Modification Endpoints
- `POST /api/v1/responses/inference` - Sets the response for the inference endpoint
  - Request body: `{ "response": "string", "delay": int, "segment": "string", "model": "string" }`
- `POST /api/v1/responses/inference/object` - Sets the response using an OpenAIResponse object
  - Request body: OpenAIResponse object with optional `delay`, `segment`, and `model` fields
- `POST /api/v1/responses/poc` - Sets the POC response with the specified weight
  - Request body: `{ "weight": long, "scenarioName": "string" }`
- `GET /api/v1/responses/inference` - Gets all inference responses
- `GET /api/v1/responses/poc` - Gets all POC responses

## State Management

The server maintains a state machine with the following states:

- `STARTED` - Initial state
- `POW` - Proof of Work state
- `INFERENCE` - Inference state
- `TRAIN` - Training state
- `STOPPED` - Stopped state

The `POW` stage has the following substates:

- `POW_IDLE`
- `POW_NO_CONTROLLER`
- `POW_LOADING`
- `POW_GENERATING`
- `POW_VALIDATING`
- `POW_STOPPED`
- `POW_MIXED`

Only 3 are used actually: `POW_GENERATING`, `POW_VALIDATING` and `POW_STOPPED`.

Others are defined here because they may be returned by the real server.

## Webhook Support

The server supports webhooks for the following endpoints:

- `POST /api/v1/pow/init/generate` - Sends a POST request to the URL specified in the request body with the generated POC data.
- `POST /api/v1/pow/init/validate` - Sends a POST request to the URL specified in the request body with the validation results.
- `POST /api/v1/pow/validate` - Sends a POST request to a predefined URL with the batch validation results.

## Implementation Details

The server is implemented using Ktor, a Kotlin framework for building asynchronous servers and clients. It uses the following components:

- `ModelState` - An enum class that represents the possible states of the model
- `WebhookService` - A service class that handles webhook callbacks
- `ResponseService` - A service class that manages and modifies responses for various endpoints
- `OpenAIResponse` - A data class representing the OpenAI API response structure
- Route handlers for different endpoint groups:
  - `StateRoutes` - Handles state-related endpoints
  - `PowRoutes` - Handles POW-related endpoints
  - `InferenceRoutes` - Handles inference-related endpoints
  - `TrainRoutes` - Handles training-related endpoints
  - `StopRoutes` - Handles stop-related endpoints
  - `HealthRoutes` - Handles health-related endpoints
  - `ResponseRoutes` - Handles response modification endpoints
