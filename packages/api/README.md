# MLNode

IMPORTNAT NOTE: Repo is originally named pow. Right after cloning, rename directory to mlnode.

## Setup

1. Set up .env in root, packages/api, packages/pow, packages/train with the following (path below is an example):
```
PROJECT_ROOT=/mnt/ramdisk/tamaz/mlnode
```

2. Set up env variables in packages/train/.env:
```
HF_TOKEN=
WANDB_API_KEY=
WANDB_ENTITY=
ZERO_BAND_LOG_LEVEL=DEBUG
ZERO_BAND_LOG_ALL_RANK=true
```

3. Build docker image from root

```
source .env
docker build $PROJECT_ROOT/ -t comb-test -f $PROJECT_ROOT/packages/api/Dockerfile
```

4. Run server and then tests

```
docker compose run server
```

```
docker compose run pow-api-test
```

```
docker compose run train-api-test
```







