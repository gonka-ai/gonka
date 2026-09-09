package types

import (
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/gogoproto/proto"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestDecimalValidateExponentBounds(t *testing.T) {
	tests := []struct {
		exponent int32
		wantErr  bool
	}{
		{math.MinInt32, true},
		{-19, true},
		{-18, false},
		{0, false},
		{18, false},
		{19, true},
		{math.MaxInt32, true},
	}
	for _, tt := range tests {
		t.Run(strconv.FormatInt(int64(tt.exponent), 10), func(t *testing.T) {
			err := (&Decimal{Value: 1, Exponent: tt.exponent}).Validate()
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidDecimalExponent)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDecimalToLegacyDecRejectsExtremeExponentPromptly(t *testing.T) {
	assertPromptError(t, func() error {
		_, err := (&Decimal{Value: 1, Exponent: math.MinInt32}).ToLegacyDec()
		return err
	})
}

func TestMsgUpdateParamsRejectsExtremeExponentBeforeAuthorityCheck(t *testing.T) {
	params := DefaultParams()
	params.ValidationParams.FalsePositiveRate = &Decimal{Value: 1, Exponent: math.MaxInt32}
	msg := &MsgUpdateParams{Authority: sample.AccAddress(), Params: params}
	assertPromptError(t, msg.ValidateBasic)
}

func TestDefaultParamsValidateEveryDecimalExponent(t *testing.T) {
	params := DefaultParams()
	var decimals []*Decimal
	collectDecimals(reflect.ValueOf(&params), &decimals)
	require.NotEmpty(t, decimals)

	for i, value := range decimals {
		original := value.Exponent
		value.Exponent = MaxDecimalExponentAbs + 1
		require.ErrorIsf(t, params.Validate(), ErrInvalidDecimalExponent,
			"default params Decimal #%d is not checked", i)
		value.Exponent = original
	}
}

func TestRegisteredMessagesWithDecimalsRejectExtremeExponent(t *testing.T) {
	registry := codectypes.NewInterfaceRegistry()
	RegisterInterfaces(registry)

	constructors := map[string]func() sdk.Msg{
		"/inference.inference.MsgRegisterModel": func() sdk.Msg {
			return &MsgRegisterModel{
				Authority:           sample.AccAddress(),
				ProposedBy:          sample.AccAddress(),
				ValidationThreshold: &Decimal{Value: 1, Exponent: math.MaxInt32},
			}
		},
		"/inference.inference.MsgUpdateParams": func() sdk.Msg {
			params := DefaultParams()
			params.ValidationParams.FalsePositiveRate = &Decimal{Value: 1, Exponent: math.MaxInt32}
			return &MsgUpdateParams{Authority: sample.AccAddress(), Params: params}
		},
	}
	decimalType := reflect.TypeOf(Decimal{})

	for _, typeURL := range registry.ListImplementations("cosmos.base.v1beta1.Msg") {
		messageType := proto.MessageType(strings.TrimPrefix(typeURL, "/"))
		if messageType == nil || !containsType(messageType, decimalType, map[reflect.Type]bool{}) {
			continue
		}
		constructor, ok := constructors[typeURL]
		require.Truef(t, ok,
			"registered message %s contains Decimal; add bounded ValidateBasic coverage", typeURL)
		message := constructor()
		validator, ok := message.(interface{ ValidateBasic() error })
		require.Truef(t, ok, "%s does not implement ValidateBasic", typeURL)
		assertPromptError(t, validator.ValidateBasic)
	}
}

func assertPromptError(t *testing.T, validate func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- validate()
	}()
	select {
	case err := <-done:
		require.ErrorIs(t, err, ErrInvalidDecimalExponent)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("decimal validation did not return promptly")
	}
}

func collectDecimals(value reflect.Value, result *[]*Decimal) {
	if !value.IsValid() {
		return
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return
		}
		if value.Type() == reflect.TypeOf((*Decimal)(nil)) {
			*result = append(*result, value.Interface().(*Decimal))
			return
		}
		collectDecimals(value.Elem(), result)
		return
	}
	switch value.Kind() {
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			collectDecimals(value.Field(i), result)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			collectDecimals(value.Index(i), result)
		}
	}
}

func containsType(current, target reflect.Type, visited map[reflect.Type]bool) bool {
	for current.Kind() == reflect.Pointer || current.Kind() == reflect.Slice || current.Kind() == reflect.Array {
		current = current.Elem()
	}
	if current == target {
		return true
	}
	if current.Kind() != reflect.Struct || visited[current] {
		return false
	}
	visited[current] = true
	for i := 0; i < current.NumField(); i++ {
		if containsType(current.Field(i).Type, target, visited) {
			return true
		}
	}
	return false
}
