# Proof of Work v2

## Development

Everything is dockerized, so you can run the development environment from container.  
The `scripts` and `notebooks` folders are mounted into the container, 
so you can edit the notebooks and scripts in your local machine and they will be reflected in the container.

The `src` folder is the source code of the project and it's copied into the container at the `build` step.  
For development purposes you can also mount your `src` folder into the container:

### Jupyter

```yaml
...
volumes:
  - ./src:/app/src
...
```

```bash
docker compose up --build
```

Then you can access the jupyter lab interface at http://localhost:8080/
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
