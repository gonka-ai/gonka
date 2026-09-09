package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

func main() {
	fs := flag.NewFlagSet("devshard-testing", flag.ExitOnError)
	grpcAddr := fs.String("grpc", "", "chain gRPC endpoint host:port (e.g. 34.9.x.x:9090)")
	rest := fs.String("rest", "", "chain REST URL for devshardctl (e.g. http://34.9.x.x:1317)")
	privateKey := fs.String("private-key", "", "raw hex private key")
	count := fs.Int("count", 3, "number of escrows to create")
	model := fs.String("model", "Qwen/Qwen2.5-7B-Instruct", "model ID")
	basePort := fs.Int("base-port", 18080, "first devshardctl local port")
	routePrefix := fs.String("route-prefix", "", "devshard route prefix (e.g. /devshard/v0.2.12)")
	amount := fs.Uint64("amount", 5_000_000_000, "escrow amount in ngonka")
	devshardctlBin := fs.String("devshardctl", "devshardctl", "path to devshardctl binary")
	stateFile := fs.String("state-file", "devshard-test-state.json", "path to state file for escrow ID persistence")
	reset := fs.Bool("reset", false, "ignore state file and create fresh escrows")
	grpcTLS := fs.Bool("grpc-tls", false, "use TLS for gRPC connection")
	chainID := fs.String("chain-id", defaultChainID, "chain ID")
	inferencesPerEscrow := fs.Int("inferences", 2, "number of inferences to send per escrow")
	skipHostsFlag := fs.String("skip-hosts", "", "comma-separated host addresses to skip (nonce is advanced past them)")
	finalize := fs.Bool("finalize", false, "finalize escrows after inferences and remove the state file")
	_ = fs.Parse(os.Args[1:])

	if *grpcAddr == "" || *rest == "" || *privateKey == "" || *routePrefix == "" {
		log.Fatal("--grpc, --rest, --private-key, and --route-prefix are required")
	}

	ctx := context.Background()

	address, err := deriveAddress(*privateKey)
	if err != nil {
		log.Fatalf("derive address: %v", err)
	}
	log.Printf("Using address: %s\n", address)

	var escrowIDs []uint64
	if !*reset {
		if s, err := loadState(*stateFile); err == nil {
			log.Printf("Loaded %d escrow IDs from %s\n", len(s.EscrowIDs), *stateFile)
			escrowIDs = filterOpenEscrows(*rest, s.EscrowIDs)
			if len(escrowIDs) < len(s.EscrowIDs) {
				log.Printf("  %d open, %d closed/missing\n", len(escrowIDs), len(s.EscrowIDs)-len(escrowIDs))
			}
		}
	}
	need := *count - len(escrowIDs)
	if need > 0 {
		log.Printf("Creating %d escrows on %s...\n", need, *grpcAddr)
		for i := 0; i < need; i++ {
			id, err := createEscrow(ctx, *grpcAddr, *chainID, *privateKey, *amount, *model, *grpcTLS)
			if err != nil {
				log.Fatalf("create escrow %d/%d: %v", i+1, need, err)
			}
			log.Printf("  escrow %d/%d created: id=%d\n", i+1, need, id)
			escrowIDs = append(escrowIDs, id)
		}
		if err := saveState(*stateFile, escrowIDs); err != nil {
			log.Fatalf("save state: %v", err)
		}
		log.Printf("Escrow IDs saved to %s\n", *stateFile)
	}

	skipHosts := parseSkipHosts(*skipHostsFlag)
	if err := runSession(escrowIDs, *devshardctlBin, *privateKey, *rest, *routePrefix, *model, *stateFile, *basePort, *inferencesPerEscrow, skipHosts, *finalize); err != nil {
		log.Printf("FAIL: %v", err)
		os.Exit(1)
	}
	log.Printf("PASS: all assertions passed")
}

func parseSkipHosts(flag string) map[string]bool {
	m := make(map[string]bool)
	for _, addr := range strings.Split(flag, ",") {
		if addr = strings.TrimSpace(addr); addr != "" {
			m[addr] = true
		}
	}
	return m
}

func runSession(escrowIDs []uint64, devshardctlBin, privateKey, rest, routePrefix, model, stateFile string, basePort, inferencesPerEscrow int, skipHosts map[string]bool, finalize bool) error {
	handles := make([]*proxyHandle, len(escrowIDs))
	defer func() {
		for _, h := range handles {
			if h != nil {
				h.stop()
			}
		}
	}()

	for i, id := range escrowIDs {
		port := basePort + i
		log.Printf("Starting devshardctl for escrow %d on port %d...\n", id, port)
		h, err := startProxy(devshardctlBin, id, privateKey, rest, routePrefix, port)
		if err != nil {
			return fmt.Errorf("start devshardctl for escrow %d: %w", id, err)
		}
		handles[i] = h
		log.Printf("  ready at %s\n", h.proxyURL)
	}

	responses := make([]string, len(handles))
	for i, h := range handles {
		slots := fetchEscrowSlots(rest, escrowIDs[i])
		sent := 0
		attempts := 0
		maxAttempts := inferencesPerEscrow + len(skipHosts)*10 + 20
		for sent < inferencesPerEscrow && attempts < maxAttempts {
			attempts++
			nonce, err := queryNonce(h.proxyURL)
			if err != nil {
				return fmt.Errorf("query nonce for escrow %d: %w", escrowIDs[i], err)
			}
			executor := "unknown"
			if len(slots) > 0 {
				executor = slots[(nonce+1)%uint64(len(slots))]
			}
			if skipHosts[executor] {
				log.Printf("Skipping inference for escrow %d (nonce %d → skipped host %s), firing async to advance nonce...\n", escrowIDs[i], nonce+1, executor)
				go sendInference(h.proxyURL, model)
				waitNonceAdvanced(h.proxyURL, nonce, 3*time.Second)
				continue
			}
			log.Printf("Sending inference %d/%d for escrow %d (nonce %d → host %s)...\n", sent+1, inferencesPerEscrow, escrowIDs[i], nonce+1, executor)
			resp, err := sendInference(h.proxyURL, model)
			if err != nil {
				log.Printf("  inference %d for escrow %d failed: %v (continuing)\n", sent+1, escrowIDs[i], err)
				continue
			}
			sent++
			responses[i] = resp
			preview := resp
			if len(preview) > 60 {
				preview = preview[:60] + "..."
			}
			log.Printf("  response: %q\n", preview)
		}
		if sent < inferencesPerEscrow {
			return fmt.Errorf("escrow %d completed %d/%d successful inferences after %d attempts", escrowIDs[i], sent, inferencesPerEscrow, attempts)
		}
	}

	if finalize {
		fmt.Println("Waiting 3s before finalization...")
		time.Sleep(3 * time.Second)
		return finalizeEscrows(handles, escrowIDs, stateFile, responses)
	}

	log.Printf("Finalization disabled; escrows stay open for manual inspection.")

	return nil
}

func finalizeEscrows(handles []*proxyHandle, escrowIDs []uint64, stateFile string, responses []string) error {
	settlements := make([]settlement, len(handles))
	for i, h := range handles {
		log.Printf("Finalizing escrow %d...\n", escrowIDs[i])
		s, err := finalizeProxy(h.proxyURL)
		if err != nil {
			return fmt.Errorf("finalize escrow %d: %w", escrowIDs[i], err)
		}
		settlements[i] = s
		log.Printf("  nonce=%d slots=%v\n", s.Nonce, slotIDsFromSettlement(s))
	}

	if err := assertResults(responses, settlements); err != nil {
		return err
	}

	// All escrows finalized — remove them from state so next run starts fresh.
	_ = os.Remove(stateFile)
	return nil
}

func slotIDsFromSettlement(s settlement) []uint32 {
	ids := make([]uint32, len(s.HostStats))
	for i, h := range s.HostStats {
		ids[i] = h.SlotID
	}
	return ids
}
