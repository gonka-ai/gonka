package types

import (
	"fmt"
	"reflect"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/gogoproto/proto"
)

const (
	PeriodTypeEpoch = "epoch"
	PeriodTypePoc   = "poc"
	PeriodTypeBlock = "block"

	maxMinGasPrice = uint64(1_000_000)
	// MaxPeriodBaseGas / MaxGasPerUnit are the Validate caps. Migration clamps
	// legacy uncapped rates down to these rather than persisting an invalid tree.
	MaxPeriodBaseGas = uint64(10_000_000) // 20× default StoreCommit 500k
	MaxGasPerUnit    = uint64(10_000)     // 100× default StoreCommit 100

	StoredBytesUnitB  = "b"
	StoredBytesUnitKB = "kb"
	StoredBytesUnitMB = "mb"
)

// DefaultFeeParams returns the default fee parameters.
// Groups ship compiled in; none are enabled. Global min_gas_price stays 0.
func DefaultFeeParams() *FeeParams {
	storeCommitURL := sdk.MsgTypeURL(&MsgPoCV2StoreCommit{})
	hdURL := sdk.MsgTypeURL(&MsgSubmitHardwareDiff{})
	return &FeeParams{
		MinGasPriceNgonka: 0,
		BaseValidationGas: 500_000,
		GasPerPocCount:    100,
		EnabledFeeGroups:  nil,
		Groups: []*FeeGroup{
			{
				Name:        FeeGroupEpoch,
				MinGasPrice: 0,
				Base:        &PeriodBase{Gas: 0, PeriodType: PeriodTypeEpoch, PeriodLength: 1},
				Msgs: []*MsgGasRule{
					{
						TypeUrl: storeCommitURL,
						Base:    &PeriodBase{Gas: 500_000, PeriodType: PeriodTypePoc, PeriodLength: 1},
						Func: &MsgGasRule_StoredDelta{
							StoredDelta: &StoredDeltaParams{
								GasPerUnit:  100,
								Items:       "entries",
								ValueField:  "count",
								IdField:     "model_id",
								ScopeFields: []string{"poc_stage_start_block_height"},
							},
						},
					},
					{
						TypeUrl: hdURL,
						Func: &MsgGasRule_StoredBytes{
							StoredBytes: &StoredBytesParams{GasPerUnit: 100, Unit: StoredBytesUnitKB},
						},
					},
				},
			},
		},
	}
}

// Validate checks that the fee parameters are well-formed.
func (fp *FeeParams) Validate() error {
	if fp == nil {
		return nil
	}
	if fp.MinGasPriceNgonka > maxMinGasPrice {
		return fmt.Errorf("min_gas_price_ngonka %d exceeds safety limit of %d", fp.MinGasPriceNgonka, maxMinGasPrice)
	}

	groupNames := make(map[string]struct{}, len(fp.Groups))
	seenTypeURLs := make(map[string]struct{})
	for i, g := range fp.Groups {
		if g == nil {
			return fmt.Errorf("groups[%d] is nil", i)
		}
		if g.Name == "" {
			return fmt.Errorf("groups[%d].name is empty", i)
		}
		if !IsKnownFeeGroup(g.Name) {
			return fmt.Errorf("groups[%d].name %q is not a compiled fee group", i, g.Name)
		}
		if _, dup := groupNames[g.Name]; dup {
			return fmt.Errorf("duplicate fee group name %q", g.Name)
		}
		groupNames[g.Name] = struct{}{}
		if g.MinGasPrice > maxMinGasPrice {
			return fmt.Errorf("groups[%q].min_gas_price %d exceeds safety limit of %d", g.Name, g.MinGasPrice, maxMinGasPrice)
		}
		if err := validatePeriodBase(g.Base, fmt.Sprintf("groups[%q].base", g.Name)); err != nil {
			return err
		}
		if err := validateChargingPeriod(g.Base, "", fmt.Sprintf("groups[%q].base", g.Name)); err != nil {
			return err
		}
		for j, rule := range g.Msgs {
			if rule == nil {
				return fmt.Errorf("groups[%q].msgs[%d] is nil", g.Name, j)
			}
			if rule.TypeUrl == "" {
				return fmt.Errorf("groups[%q].msgs[%d].type_url is empty", g.Name, j)
			}
			if _, dup := seenTypeURLs[rule.TypeUrl]; dup {
				return fmt.Errorf("duplicate type_url %q in fee groups", rule.TypeUrl)
			}
			seenTypeURLs[rule.TypeUrl] = struct{}{}
			compiled := CompiledFeeGroupForTypeURL(rule.TypeUrl)
			if compiled == "" {
				return fmt.Errorf("groups[%q].msgs[%d].type_url %q is not a fee-grouped message", g.Name, j, rule.TypeUrl)
			}
			if compiled != g.Name {
				return fmt.Errorf("groups[%q].msgs[%d].type_url %q belongs to compiled group %q", g.Name, j, rule.TypeUrl, compiled)
			}
			if err := validatePeriodBase(rule.Base, fmt.Sprintf("groups[%q].msgs[%d].base", g.Name, j)); err != nil {
				return err
			}
			if err := validateChargingPeriod(rule.Base, rule.TypeUrl, fmt.Sprintf("groups[%q].msgs[%d].base", g.Name, j)); err != nil {
				return err
			}
			if err := validateMsgGasRuleFunc(rule); err != nil {
				return fmt.Errorf("groups[%q].msgs[%d]: %w", g.Name, j, err)
			}
		}
	}

	for _, name := range fp.EnabledFeeGroups {
		if name == "" {
			return fmt.Errorf("enabled_fee_groups contains an empty name")
		}
		if !IsKnownFeeGroup(name) {
			return fmt.Errorf("unknown enabled fee group %q", name)
		}
		if _, ok := groupNames[name]; !ok {
			return fmt.Errorf("enabled fee group %q has no groups[] entry", name)
		}
	}
	return nil
}

func validatePeriodBase(base *PeriodBase, loc string) error {
	if base == nil {
		return nil
	}
	// Do not rewrite period_length: MsgUpdateParams.ValidateBasic runs on the
	// governance message itself. Treat omitted 0 as 1 only at use time
	// (PeriodLengthOrDefault / validateChargingPeriod).
	if base.Gas > MaxPeriodBaseGas {
		return fmt.Errorf("%s.gas %d exceeds safety limit of %d", loc, base.Gas, MaxPeriodBaseGas)
	}
	if base.PeriodType == "" {
		return nil
	}
	switch base.PeriodType {
	case PeriodTypeEpoch, PeriodTypePoc, PeriodTypeBlock:
		return nil
	default:
		return fmt.Errorf("%s.period_type %q is invalid (want epoch|poc|block)", loc, base.PeriodType)
	}
}

func validateChargingPeriod(base *PeriodBase, typeURL, loc string) error {
	if base == nil || base.Gas == 0 {
		return nil
	}
	storeCommitURL := sdk.MsgTypeURL(&MsgPoCV2StoreCommit{})
	if typeURL != storeCommitURL {
		return fmt.Errorf("%s: nonzero period gas is only supported on StoreCommit (poc/1)", loc)
	}
	if base.PeriodType != PeriodTypePoc {
		return fmt.Errorf("%s: nonzero period gas requires period_type=poc", loc)
	}
	length := base.PeriodLength
	if length == 0 {
		length = 1
	}
	if length != 1 {
		return fmt.Errorf("%s: nonzero period gas requires period_length=1", loc)
	}
	return nil
}

func validateGasPerUnit(rate uint64, loc string) error {
	if rate > MaxGasPerUnit {
		return fmt.Errorf("%s %d exceeds safety limit of %d", loc, rate, MaxGasPerUnit)
	}
	return nil
}

// ClampFeeTreeSafetyLimits caps period-base gas and gas_per_unit to the
// Validate limits. Returns true if any value was reduced. Used by the v0.2.16
// migration so previously-valid uncapped legacy fields cannot persist a tree
// that later MsgUpdateParams.Validate would reject.
func ClampFeeTreeSafetyLimits(fp *FeeParams) bool {
	if fp == nil {
		return false
	}
	changed := false
	clamp := func(v *uint64, max uint64) {
		if v != nil && *v > max {
			*v = max
			changed = true
		}
	}
	clamp(&fp.BaseValidationGas, MaxPeriodBaseGas)
	clamp(&fp.GasPerPocCount, MaxGasPerUnit)
	clampBase := func(base *PeriodBase) {
		if base != nil {
			clamp(&base.Gas, MaxPeriodBaseGas)
		}
	}
	for _, g := range fp.Groups {
		if g == nil {
			continue
		}
		clampBase(g.Base)
		for _, rule := range g.Msgs {
			if rule == nil {
				continue
			}
			clampBase(rule.Base)
			if d := rule.GetStoredDelta(); d != nil {
				clamp(&d.GasPerUnit, MaxGasPerUnit)
			}
			if b := rule.GetStoredBytes(); b != nil {
				clamp(&b.GasPerUnit, MaxGasPerUnit)
			}
			if r := rule.GetRepeatedLen(); r != nil {
				clamp(&r.GasPerUnit, MaxGasPerUnit)
			}
		}
	}
	return changed
}

// StoredBytesUnitSize returns the byte divisor for stored_bytes.unit.
// Empty unit is "b" (per byte). kb=1000, mb=1_000_000 (decimal, not 1024).
func StoredBytesUnitSize(unit string) (uint64, bool) {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "", StoredBytesUnitB:
		return 1, true
	case StoredBytesUnitKB:
		return 1000, true
	case StoredBytesUnitMB:
		return 1_000_000, true
	default:
		return 0, false
	}
}

func ExtraGasForStoredBytes(deltaBytes, gasPerUnit uint64, unit string) uint64 {
	if deltaBytes == 0 || gasPerUnit == 0 {
		return 0
	}
	div, ok := StoredBytesUnitSize(unit)
	if !ok || div == 0 {
		return 0
	}
	prod := deltaBytes * gasPerUnit
	if prod/deltaBytes != gasPerUnit {
		return ^uint64(0)
	}
	return prod / div
}

func validateMsgGasRuleFunc(rule *MsgGasRule) error {
	storeCommitURL := sdk.MsgTypeURL(&MsgPoCV2StoreCommit{})
	hdURL := sdk.MsgTypeURL(&MsgSubmitHardwareDiff{})

	switch f := rule.GetFunc().(type) {
	case nil:
		return nil
	case *MsgGasRule_StoredDelta:
		if f.StoredDelta == nil {
			return fmt.Errorf("stored_delta is nil")
		}
		if rule.TypeUrl != storeCommitURL {
			return fmt.Errorf("stored_delta is only supported on StoreCommit")
		}
		if err := validateGasPerUnit(f.StoredDelta.GasPerUnit, "stored_delta.gas_per_unit"); err != nil {
			return err
		}
		return validateStoredDelta(rule.TypeUrl, f.StoredDelta)
	case *MsgGasRule_StoredBytes:
		if f.StoredBytes == nil {
			return fmt.Errorf("stored_bytes is nil")
		}
		if rule.TypeUrl != hdURL {
			return fmt.Errorf("stored_bytes is only supported on HardwareDiff")
		}
		if err := validateGasPerUnit(f.StoredBytes.GasPerUnit, "stored_bytes.gas_per_unit"); err != nil {
			return err
		}
		if _, ok := StoredBytesUnitSize(f.StoredBytes.Unit); !ok {
			return fmt.Errorf("stored_bytes.unit %q is invalid (want b|kb|mb)", f.StoredBytes.Unit)
		}
		return nil
	case *MsgGasRule_RepeatedLen:
		if f.RepeatedLen == nil {
			return fmt.Errorf("repeated_len is nil")
		}
		if f.RepeatedLen.Field == "" {
			return fmt.Errorf("repeated_len.field is empty")
		}
		if err := validateGasPerUnit(f.RepeatedLen.GasPerUnit, "repeated_len.gas_per_unit"); err != nil {
			return err
		}
		if rule.TypeUrl == storeCommitURL || rule.TypeUrl == hdURL {
			return fmt.Errorf("repeated_len is not supported on %s (use stored_delta / stored_bytes)", rule.TypeUrl)
		}
		msg := prototypeForTypeURL(rule.TypeUrl)
		if msg == nil {
			return fmt.Errorf("repeated_len.field cannot be verified on unknown type %s", rule.TypeUrl)
		}
		if !protoHasField(msg, f.RepeatedLen.Field) {
			return fmt.Errorf("repeated_len.field %q not found on %s", f.RepeatedLen.Field, rule.TypeUrl)
		}
		if !protoFieldIsRepeated(msg, f.RepeatedLen.Field) {
			return fmt.Errorf("repeated_len.field %q is not a repeated field on %s", f.RepeatedLen.Field, rule.TypeUrl)
		}
		return nil
	default:
		return fmt.Errorf("unknown gas rule func")
	}
}

// validateStoredDelta checks proto field paths only. Quantity still comes from
// the handler (canonical Count delta / inventory byte growth), not these paths.
func validateStoredDelta(typeURL string, p *StoredDeltaParams) error {
	if p.Items == "" {
		return fmt.Errorf("stored_delta.items is empty")
	}
	if p.ValueField == "" {
		return fmt.Errorf("stored_delta.value_field is empty")
	}
	msg := prototypeForTypeURL(typeURL)
	if msg == nil {
		return nil
	}
	if !protoHasField(msg, p.Items) {
		return fmt.Errorf("stored_delta.items %q not found on %s", p.Items, typeURL)
	}
	if !protoFieldIsRepeated(msg, p.Items) {
		return fmt.Errorf("stored_delta.items %q is not a repeated field on %s", p.Items, typeURL)
	}
	elem := protoFieldElemType(msg, p.Items)
	if elem != nil {
		elemMsg, ok := reflect.New(elem).Interface().(proto.Message)
		if ok {
			if !protoHasField(elemMsg, p.ValueField) {
				return fmt.Errorf("stored_delta.value_field %q not found on items type", p.ValueField)
			}
			if !protoFieldIsUnsignedInt(elemMsg, p.ValueField) {
				return fmt.Errorf("stored_delta.value_field %q is not an unsigned integer", p.ValueField)
			}
			if p.IdField != "" && !protoHasField(elemMsg, p.IdField) {
				return fmt.Errorf("stored_delta.id_field %q not found on items type", p.IdField)
			}
		}
	}
	for _, sf := range p.ScopeFields {
		if !protoHasField(msg, sf) {
			return fmt.Errorf("stored_delta.scope_fields %q not found on %s", sf, typeURL)
		}
	}
	return nil
}

func prototypeForTypeURL(typeURL string) proto.Message {
	switch typeURL {
	case sdk.MsgTypeURL(&MsgPoCV2StoreCommit{}):
		return &MsgPoCV2StoreCommit{}
	case sdk.MsgTypeURL(&MsgSubmitHardwareDiff{}):
		return &MsgSubmitHardwareDiff{}
	case sdk.MsgTypeURL(&MsgSubmitPocValidationsV2{}):
		return &MsgSubmitPocValidationsV2{}
	case sdk.MsgTypeURL(&MsgSubmitSeed{}):
		return &MsgSubmitSeed{}
	case sdk.MsgTypeURL(&MsgClaimRewards{}):
		return &MsgClaimRewards{}
	case sdk.MsgTypeURL(&MsgMLNodeWeightDistribution{}):
		return &MsgMLNodeWeightDistribution{}
	case sdk.MsgTypeURL(&MsgDeclarePoCIntent{}):
		return &MsgDeclarePoCIntent{}
	default:
		name := strings.TrimPrefix(typeURL, "/")
		if t := proto.MessageType(name); t != nil {
			v := reflect.New(t.Elem())
			if m, ok := v.Interface().(proto.Message); ok {
				return m
			}
		}
		return nil
	}
}

func protoStructField(msg proto.Message, name string) (reflect.StructField, bool) {
	if msg == nil || name == "" {
		return reflect.StructField{}, false
	}
	t := reflect.TypeOf(msg)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return reflect.StructField{}, false
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if protoTagName(f) == name {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

func protoTagName(f reflect.StructField) string {
	for _, part := range strings.Split(f.Tag.Get("protobuf"), ",") {
		if strings.HasPrefix(part, "name=") {
			return strings.TrimPrefix(part, "name=")
		}
	}
	return ""
}

func protoTagIsRepeated(f reflect.StructField) bool {
	for _, part := range strings.Split(f.Tag.Get("protobuf"), ",") {
		if part == "rep" {
			return true
		}
	}
	return false
}

func protoHasField(msg proto.Message, name string) bool {
	_, ok := protoStructField(msg, name)
	return ok
}

func protoFieldIsRepeated(msg proto.Message, name string) bool {
	f, ok := protoStructField(msg, name)
	return ok && protoTagIsRepeated(f)
}

func protoFieldIsUnsignedInt(msg proto.Message, name string) bool {
	f, ok := protoStructField(msg, name)
	if !ok {
		return false
	}
	switch f.Type.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	default:
		return false
	}
}

// RepeatedFieldLen returns len of the named protobuf repeated field.
// ok is false when the field is missing or not repeated.
func RepeatedFieldLen(msg any, name string) (uint64, bool) {
	pm, ok := msg.(proto.Message)
	if !ok || pm == nil || name == "" {
		return 0, false
	}
	f, ok := protoStructField(pm, name)
	if !ok || !protoTagIsRepeated(f) {
		return 0, false
	}
	v := reflect.ValueOf(pm)
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return 0, false
		}
		v = v.Elem()
	}
	fv := v.FieldByIndex(f.Index)
	if !fv.IsValid() || (fv.Kind() != reflect.Slice && fv.Kind() != reflect.Array) {
		return 0, false
	}
	n := fv.Len()
	if n < 0 {
		return 0, false
	}
	return uint64(n), true
}

func protoFieldElemType(msg proto.Message, name string) reflect.Type {
	f, ok := protoStructField(msg, name)
	if !ok {
		return nil
	}
	ft := f.Type
	if ft.Kind() == reflect.Slice {
		ft = ft.Elem()
	}
	if ft.Kind() == reflect.Ptr {
		ft = ft.Elem()
	}
	return ft
}

// IsGroupEnabled reports whether name is in enabled_fee_groups.
func (fp *FeeParams) IsGroupEnabled(name string) bool {
	if fp == nil || name == "" {
		return false
	}
	for _, g := range fp.EnabledFeeGroups {
		if g == name {
			return true
		}
	}
	return false
}

// GroupByName returns the named FeeGroup, or nil.
func (fp *FeeParams) GroupByName(name string) *FeeGroup {
	if fp == nil {
		return nil
	}
	for _, g := range fp.Groups {
		if g != nil && g.Name == name {
			return g
		}
	}
	return nil
}

// RuleForTypeURL returns the group and msg rule for typeURL, or (nil, nil).
func (fp *FeeParams) RuleForTypeURL(typeURL string) (*FeeGroup, *MsgGasRule) {
	if fp == nil {
		return nil, nil
	}
	for _, g := range fp.Groups {
		if g == nil {
			continue
		}
		for _, rule := range g.Msgs {
			if rule != nil && rule.TypeUrl == typeURL {
				return g, rule
			}
		}
	}
	return nil, nil
}

// EnabledPayingPrice returns the max min_gas_price of enabled groups that
// contain a non-exempt inner message. 0 means the tx is free.
func (fp *FeeParams) EnabledPayingPrice(msgs []sdk.Msg, isExempt func(sdk.Msg) bool) uint64 {
	if fp == nil {
		return 0
	}
	var price uint64
	for _, msg := range msgs {
		if msg == nil {
			continue
		}
		if isExempt != nil && isExempt(msg) {
			continue
		}
		g := FeeGroupOf(msg)
		if g == "" || !fp.IsGroupEnabled(g) {
			continue
		}
		grp := fp.GroupByName(g)
		if grp == nil {
			continue
		}
		if grp.MinGasPrice > price {
			price = grp.MinGasPrice
		}
	}
	return price
}

// ResolvedPeriodBase returns the msg-level PeriodBase if set (including gas=0
// opt-out), otherwise the group base.
func ResolvedPeriodBase(group *FeeGroup, rule *MsgGasRule) *PeriodBase {
	if rule != nil && rule.Base != nil {
		return rule.Base
	}
	if group != nil {
		return group.Base
	}
	return nil
}

// PeriodLengthOrDefault returns period_length, treating 0 as 1.
func PeriodLengthOrDefault(base *PeriodBase) uint64 {
	if base == nil || base.PeriodLength == 0 {
		return 1
	}
	return base.PeriodLength
}
