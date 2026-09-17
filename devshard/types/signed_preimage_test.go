package types

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"

	"devshard/signing"
)

// signedProtoFullNames are proto messages whose deterministic encoding is a
// secp256k1 preimage (possibly with a domain prefix). A new signed message
// must be listed here; TestAllDevshardProtoMessagesClassified will fail until
// it is classified as signed or unsigned.
var signedProtoFullNames = map[string]struct{}{
	"devshard.v1.DiffContent":                {},
	"devshard.v1.ExecutorReceiptContent":     {},
	"devshard.v1.TimeoutVoteContent":         {},
	"devshard.v1.ErrorMissVoteContent":       {},
	"devshard.v1.StateSignatureContent":      {},
	"devshard.v1.MsgFinishInference":         {},
	"devshard.v1.MsgValidation":              {},
	"devshard.v1.MsgValidationVote":          {},
	"devshard.v1.MsgHeightAck":               {},
	"devshard.v1.InferenceHeightSyncSection": {},
}

// unsignedProtoFullNames is every other message in proto/devshard/v1. Map
// entries generated for proto maps are ignored by the walker.
var unsignedProtoFullNames = map[string]struct{}{
	"devshard.v1.MsgStartInference":         {},
	"devshard.v1.MsgConfirmStart":           {},
	"devshard.v1.MsgTimeoutInference":       {},
	"devshard.v1.TimeoutVote":               {},
	"devshard.v1.MsgErrorMiss":              {},
	"devshard.v1.ErrorMissVote":             {},
	"devshard.v1.MsgFinalizeRound":          {},
	"devshard.v1.MsgRevealSeed":             {},
	"devshard.v1.MsgForceHeightSyncTurn":    {},
	"devshard.v1.DevshardTx":                {},
	"devshard.v1.MsgHeartbeat":              {},
	"devshard.v1.SyncVectorEntry":           {},
	"devshard.v1.HostStatsProto":            {},
	"devshard.v1.HostStatsMapProto":         {},
	"devshard.v1.InferenceRecordProto":      {},
	"devshard.v1.InferencesMapProto":        {},
	"devshard.v1.SessionConfigProto":        {},
	"devshard.v1.SlotAssignmentProto":       {},
	"devshard.v1.EscrowStateProto":          {},
	"devshard.v1.FloorIndexEntryProto":      {},
	"devshard.v1.FloorIndexProto":           {},
	"devshard.v1.StateSnapshotProto":        {},
	"devshard.v1.InferenceRequestEnvelope":  {},
	"devshard.v1.InferenceResponseEnvelope": {},
}

// protoSigFields are zeroed before signing and are not part of the preimage.
var protoSigFields = map[string][]protoreflect.FieldNumber{
	"devshard.v1.MsgFinishInference":         {6},
	"devshard.v1.MsgValidation":              {4},
	"devshard.v1.MsgValidationVote":          {4},
	"devshard.v1.MsgHeightAck":               {8},
	"devshard.v1.InferenceHeightSyncSection": {8},
}

// heightsyncDomains are prefixed by heightsync.Canonical* helpers, not
// CanonicalSignedBytes. Keep in sync with heightsync.DomainHeightAck and
// OriginSignDomain.
var heightsyncDomains = map[string]string{
	"devshard.v1.MsgHeightAck":               "heightsync.ack.v1",
	"devshard.v1.InferenceHeightSyncSection": "heightsync.origin.v1",
}

func allDevshardFileDescriptors() []protoreflect.FileDescriptor {
	return []protoreflect.FileDescriptor{
		File_devshard_v1_tx_proto,
		File_devshard_v1_diff_proto,
		File_devshard_v1_state_proto,
		File_devshard_v1_snapshot_proto,
		File_devshard_v1_inference_envelope_proto,
	}
}

func allDevshardMessages(t *testing.T) []protoreflect.MessageDescriptor {
	t.Helper()
	var out []protoreflect.MessageDescriptor
	seen := map[string]struct{}{}
	var walk func(protoreflect.MessageDescriptors)
	walk = func(msgs protoreflect.MessageDescriptors) {
		for i := 0; i < msgs.Len(); i++ {
			md := msgs.Get(i)
			name := string(md.FullName())
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, md)
			walk(md.Messages())
		}
	}
	for _, fd := range allDevshardFileDescriptors() {
		if fd == nil {
			t.Fatal("missing generated file descriptor")
		}
		walk(fd.Messages())
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].FullName() < out[j].FullName()
	})
	return out
}

func fieldWireType(fd protoreflect.FieldDescriptor) protowire.Type {
	if fd.IsMap() || (fd.IsList() && fd.IsPacked()) {
		return protowire.BytesType
	}
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind, protoreflect.BytesKind, protoreflect.StringKind:
		return protowire.BytesType
	case protoreflect.Fixed32Kind, protoreflect.Sfixed32Kind, protoreflect.FloatKind:
		return protowire.Fixed32Type
	case protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind, protoreflect.DoubleKind:
		return protowire.Fixed64Type
	default:
		return protowire.VarintType
	}
}

func skipSet(name string) map[protoreflect.FieldNumber]struct{} {
	out := map[protoreflect.FieldNumber]struct{}{}
	for _, n := range protoSigFields[name] {
		out[n] = struct{}{}
	}
	return out
}

// fingerprint is the sorted (field number, wire type) layout proto3 encodes.
// Signature fields that are zeroed before signing are omitted. Two messages
// with the same fingerprint, or one a subset of the other, can produce
// identical bytes because proto3 omits defaults.
type fingerprint [][2]int

func layoutFingerprint(md protoreflect.MessageDescriptor) fingerprint {
	skip := skipSet(string(md.FullName()))
	fields := md.Fields()
	fp := make(fingerprint, 0, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if _, drop := skip[fd.Number()]; drop {
			continue
		}
		fp = append(fp, [2]int{int(fd.Number()), int(fieldWireType(fd))})
	}
	sort.Slice(fp, func(i, j int) bool {
		if fp[i][0] != fp[j][0] {
			return fp[i][0] < fp[j][0]
		}
		return fp[i][1] < fp[j][1]
	})
	return fp
}

func (fp fingerprint) String() string {
	parts := make([]string, len(fp))
	for i, p := range fp {
		parts[i] = fmt.Sprintf("%d:%d", p[0], p[1])
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func subsetFingerprint(a, b fingerprint) bool {
	if len(a) == 0 || len(a) > len(b) {
		return false
	}
	j := 0
	for i := range a {
		for j < len(b) && (b[j][0] < a[i][0] || (b[j][0] == a[i][0] && b[j][1] < a[i][1])) {
			j++
		}
		if j >= len(b) || b[j] != a[i] {
			return false
		}
		j++
	}
	return true
}

func signedDomainFor(name string) string {
	if d, ok := heightsyncDomains[name]; ok {
		return d
	}
	return signedPreimageDomain[protoreflect.FullName(name)]
}

func TestAllDevshardProtoMessagesClassified(t *testing.T) {
	classified := make(map[string]string, len(signedProtoFullNames)+len(unsignedProtoFullNames))
	for name := range signedProtoFullNames {
		if _, dup := unsignedProtoFullNames[name]; dup {
			t.Errorf("%s is listed as both signed and unsigned", name)
		}
		classified[name] = "signed"
	}
	for name := range unsignedProtoFullNames {
		classified[name] = "unsigned"
	}

	seen := map[string]struct{}{}
	for _, md := range allDevshardMessages(t) {
		if md.IsMapEntry() {
			continue
		}
		name := string(md.FullName())
		seen[name] = struct{}{}
		if _, ok := classified[name]; !ok {
			t.Errorf("proto message %s is not classified: add it to signedProtoFullNames or unsignedProtoFullNames in signed_preimage_test.go", name)
		}
	}
	for name := range classified {
		if _, ok := seen[name]; !ok {
			t.Errorf("classified proto %s is not in the generated descriptors", name)
		}
	}
}

func TestSignedProtoPreimagesAreNotInterchangeable(t *testing.T) {
	type spec struct {
		name   string
		domain string
		fp     fingerprint
	}
	var specs []spec
	for _, md := range allDevshardMessages(t) {
		if md.IsMapEntry() {
			continue
		}
		name := string(md.FullName())
		if _, ok := signedProtoFullNames[name]; !ok {
			continue
		}
		specs = append(specs, spec{name: name, domain: signedDomainFor(name), fp: layoutFingerprint(md)})
	}
	if len(specs) != len(signedProtoFullNames) {
		t.Fatalf("classified %d signed messages, registry has %d", len(specs), len(signedProtoFullNames))
	}

	for i, a := range specs {
		for _, b := range specs[i+1:] {
			if a.domain != b.domain {
				continue
			}
			same := len(a.fp) == len(b.fp) && subsetFingerprint(a.fp, b.fp)
			if same || subsetFingerprint(a.fp, b.fp) || subsetFingerprint(b.fp, a.fp) {
				t.Errorf("signed proto preimages %s and %s are interchangeable (domain %q):\n  %s %s\n  %s %s\nprefix a unique domain tag via CanonicalSignedBytes or give them disjoint field numbers/wire types",
					a.name, b.name, a.domain, a.name, a.fp, b.name, b.fp)
			}
		}
	}
}

func TestCanonicalSignedBytesRejectsValidationVoteReplay(t *testing.T) {
	signer, err := signing.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	verifier := signing.NewSecp256k1Verifier()

	val := &MsgValidation{
		InferenceId:   7,
		ValidatorSlot: 2,
		Valid:         true,
		EscrowId:      "escrow-1",
	}
	preimage, err := CanonicalSignedBytes(val)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := signer.Sign(preimage)
	if err != nil {
		t.Fatal(err)
	}

	vote := &MsgValidationVote{
		InferenceId: 7,
		VoterSlot:   2,
		VoteValid:   true,
		EscrowId:    "escrow-1",
	}
	votePreimage, err := CanonicalSignedBytes(vote)
	if err != nil {
		t.Fatal(err)
	}
	if string(preimage) == string(votePreimage) {
		t.Fatal("validation and validation-vote preimages must not be equal")
	}

	recovered, err := verifier.RecoverAddress(votePreimage, sig)
	if err != nil {
		return
	}
	if recovered == signer.Address() {
		t.Fatal("validation signature verified as a validation vote")
	}
}

func TestCanonicalSignedBytesRejectsTimeoutErrorMissReplay(t *testing.T) {
	signer, err := signing.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	verifier := signing.NewSecp256k1Verifier()

	timeout := &TimeoutVoteContent{
		EscrowId:    "escrow-1",
		InferenceId: 1,
		Reason:      TimeoutReason_TIMEOUT_REASON_REFUSED,
		Accept:      false,
	}
	preimage, err := CanonicalSignedBytes(timeout)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := signer.Sign(preimage)
	if err != nil {
		t.Fatal(err)
	}

	miss := &ErrorMissVoteContent{
		EscrowId:    "escrow-1",
		InferenceId: 1,
		Accept:      true, // field 3 varint 1, same as REFUSED
	}
	missPreimage, err := CanonicalSignedBytes(miss)
	if err != nil {
		t.Fatal(err)
	}
	if string(preimage) == string(missPreimage) {
		t.Fatal("timeout reject and error-miss accept must not share a preimage")
	}
	recovered, err := verifier.RecoverAddress(missPreimage, sig)
	if err != nil {
		return
	}
	if recovered == signer.Address() {
		t.Fatal("timeout-vote signature verified as an error-miss vote")
	}
}

func TestSignedPreimageDomainsAreUnique(t *testing.T) {
	seen := map[string]string{}
	for name, domain := range signedPreimageDomain {
		if domain == "" {
			t.Errorf("%s listed with empty domain", name)
			continue
		}
		if other, ok := seen[domain]; ok {
			t.Errorf("domain %q used by both %s and %s", domain, other, name)
		}
		seen[domain] = string(name)
	}
	for name, domain := range heightsyncDomains {
		if other, ok := seen[domain]; ok {
			t.Errorf("domain %q used by both %s and %s", domain, other, name)
		}
		seen[domain] = name
	}
}

func TestCanonicalSignedBytes_Nil(t *testing.T) {
	if _, err := CanonicalSignedBytes(nil); err == nil {
		t.Fatal("expected error")
	}
}
