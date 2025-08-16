# Bank Module Send Restrictions

The bank module in Cosmos SDK provides a sophisticated system for other modules to set send restrictions on token transfers. This document explains how modules can implement and configure send restrictions.

## Send Restriction Function Interface

Other modules can set send restrictions by implementing the `SendRestrictionFn` interface:

```golang
type SendRestrictionFn func(ctx context.Context, fromAddr, toAddr sdk.AccAddress, amt sdk.Coins) (newToAddr sdk.AccAddress, err error)
```

This function:
- Takes the transaction context, sender address, recipient address, and coin amount
- Can either **restrict the transfer** (by returning an error) or **modify the recipient address**
- Returns the (potentially modified) recipient address and any error

## How Send Restrictions Are Applied

Send restrictions are applied during:
1. **`SendCoins`**: Applied before coins are removed from the sender and added to the recipient
2. **`InputOutputCoins`**: Applied after input coins are removed and once for each output before funds are added

### Important Limitations

- Send restrictions are **NOT** applied to `ModuleToAccount` or `ModuleToModule` transfers
- This design choice allows modules to move funds freely for rewards, governance, and other protocol operations
- Prevents potential chain halts if tokens are disabled but need to be moved in begin/endblock operations

## Two Ways to Set Send Restrictions

### 1. Direct Method (Legacy)

Modules can directly call the bank keeper's restriction methods:

```golang
// In your module's keeper constructor
func NewKeeper(cdc codec.BinaryCodec, storeKey storetypes.StoreKey, bankKeeper mymodule.BankKeeper) Keeper {
    rv := Keeper{/*...*/}
    bankKeeper.AppendSendRestriction(rv.SendRestrictionFn)
    return rv
}

// Implement the restriction function
var _ banktypes.SendRestrictionFn = Keeper{}.SendRestrictionFn

func (k Keeper) SendRestrictionFn(ctx context.Context, fromAddr, toAddr sdk.AccAddress, amt sdk.Coins) (sdk.AccAddress, error) {
    // Bypass if the context says to
    if mymodule.HasBypass(ctx) {
        return toAddr, nil
    }
    
    // Your custom send restriction logic goes here
    // Example: block transfers of certain denominations
    for _, coin := range amt {
        if coin.Denom == "restricted-token" {
            return nil, errors.New("transfers of restricted-token are not allowed")
        }
    }
    
    return toAddr, nil
}
```

### 2. Dependency Injection Method (Modern/Preferred)

The preferred approach uses dependency injection through the module system:

**Step 1: Module provides a SendRestrictionFn**

```golang
// In your module's ProvideModule function
func ProvideModule(in ModuleInputs) ModuleOutputs {
    keeper := NewKeeper(/*...*/)
    
    return ModuleOutputs{
        Keeper:            keeper,
        Module:            NewAppModule(keeper),
        SendRestrictionFn: keeper.SendRestrictionFn, // Provide the restriction function
    }
}

// Make sure your ModuleOutputs struct includes the restriction
type ModuleOutputs struct {
    depinject.Out
    
    Keeper            keeper.Keeper
    Module            appmodule.AppModule
    SendRestrictionFn banktypes.SendRestrictionFn `group:"bank-send-restrictions"`
}
```

**Step 2: Bank module automatically collects restrictions**

The bank module's `InvokeSetSendRestrictions` function:
- Collects all `SendRestrictionFn` instances from modules via dependency injection
- Applies them in a configurable order using `restrictions_order` in the bank module config
- If no order is specified, applies them alphabetically by module name

## Module Configuration

You can configure the order of send restrictions in your app configuration:

### App Config (YAML)
```yaml
modules:
  bank:
    restrictions_order: ["module1", "module2", "module3"]
```

### App Config (Go)
```golang
bankConfig := &bankmodulev1.Module{
    RestrictionsOrder: []string{"module1", "module2", "module3"},
}
```

## Restriction Composition

Multiple send restrictions are composed together using:
- **`AppendSendRestriction`**: Adds restriction to run **after** existing ones
- **`PrependSendRestriction`**: Adds restriction to run **before** existing ones

The composition **short-circuits** on errors - if one restriction returns an error, subsequent ones are not executed.

### Composition Example

```golang
// If you have restrictions A, B, C applied in that order:
// 1. Restriction A is called first
// 2. If A returns an error, B and C are not called
// 3. If A succeeds, B is called with A's output address
// 4. If B succeeds, C is called with B's output address
// 5. Final result is C's output (if all succeed)
```

## Best Practices

### 1. Context Bypass Pattern

Always implement a bypass mechanism using context values:

```golang
const bypassKey = "bypass-mymodule-restriction"

// WithBypass returns a new context that will cause the mymodule bank send restriction to be skipped
func WithBypass(ctx context.Context) context.Context {
    return sdk.UnwrapSDKContext(ctx).WithValue(bypassKey, true)
}

// WithoutBypass returns a new context that will cause the mymodule bank send restriction to not be skipped
func WithoutBypass(ctx context.Context) context.Context {
    return sdk.UnwrapSDKContext(ctx).WithValue(bypassKey, false)
}

// HasBypass checks the context to see if the mymodule bank send restriction should be skipped
func HasBypass(ctx context.Context) bool {
    bypassValue := ctx.Value(bypassKey)
    if bypassValue == nil {
        return false
    }
    bypass, isBool := bypassValue.(bool)
    return isBool && bypass
}
```

### 2. Use Bypasses for Module Operations

When your module needs to transfer funds internally without triggering its own restrictions:

```golang
func (k Keeper) DoInternalTransfer(ctx context.Context, fromAddr, toAddr sdk.AccAddress, amt sdk.Coins) error {
    // Use bypass to avoid triggering our own send restriction
    return k.bankKeeper.SendCoins(mymodule.WithBypass(ctx), fromAddr, toAddr, amt)
}
```

### 3. Address Redirection

Send restrictions can redirect transfers to different addresses:

```golang
func (k Keeper) SendRestrictionFn(ctx context.Context, fromAddr, toAddr sdk.AccAddress, amt sdk.Coins) (sdk.AccAddress, error) {
    // Example: redirect all transfers to a treasury address
    if shouldRedirect(toAddr) {
        treasuryAddr := k.GetTreasuryAddress(ctx)
        return treasuryAddr, nil
    }
    
    return toAddr, nil
}
```

## Implementation Details

### Internal Structure

The bank keeper maintains send restrictions in a `sendRestriction` struct that:
- Composes multiple restrictions using the `Then` method
- Applies restrictions through the `apply` method during transfers
- Can be cleared, appended to, or prepended to

### Key Functions

```golang
// AppendSendRestriction adds the provided SendRestrictionFn to run after previously provided restrictions
func (k BaseSendKeeper) AppendSendRestriction(restriction types.SendRestrictionFn)

// PrependSendRestriction adds the provided SendRestrictionFn to run before previously provided restrictions  
func (k BaseSendKeeper) PrependSendRestriction(restriction types.SendRestrictionFn)

// ClearSendRestriction removes the send restriction (if there is one)
func (k BaseSendKeeper) ClearSendRestriction()
```

## Example Use Cases

### 1. Token Freeze Module
```golang
func (k FreezeKeeper) SendRestrictionFn(ctx context.Context, fromAddr, toAddr sdk.AccAddress, amt sdk.Coins) (sdk.AccAddress, error) {
    if mymodule.HasBypass(ctx) {
        return toAddr, nil
    }
    
    for _, coin := range amt {
        if k.IsTokenFrozen(ctx, coin.Denom) {
            return nil, errors.New("token is frozen")
        }
    }
    
    return toAddr, nil
}
```

### 2. KYC/AML Compliance Module
```golang
func (k ComplianceKeeper) SendRestrictionFn(ctx context.Context, fromAddr, toAddr sdk.AccAddress, amt sdk.Coins) (sdk.AccAddress, error) {
    if mymodule.HasBypass(ctx) {
        return toAddr, nil
    }
    
    // Check if addresses are compliant
    if !k.IsCompliant(ctx, fromAddr) {
        return nil, errors.New("sender not compliant")
    }
    
    if !k.IsCompliant(ctx, toAddr) {
        return nil, errors.New("recipient not compliant")  
    }
    
    return toAddr, nil
}
```

### 3. Fee Collection Module
```golang
func (k FeeKeeper) SendRestrictionFn(ctx context.Context, fromAddr, toAddr sdk.AccAddress, amt sdk.Coins) (sdk.AccAddress, error) {
    if mymodule.HasBypass(ctx) {
        return toAddr, nil
    }
    
    // Collect fee before transfer
    fee := k.CalculateFee(ctx, amt)
    if err := k.bankKeeper.SendCoins(mymodule.WithBypass(ctx), fromAddr, k.GetFeeCollector(), fee); err != nil {
        return nil, err
    }
    
    return toAddr, nil
}
```

## Summary

This design allows for flexible, composable send restrictions that can be managed both programmatically and through configuration, giving app developers fine-grained control over token transfer policies across different modules. The dependency injection approach is the modern way to implement send restrictions, while the direct method is still supported for backward compatibility.
