package env

import "testing"

// Test flow:
//  1. Table-driven: each case sets the deployed variable for allowing private addresses, expecting the guard to stay on unless explicitly opted out.
//  2. For each case, set the variable and call `AllowPrivateAddresses`.
//  3. Assert the result matches the case's expectation, including the lenient boolean spellings (on/yes/1) and any non-boolean value keeping the guard on.
func TestPrivateAddressesAreRefusedUnlessTheStandAsksForThem(t *testing.T) {
	testCases := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "unset keeps the guard on", want: false},
		{name: "false keeps the guard on", raw: "false", want: false},
		{name: "true opts the process out", raw: "true", want: true},
		{name: "the on spelling opts out too", raw: "on", want: true},
		{name: "a value that is not a boolean keeps the guard on", raw: "maybe", want: false},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(AllowPrivateAddressesVariable, testCase.raw)

			if got := AllowPrivateAddresses(); got != testCase.want {
				t.Fatalf("AllowPrivateAddresses() = %v, want %v", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Set the renamed gateway spelling to true.
//  2. Assert `AllowPrivateAddresses` keeps the guard on, since only the deployed name is read.
func TestTheRenamedPrivateAddressSpellingIsNotRead(t *testing.T) {
	t.Setenv("GATEWAY_ALLOW_PRIVATE_ADDRESSES", "true")

	if AllowPrivateAddresses() {
		t.Fatal("AllowPrivateAddresses() = true from GATEWAY_ALLOW_PRIVATE_ADDRESSES, want the guard on")
	}
}
