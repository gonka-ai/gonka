#!/usr/bin/env bash
#
# devshard-v3-stream-fanout.sh
#
# Opens N parallel SSH sessions to the host running the v3 standalone
# devshardctl and fires one streaming /v1/chat/completions request per session.
# Each session gets a DIFFERENT story subject, randomly sampled (without
# replacement) from an embedded pool of 500 words. Each session's rendered text
# is written to its own local log so concurrent streams don't interleave.
#
# The heavy quoting of the SSE renderer is avoided by shipping a small helper
# script to the host once and feeding each request body over the SSH stdin.
#
# Usage:
#   ./devshard-v3-stream-fanout.sh                 # N=20 random subjects
#   N=50 ./devshard-v3-stream-fanout.sh            # 50 random subjects
#   N=200 SEED=42 ./devshard-v3-stream-fanout.sh   # reproducible sample
#   SSH_PORT=18222 DEVSHARD_PORT=18082 ./devshard-v3-stream-fanout.sh
#
set -euo pipefail

# ---- connection / target (override via env) --------------------------------
SSH_USER=${SSH_USER:-decentai}
SSH_HOST=${SSH_HOST:-xj7-5.s.filfox.io}
SSH_PORT=${SSH_PORT:-18222}                 # genesis host 702111 (standalone lives here)
DEVSHARD_PORT=${DEVSHARD_PORT:-18082}       # v3 standalone devshardctl port (NOT the gateway :18080)
MODEL=${MODEL:-"Qwen/Qwen3-4B-Instruct-2507"}

# ---- fanout controls -------------------------------------------------------
N=${N:-20}                                  # number of random subjects / parallel sessions
SEED=${SEED:-}                              # optional: fix the random sample (empty = time-based)
STAGGER_SEC=${STAGGER_SEC:-0.15}            # small delay between launches to be nice to sshd
OUTDIR=${OUTDIR:-/tmp/v3-stream-fanout-$(date +%Y%m%d-%H%M%S)}
REMOTE_HELPER=${REMOTE_HELPER:-/tmp/devshard-stream-render.sh}

SSH_OPTS=(-p "$SSH_PORT" -o ConnectTimeout=10 -o ServerAliveInterval=15 -o BatchMode=yes)
TARGET="${SSH_USER}@${SSH_HOST}"

# ---- embedded pool of 500 single-word subjects -----------------------------
read -r -d '' SUBJECT_POOL <<'POOL' || true
stones snakes rivers mountains moon coffee bridges mirrors clocks gardens
deserts libraries oceans forests lighthouses trains wine birds maps storms
candles ravens roses daggers castles ghosts sailors whales comets tunnels
lanterns keys doors windows staircases attics cellars gargoyles fountains statues
violins pianos drums flutes trumpets harps bells organs guitars cellos
apples cherries plums peaches lemons oranges grapes figs pears melons
wolves foxes bears lions tigers eagles owls hawks doves sparrows
dragons unicorns mermaids giants dwarves elves witches wizards knights kings
queens princes princesses jesters minstrels pilgrims hermits monks nuns priests
swords shields armor helmets arrows spears axes bows crossbows catapults
ships boats rafts canoes galleons frigates submarines yachts ferries barges
rain snow hail fog mist frost dew clouds thunder lightning
meadows valleys canyons cliffs caves glaciers volcanoes reefs islands peninsulas
roots branches leaves petals thorns vines moss ferns mushrooms lichen
amber jade ivory pearls diamonds rubies emeralds sapphires opals garnets
iron copper silver gold bronze steel tin lead zinc nickel
bread cheese honey salt pepper sugar cinnamon nutmeg saffron vinegar
tea milk beer cider mead brandy whiskey rum vodka gin
letters diaries scrolls manuscripts tablets inscriptions poems ballads sonnets odes
riddles fables myths legends parables proverbs prophecies curses blessings spells
shadows echoes whispers silence dreams nightmares memories secrets promises regrets
hope fear love hatred envy pride greed courage sorrow joy
hourglasses sundials compasses telescopes microscopes lenses prisms globes atlases charts
feathers scales shells claws fangs horns tusks wings beaks talons
rats mice bats moles hedgehogs squirrels rabbits hares badgers otters
salmon trout cod herring tuna sharks eels octopuses squids crabs
bees ants beetles moths butterflies spiders scorpions locusts wasps dragonflies
oaks pines birches willows maples cedars elms aspens redwoods sequoias
tulips lilies daisies orchids violets poppies sunflowers marigolds jasmine lavender
palaces temples cathedrals monasteries fortresses citadels towers dungeons crypts shrines
markets taverns inns harbors docks wharves plazas courtyards alleys boulevards
crowns scepters thrones banners tapestries chalices goblets amulets talismans relics
masks puppets marionettes carousels kaleidoscopes automatons dolls figurines toys trinkets
rings bracelets necklaces brooches earrings pendants lockets tiaras diadems anklets
quills inkwells parchment wax seals ribbons envelopes stamps postcards telegrams
winds tides currents whirlpools waterfalls geysers springs wells aqueducts canals
dunes oases mirages caravans nomads camels tents bazaars carpets pyramids
stars planets galaxies nebulae meteors asteroids eclipses auroras constellations orbits
angels demons spirits phantoms specters banshees vampires werewolves zombies goblins
heroes villains outlaws bandits pirates smugglers spies assassins mercenaries gladiators
farmers shepherds fishermen blacksmiths carpenters weavers potters bakers butchers tailors
doctors nurses scholars philosophers astronomers alchemists inventors explorers cartographers navigators
orchards vineyards greenhouses hedges arbors trellises lawns pastures fields moors
hills ridges plateaus foothills slopes summits gorges ravines crevasses fjords
streams brooks creeks lagoons estuaries deltas marshes swamps bogs ponds
novels essays plays scripts screenplays chapters prologues epilogues footnotes prefaces
verses stanzas rhymes metaphors similes allegories symbols themes motifs refrains
crimson scarlet azure indigo turquoise magenta vermilion ochre teal maroon
summers winters autumns dawns dusks twilights middays midnights sunrises sunsets
journeys voyages quests pilgrimages expeditions odysseys adventures wanderings crossings migrations
POOL

# dedupe while preserving pool, then count (bash 3.2 compatible: no mapfile)
POOL_WORDS=()
while IFS= read -r w; do
  [[ -n "$w" ]] && POOL_WORDS+=("$w")
done < <(printf '%s\n' $SUBJECT_POOL | awk 'NF && !seen[$0]++')
POOL_COUNT=${#POOL_WORDS[@]}

if (( N > POOL_COUNT )); then
  echo ">> requested N=${N} exceeds pool size ${POOL_COUNT}; capping N=${POOL_COUNT} (sampling without replacement)"
  N=$POOL_COUNT
fi

# portable random shuffle (macOS/bash-3.2-safe: no shuf/sort -R/mapfile needed)
CHOSEN=()
while IFS= read -r w; do
  [[ -n "$w" ]] && CHOSEN+=("$w")
done < <(printf '%s\n' "${POOL_WORDS[@]}" \
  | awk -v seed="${SEED:-}" 'BEGIN{ if (seed=="") srand(); else srand(seed) } {print rand()"\t"$0}' \
  | sort -k1,1n | cut -f2- | head -n "$N")

mkdir -p "$OUTDIR"

# ---- 1) ship the SSE renderer helper to the host --------------------------
cat >"$OUTDIR/render.sh" <<'HELPER'
#!/usr/bin/env sh
# Reads a chat-completions JSON body on stdin, streams it, prints token deltas.
port="$1"
curl -sS -N -X POST "http://127.0.0.1:${port}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -d @- \
| while IFS= read -r line; do
    case "$line" in
      "data: [DONE]") break ;;
      data:\ *) printf '%s' "$(printf '%s' "${line#data: }" | jq -rj '.choices[0].delta.content // empty')" ;;
    esac
  done
echo
HELPER

echo ">> pool=${POOL_COUNT} words | sampling ${N} distinct subjects${SEED:+ (seed=${SEED})}"
echo ">> shipping renderer to ${TARGET}:${REMOTE_HELPER}"
scp -q -P "$SSH_PORT" "$OUTDIR/render.sh" "${TARGET}:${REMOTE_HELPER}"

# ---- 2) launch N parallel streaming sessions -------------------------------
echo ">> launching ${N} streaming sessions against http://127.0.0.1:${DEVSHARD_PORT} (v3 standalone)"
echo ">> logs: ${OUTDIR}/story-NN-<subject>.log"
echo

pids=()
i=0
for subj in "${CHOSEN[@]}"; do
  i=$((i+1))
  idx=$(printf "%03d" "$i")
  log="$OUTDIR/story-${idx}-${subj}.log"
  body="$OUTDIR/body-${idx}.json"

  prompt="Write short, essay on the history of ${subj} in literature starting with early poems and ending with bestselling novels of today."
  jq -nc --arg model "$MODEL" --arg content "$prompt" \
    '{model:$model, stream:true, messages:[{role:"user", content:$content}]}' >"$body"

  {
    echo "=== session ${idx} | subject: ${subj} | $(date -u +%H:%M:%SZ) ==="
    ssh "${SSH_OPTS[@]}" "$TARGET" "sh ${REMOTE_HELPER} ${DEVSHARD_PORT}" <"$body"
    echo "=== session ${idx} done | $(date -u +%H:%M:%SZ) ==="
  } >"$log" 2>&1 &
  pids+=("$!")

  echo "  [${idx}] subject='${subj}' -> ${log}"
  sleep "$STAGGER_SEC"
done

echo
echo ">> all ${N} sessions launched. Follow one with:  tail -f ${OUTDIR}/story-001-*.log"
echo ">> waiting for completion..."

fail=0
for pid in "${pids[@]}"; do
  wait "$pid" || fail=$((fail+1))
done

echo
echo ">> done. ${N} sessions finished (${fail} non-zero exits). Outputs in: ${OUTDIR}"
ls -1 "$OUTDIR"/story-*.log
