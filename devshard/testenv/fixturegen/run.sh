#!/usr/bin/env bash
# Generate fixtures for every model in $MODELS, one container at a time.
# Each container runs offline: it starts `vllm launch render` with the model's serving
# arguments, waits for /health, then runs gen.py over cases.json.
# Usage: [MODELS=models-mlnode310.tsv] [OUT=fixtures] [VARIANT=label] [BUILD_NOTE=text] [LOGTAG=tag] run.sh [model-substring] [case-name ...]
set -u
cd "$(dirname "$0")"
# HF_CACHE: offline Hugging Face cache holding the tokenizer files of every model in $MODELS.
HF=${HF_CACHE:?set HF_CACHE to the offline Hugging Face cache}
FILTER=${1:-}
shift || true
CASES_ARGS="$*"
MODELS=${MODELS:-models-mlnode310.tsv}
if [ ! -f "$MODELS" ] || [ ! -r "$MODELS" ]; then
  echo "Cannot read models table: $MODELS" >&2
  exit 1
fi
OUT=${OUT:-fixtures}
VARIANT=${VARIANT:-}
LOGTAG=${LOGTAG:-}
# The container writes under /w, this directory: OUT must be relative to it.
case "$OUT" in /*|*..*) echo "OUT must be a path inside $(pwd)" >&2; exit 2 ;; esac
mkdir -p "$OUT" logs
FAILED=0
SELECTED=0
while IFS=$'\t' read -r MODEL REV IMAGE HEADER DEFAULT_MODE ARGS; do
  [ -n "$FILTER" ] && [[ "$MODEL" != *"$FILTER"* ]] && continue
  [ -n "$MODEL" ] || continue
  SELECTED=$((SELECTED + 1))
  SLUG=$(echo "$MODEL" | tr '/' '_')
  AVAIL=$(free -g | awk '/^Mem:/ {print $7}')
  if [ "$AVAIL" -lt 8 ]; then
    echo "skip $MODEL: only ${AVAIL} GiB available (need 8)"; FAILED=1; continue
  fi
  if ! IMAGE_ID=$(docker image inspect --format '{{.Id}}' "$IMAGE"); then
    echo "skip $MODEL: no image $IMAGE"; FAILED=1; continue
  fi
  FORK_SHA=$(docker image inspect --format '{{index .Config.Labels "fixturegen.fork-sha"}}' "$IMAGE")
  FORK_REF=$(docker image inspect --format '{{index .Config.Labels "fixturegen.fork-ref"}}' "$IMAGE")
  echo "== $MODEL on $IMAGE ($IMAGE_ID), available ${AVAIL} GiB"
  timeout 1800 docker run --rm --network none --memory 8g --cpus 4 --entrypoint bash \
    -e HF_HUB_OFFLINE=1 -e HF_HOME=/hf -e HF_HUB_CACHE=/hf -e VLLM_TARGET_DEVICE=cpu \
    -e MODEL="$MODEL" -e REV="$REV" -e ARGS="$ARGS" -e IMAGE="$IMAGE" -e IMAGE_ID="$IMAGE_ID" -e TOOL_HEADER="$HEADER" \
    -e FORK_SHA="$FORK_SHA" -e FORK_REF="$FORK_REF" -e VARIANT="$VARIANT" -e BUILD_NOTE="${BUILD_NOTE:-}" -e DEFAULT_MODE="${DEFAULT_MODE#-}" \
    -e LOG="/w/logs/$SLUG${LOGTAG:+-$LOGTAG}-render.log" -e DEST="/w/$OUT/$SLUG" -e CASES_ARGS="$CASES_ARGS" \
    -v "$PWD":/w -v "$HF":/hf "$IMAGE" -c "
      vllm launch render \"\$MODEL\" --revision \"\$REV\" \$ARGS --port 8000 > \"\$LOG\" 2>&1 &
      PID=\$!
      for i in \$(seq 1 300); do curl -sf localhost:8000/health >/dev/null && break; kill -0 \$PID 2>/dev/null || break; sleep 1; done
      if ! curl -sf localhost:8000/health >/dev/null; then echo 'render server not up'; tail -30 \"\$LOG\"; exit 1; fi
      echo \"render server up after \${i}s, memory.peak=\$(( \$(cat /sys/fs/cgroup/memory.peak) / 1048576 )) MiB\"
      python3 /w/gen.py /w/cases.json \"\$DEST\" \$CASES_ARGS
      RC=\$?
      echo \"gen rc=\$RC memory.peak=\$(( \$(cat /sys/fs/cgroup/memory.peak) / 1048576 )) MiB\"
      exit \$RC
    " > "logs/$SLUG${LOGTAG:+-$LOGTAG}-gen.log" 2>&1 < /dev/null
  RC=$?
  echo "rc=$RC $MODEL -> logs/$SLUG${LOGTAG:+-$LOGTAG}-gen.log"
  [ "$RC" -eq 0 ] || FAILED=1
done < <(tail -n +2 "$MODELS")
if [ "$SELECTED" -eq 0 ]; then
  echo "No models selected from $MODELS" >&2
  FAILED=1
fi
# A model that did not run leaves its earlier fixtures in place, which check.py would pass.
python3 check.py "$OUT" || FAILED=1
exit "$FAILED"
