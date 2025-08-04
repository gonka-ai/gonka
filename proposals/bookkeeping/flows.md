# Flows as of Tokenomics v2
Rough chart of funding flows as of Tokenomics v2.

In all charts, the accounts with underscores are "subaccounts", the others are full Cosmos accounts.
## Inferences

```mermaid
sequenceDiagram
    participant inference as inference
    participant dev as dev
    participant exec as exec
    participant exec_balance as exec_balance
    participant exec_settled as exec_settled
    participant exec_vesting as exec_vesting
    participant vesting as vesting
    participant supply as supply

    note over inference: Module cosmos<br>account
    note over dev: dev cosmos<br>account
    note over exec: executor cosmos<br>account
    note over exec_balance: executor balance<br>owed
    note over exec_settled: executor amount<br>awaiting claim
    note over exec_vesting: amount held for<br>vesting for exec
    note over vesting: Module cosmos<br>account
    note over supply: Burns/Mints come from<br>here

    %% Inference Starts
    rect rgb(220, 255, 255)
    Note over inference,dev: Inference Starts
    dev->>inference: escrow
    end

    %% Inference Finishes
    rect rgb(220, 255, 255)
    Note over inference,dev: Inference Finishes
    inference-->>dev: refund
    inference-->>exec_balance: work
    end

    %% Inference Expires
    rect rgb(255, 255, 220)
    Note over inference,dev: Inference Expires
    inference-->>dev: full refund
    end

    %% Inference Invalidated
    rect rgb(255, 220, 220)
    Note over inference,dev: Inference Invalidated
    exec_balance-->>inference: removed
    inference-->>dev: refunded
    end

    %% Settle
    rect rgb(220, 255, 220)
    Note over inference,supply: Settle
    supply-->>inference: reward
    inference-->>exec_settled: reward
    exec_balance-->>exec_settled: work
    end

    %% Claim
    rect rgb(230, 240, 255)
    Note over exec_settled,vesting: Claim
    exec_settled-->>vesting: reward
    vesting-->>exec_vesting: reward
    exec_settled-->>exec: work
    end

    %% UnclaimedAtSettle
    rect rgb(255, 230, 255)
    Note over exec_settled,supply: Unclaimed At Settle
    exec_settled-->>supply: burn (work+reward)
    end

    %% Vesting
    rect rgb(255, 220, 240)
    Note over exec_vesting,exec: Vesting
    exec_vesting-->>exec: vested amount
    end
```

## Collateral
```mermaid
sequenceDiagram
    participant provider as provider
    participant collateral as collateral
    participant provider_collateral as provider_collateral
    participant provider_unbonding as provider_unbonding
    participant supply as supply

    %% Deposit
    rect rgb(220, 255, 255)
    Note over provider,collateral: Deposit
    provider->>collateral: deposit
    collateral-->>provider_collateral: deposit
    end

    %% Withdrawal Request
    rect rgb(255, 255, 220)
    Note over provider_collateral,provider_unbonding: Withdrawal Request
    provider_collateral-->>provider_unbonding: unbonding 
    end

    %% Unbonding finished
    rect rgb(230, 240, 255)
    Note over provider_unbonding,provider: Unbonding finished
    provider_unbonding-->>provider: unbonding
    end

    %% Slashing
    rect rgb(255, 220, 220)
    Note over provider_collateral,supply: Slashing
    provider_collateral-->>supply: % burned
    provider_unbonding-->>supply: % burned
    end
```