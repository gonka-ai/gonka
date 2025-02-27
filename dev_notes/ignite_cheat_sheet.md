# Ignite Cheat Sheet
Some tips for how to use Cosmos Ignite to update and create things:

## Add new store object:

`ignite scaffold map participant reputation:int weight:int join_time:uint join_height:int last_inference_time:uint --index address --module inference --no-message`

- **Be sure to include --no-message**, or else the store object will be modifiable simply by messages sent to the chain.
- Prefer snake_case
- Since address is added as an index, it doesn't need to be added as a field
- You can have multiple fields as an index by using the --index parameter multiple times (`--index key1 --index key2`)

## Add new message:
`ignite scaffold message createGame black red --module checkers --response gameIndex`

## Add new query:
`ignite scaffold query getGameResult gameIndex --module checkers --response result`

## Types that can be used in above CLI calls:

| Type         | Alias   | Index | Code Type | Description                     |
| ------------ | ------- | ----- | --------- | ------------------------------- |
| string       | -       | yes   | string    | Text type                       |
| array.string | strings | no    | []string  | List of text type               |
| bool         | -       | yes   | bool      | Boolean type                    |
| int          | -       | yes   | int32     | Integer type                    |
| array.int    | ints    | no    | []int32   | List of integers types          |
| uint         | -       | yes   | uint64    | Unsigned integer type           |
| array.uint   | uints   | no    | []uint64  | List of unsigned integers types |
| coin         | -       | no    | sdk.Coin  | Cosmos SDK coin type            |
| array.coin   | coins   | no    | sdk.Coins | List of Cosmos SDK coin types   |
|              |         |       |           |                                 |


## Modify existing store object:
change the types in the `.proto` file for the store object and then run:
`ignite generate proto-go`

## Modify existing message:
Change the types in `tx.proto` and then run:
`ignite generate proto-go`