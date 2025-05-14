# TMKMS Integration with Our Project

This project integrates **TMKMS** with our system. For the official TMKMS repository, please visit [TMKMS GitHub](https://github.com/iqlusioninc/tmkms).
**Note:** TMKMS does not officially provide Docker images, so we create them ourselves to simplify the setup and deployment process.

### Two Setup Scenarios
There are two different scenarios for running **TMKMS**:

1. **Running with the Network Node**: This scenario is for when a participant is joining the network for the first time and needs to generate a new consensus key.
2. **Migrating an Existing Consensus Key**: In this case, you already have a validator's consensus key, and you want to import it into **TMKMS**.

To support these scenarios, we provide two different Dockerfiles to build two distinct Docker images.

### Build Commands

- **Build Image to Generate a New Validator Private Key:**
  ```bash
  make build-with-keygen
  ```

- **Build Image for Importing Existing Private Validator Key:**
  This scenario is used when migrating an already existing consensus key (`priv_validator_key.json` and `priv_validator_state.json`).
  ```bash
  make build-with-import
  ```

### Building with Latest Tag

To build these images with the `latest` tag, run the above commands and add the `SET_LATEST=1` environment variable:

```bash
export SET_LATEST=1
make build-with-keygen
make build-with-import
```