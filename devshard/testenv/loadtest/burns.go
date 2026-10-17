package loadtest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const ghostStage = "ghost_probe_skipped"

// faultBurnReasons are the burns a fault causes: the gateway ejected or cut off the slot's host.
var faultBurnReasons = map[string]bool{"participant_ejected_no_send": true, "participant_cut_off_no_send": true}

// budgetBurnReasons are the burns of a host that refuses Chats over its peer budget: the gateway
// throttles it, and it falls behind on state it never got. Such a fault is often a background marker
// that lasts the whole run, so its burns are left out in background windows too.
var budgetBurnReasons = map[string]bool{"participant_throttled_no_send": true, "participant_catching_up_no_send": true}

// gatewayBurn is one nonce the gateway committed and never sent (a ghost), from its log.
type gatewayBurn struct {
	At       time.Time `json:"at"`
	EscrowID string    `json:"escrow_id"`
	Nonce    string    `json:"nonce"`
	Host     string    `json:"host"` // user.Session.HostLabel: the last 8 characters of the participant address
	Reason   string    `json:"reason"`
}

// readGatewayBurns returns every ghost_probe_skipped line of a compose log, in the text or the JSON
// log format, in log order.
func readGatewayBurns(path string) ([]gatewayBurn, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read gateway trace %s: %w", path, err)
	}
	defer file.Close()
	burns := []gatewayBurn{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, ghostStage) {
			continue
		}
		burn, ok, err := parseGatewayBurn(line)
		if err != nil {
			return nil, fmt.Errorf("parse ghost line in %s: %w", path, err)
		}
		if ok {
			burns = append(burns, burn)
		}
	}
	return burns, scanner.Err()
}

func parseGatewayBurn(line string) (gatewayBurn, bool, error) {
	fields := map[string]string{}
	if start := strings.Index(line, "{"); start >= 0 && json.Valid([]byte(line[start:])) {
		var record map[string]any
		if err := json.Unmarshal([]byte(line[start:]), &record); err != nil {
			return gatewayBurn{}, false, err
		}
		for key, value := range record {
			switch v := value.(type) {
			case string:
				fields[key] = v
			case float64:
				fields[key] = strconv.FormatFloat(v, 'f', -1, 64)
			}
		}
	} else {
		for _, field := range strings.Fields(line) {
			if key, value, ok := strings.Cut(field, "="); ok {
				fields[key] = strings.Trim(value, `"`)
			}
		}
	}
	if fields["stage"] != ghostStage {
		return gatewayBurn{}, false, nil
	}
	if _, err := strconv.ParseUint(fields["nonce"], 10, 64); err != nil {
		return gatewayBurn{}, false, fmt.Errorf("nonce %q: %w", fields["nonce"], err)
	}
	burn := gatewayBurn{EscrowID: fields["escrow"], Nonce: fields["nonce"], Host: fields["host"], Reason: fields["reason"]}
	if at, err := time.Parse(time.RFC3339Nano, fields["time"]); err == nil {
		burn.At = at.UTC()
	}
	return burn, true, nil
}

// ghostSet maps escrow to burned nonce to whether the ghost rate leaves it out. Escrow "" holds
// every burn, for a runtime read without its escrow.
type ghostSet map[string]map[string]bool

func newGhostSet(burns []gatewayBurn, excused func(gatewayBurn) bool) ghostSet {
	set := ghostSet{"": {}}
	for _, burn := range burns {
		left := excused != nil && excused(burn)
		if set[burn.EscrowID] == nil {
			set[burn.EscrowID] = map[string]bool{}
		}
		set[burn.EscrowID][burn.Nonce] = left
		set[""][burn.Nonce] = set[""][burn.Nonce] || left
	}
	return set
}

func (g ghostSet) escrow(id string) map[string]bool {
	return g[id]
}

// burnExcuse reports whether the ghost rate leaves a burn out: the gateway ejected or cut off the
// slot's host inside one of its graced fault windows, or throttled it inside a peer-budget fault
// window, so the fault caused it.
func burnExcuse(in InvariantInputs) func(gatewayBurn) bool {
	c := newInvariantContext(in)
	return func(burn gatewayBurn) bool {
		if burn.Host == "" {
			return false
		}
		for _, participant := range c.participants {
			if !strings.HasSuffix(participant, burn.Host) {
				continue
			}
			if faultBurnReasons[burn.Reason] && c.inForegroundWindow(participant, burn.At, burn.At) {
				return true
			}
			for _, w := range c.faultyDuring(participant, burn.At, burn.At) {
				if budgetBurnReasons[burn.Reason] && w.Event.Params["observe"] == "host_rate_limited" {
					return true
				}
			}
		}
		return false
	}
}

// burnedNonces maps escrow to the nonces the gateway burned.
func burnedNonces(burns []gatewayBurn) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, burn := range burns {
		if out[burn.EscrowID] == nil {
			out[burn.EscrowID] = map[string]bool{}
		}
		out[burn.EscrowID][burn.Nonce] = true
	}
	return out
}
