# Proof of Work v2

## Usage Example

### Starting the Server

1. **Launch the Server:**
   - Use Docker Compose to start the server application. By default, it runs on port 8080.
   ```
   docker compose up server
   ```

2. **Launch the Batch Receiver:**
   - This is a separate application that stores all received batches. You can later access these batches from a Jupyter notebook.
   - Use the following command to start the batch receiver. Adjust the host and port as needed, but ensure to update these values in the notebook accordingly.
   ```
   docker compose up batch-reciever
   ```

3. **Launch the Test:**
   - This is a test that goes through the pow API commands.
   ```
   docker compose up test
   ```

### Using the Jupyter Notebook

1. **Open the Notebook:**
   - Access the notebook at the following URL: [rest-usage.ipynb](https://github.com/product-science/pow/blob/tg/for_dima/notebooks/rest-usage.ipynb).

2. **Follow the Instructions:**
   - The notebook guides you through the process of initiating batch generation.
   - It demonstrates how to create a batch of 2000 valid nonces based on one correct batch.
   - It also shows how to create a second batch of 2000 nonces, with 10 intentionally incorrect ones.
   - Finally, it walks you through the validation process for both batches and prints the results.


## Build

The image is built on top of vLLM's fork image, which is available on GCP as `decentralized-ai/vllm:<VLLM_VERSION>`. 
Changes in the current repository don't require rebuilding the vLLM's fork image.


#### Build vLLM's fork

If you need to build the vLLM's fork image, you can do it with the following command:

```bash
DOCKER_BUILDKIT=1 \
docker build . --target vllm-openai \
--tag gcr.io/decentralized-ai/vllm:<VLLM_VERSION>  \
--build-arg max_jobs=24 \
--build-arg nvcc_threads=12 \
--build-arg CUDA_VERSION=12.1.0
```

And push it to GCP:

```bash
docker push gcr.io/decentralized-ai/vllm:<VLLM_VERSION>  
```

Then you need to update the `VLLM_VERSION` in the `Dockerfile` to the new version.

`<VLLM_VERSION>` is the version of the vLLM's fork image. *It should be aligned with the original vLLM version.*

Current vLLM version is `0.5.0.post1`.  
Current latest version of vLLM's fork can be found in `productscience/dev` branch.

#### Build the Proof of Work image

```bash
DOCKER_BUILDKIT=1 \
docker build . --target app --tag gcr.io/decentralized-ai/inference-runner:<VERSION>
```

And push it to GCP:

```bash
docker push gcr.io/decentralized-ai/inference-runner:<VERSION>
```

`<VERSION>` is the version of the Proof of Work image.


### Development

Everything is dockerized, so you can run the development environment from container.  
The `scripts` and `notebooks` folders are mounted into the container, 
so you can edit the notebooks and scripts in your local machine and they will be reflected in the container.

The `src` folder is the source code of the project and it's copied into the container at the `build` step.  
For development purposes you can also mount your `src` folder into the container:

```yaml
...
volumes:
  - ./src:/app/src
...
```


#### User and Group
For convenience, the user and group are created in the container with the same UID and GID as the host user.
It allows the container to write to the `scripts` and `notebooks` folders without changing the permissions on your local machine.  

To use the same UID and GID as your host user, run the following command:
```bash
echo "HOST_UID=$(id -u)" >> .env
echo "HOST_GID=$(id -g)" >> .env
```

#### Jupyter

```bash
docker compose up --build
```

Then you can access the jupyter lab interface at http://localhost:8080/. 
The password can be found under `JUPYTER_TOKEN` in the `.env` file.
For sure you will need to forward the port to your local machine.

```bash
ssh -L 8080:localhost:8080 user@remote-server
```

Or with gcloud:

```bash
gcloud compute start-iap-tunnel pow-test 8080 --project=<PROJECT_ID> --local-host-port=localhost:8080
```

#### Scripts

```bash
docker compose run --rm pow python scripts/check_operations.py
```
