# Proof of Work v2

## Development

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


### User and Group
For convenience, the user and group are created in the container with the same UID and GID as the host user.
It allows the container to write to the `scripts` and `notebooks` folders without changing the permissions on your local machine.  

To use the same UID and GID as your host user, run the following command:
```bash
echo "HOST_UID=$(id -u)" >> .env
echo "HOST_GID=$(id -g)" >> .env
```

### Jupyter

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

### Scripts

```bash
docker compose run --rm pow python scripts/check_operations.py
```


## Production

WIP