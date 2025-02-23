FROM gcr.io/decentralized-ai/vllm:0.5.0.post1 AS builder

ENV POETRY_VERSION=2.0.1 \
    PYTHONUNBUFFERED=1 \
    POETRY_NO_INTERACTION=1 \
    DEBIAN_FRONTEND=noninteractive

RUN pip install --upgrade pip && \
    pip install "poetry==$POETRY_VERSION"

WORKDIR /app

# Copy all pyproject.toml files
COPY packages/proof-of-work/pyproject.toml packages/proof-of-work/poetry.lock /app/mlnode/packages/proof-of-work/
COPY packages/train/pyproject.toml packages/train/poetry.lock /app/mlnode/packages/train/
COPY packages/api/pyproject.toml packages/api/poetry.lock /app/mlnode/packages/api/

COPY packages/api/src /app/mlnode/packages/api/src

COPY packages/proof-of-work/src /app/mlnode/packages/proof-of-work/src
COPY packages/proof-of-work/tests /app/mlnode/packages/proof-of-work/tests
COPY packages/proof-of-work/resources /app/mlnode/packages/proof-of-work/resources
COPY packages/proof-of-work/scripts /app/mlnode/packages/proof-of-work/scripts
COPY packages/proof-of-work/README.md /app/mlnode/packages/proof-of-work/README.md

COPY packages/train/src /app/mlnode/packages/train/src
COPY packages/train/scripts /app/mlnode/packages/train/scripts
COPY packages/train/configs /app/mlnode/packages/train/configs
COPY packages/train/third_party /app/mlnode/packages/train/third_party
COPY packages/train/README.md /app/mlnode/packages/train/README.md


# Create single virtualenv and install dependencies from both projects
RUN poetry config virtualenvs.in-project true 
RUN cd /app/mlnode/packages/api && poetry install --no-root

RUN . /app/mlnode/packages/api/.venv/bin/activate
RUN pip install --verbose -e /app/mlnode/packages/train/third_party/gorilla/berkeley-function-call-leaderboard[oss_eval_vllm]

FROM gcr.io/decentralized-ai/vllm:0.5.0.post1 AS app
COPY --from=builder /app/mlnode/packages/api/.venv /app/mlnode/packages/api/.venv
COPY --from=builder /app/mlnode /app/mlnode
COPY .git /app/mlnode/.git

ENV PYTHONPATH="/app"
ENV PYTHONPATH="${PYTHONPATH}:/app/mlnode/packages/api/src"
ENV PYTHONPATH="${PYTHONPATH}:/app/mlnode/packages/proof-of-work/src"
ENV PYTHONPATH="${PYTHONPATH}:/app/mlnode/packages/train/src"
ENV PYTHONPATH="${PYTHONPATH}:/app/mlnode/packages/train/third_party/gorilla/berkeley-function-call-leaderboard"

COPY packages/api/entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh
WORKDIR /app
ENTRYPOINT ["/app/entrypoint.sh"]
