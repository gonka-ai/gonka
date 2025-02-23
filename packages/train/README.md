## Launching training

1. Define the environment variables in .env file.
2. Build the docker image.
3. Run the docker compose api and test.

## Environment variables
Define this variable in .env file.

HF_TOKEN=
WANDB_API_KEY=
WANDB_ENTITY=product-science
ZERO_BAND_LOG_LEVEL=DEBUG
ZERO_BAND_LOG_ALL_RANK=true

### Global Store Initialization
| Environment Variable  | Description                                      | Default Value |
|-----------------------|--------------------------------------------------|---------------|
| `GLOBAL_UNIQUE_ID`    | Unique identifier worker in global store.        | `None`  |
| `GLOBAL_ADDR`         | IP Address of the global store                   | `None`  |
| `GLOBAL_PORT`         | Port number of the global store.                 | `None` |
| `GLOBAL_WORLD_SIZE`   | The size of the global process group.            | `1` |
| `GLOBAL_RANK`         | Rank of the process in the global process group. | `0` |

### Elastic Device Mesh Configuration
| Environment Variable  | Description                                      | Default Value |
|-----------------------|--------------------------------------------------|---------------|
| `ZERO_BAND_LOG_LEVEL` | Enable debug mode for loge | `False` |
| `ZERO_BAND_GLOBAL_STORE_TIMEOUT_SECONDS` | Number of seconds before the global store operations timeout | `300` |
| `ZERO_BAND_GLOBAL_PG_TIMEOUT_SECONDS` | Number of seconds before the global process group operations timeout | `600` |
| `ZERO_BAND_GLOBAL_STORE_POLLING_INTERVAL_SECONDS` | Number of seconds between polls to the store when waiting for values | `0.1` |
| `ZERO_BAND_EDM_HEARTBEAT_INTERVAL_SECONDS` | Interval in seconds between heartbeats | `2` |
| `ZERO_BAND_EDM_HEARTBEAT_TIMEOUT_SECONDS` | Time in seconds after which a node is considered dead if no heartbeat is received | `10` |
| `ZERO_BAND_LIVE_RECO_PORT` | Port number for the live recovery server | random |  
| `ZERO_BAND_LIVE_RECO_ADDR` | IP Address for the live recovery server | `localhost` |  
