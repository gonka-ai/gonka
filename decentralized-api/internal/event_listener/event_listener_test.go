package event_listener

import (
	"decentralized-api/internal/event_listener/chainevents"
	"encoding/json"
	"log"
	"testing"
)

const (
	e3 = `
{
  "jsonrpc": "2.0",
  "id": "1",
  "result": {
    "query": "tm.event='Tx' AND message.action='/inference.inference.MsgFinishInference'",
    "data": {
      "type": "tendermint/event/Tx",
      "value": {
        "TxResult": {
          "height": "20483",
          "tx": "Cs4WCssWCicvaW5mZXJlbmNlLmluZmVyZW5jZS5Nc2dGaW5pc2hJbmZlcmVuY2USnxYKLWNvc21vczFwZDk0bjZkcDhldDJncmVwbWFuaDlxc2FqY2NuN210NzZkaDBzNxIkM2NkZmE3YjMtZWMxZC00NWQ0LTk5YjItMWZlNzA4YTlkZGE3GkA3OTBhNWE3ZDVkOGE4ZjhjNzQ5NTAwOGE3ZjEyMDIzYTkxM2JlOWQwY2Y5Y2MxOGJjNjdiYmYwZDEzZmNmMTljItIUeyJpZCI6ImNtcGwtOWRkNDNjYWIwZTM1NGM0NTgyYzU3M2Y4YzU5ZTlhZWUiLCJvYmplY3QiOiJjaGF0LmNvbXBsZXRpb24iLCJjcmVhdGVkIjoxNzIyOTk5MTIzLCJtb2RlbCI6InVuc2xvdGgvbGxhbWEtMy04Yi1JbnN0cnVjdCIsImNob2ljZXMiOlt7ImluZGV4IjowLCJtZXNzYWdlIjp7InJvbGUiOiJhc3Npc3RhbnQiLCJjb250ZW50IjoiQXVndXN0IDIxLCAxOTU5LiIsInRvb2xfY2FsbHMiOltdfSwibG9ncHJvYnMiOnsiY29udGVudCI6W3sidG9rZW4iOiJBdWd1c3QiLCJsb2dwcm9iIjotMi44NzI5MDI1ODcxMTQzNjc2ZS0wNSwiYnl0ZXMiOls2NSwxMTcsMTAzLDExNywxMTUsMTE2XSwidG9wX2xvZ3Byb2JzIjpbeyJ0b2tlbiI6IkF1Z3VzdCIsImxvZ3Byb2IiOi0yLjg3MjkwMjU4NzExNDM2NzZlLTA1LCJieXRlcyI6WzY1LDExNywxMDMsMTE3LDExNSwxMTZdfSx7InRva2VuIjoiSCIsImxvZ3Byb2IiOi0xMS44NzUwMjg2MTAyMjk0OTIsImJ5dGVzIjpbNzJdfSx7InRva2VuIjoiMTk1IiwibG9ncHJvYiI6LTEyLjEyNTAyODYxMDIyOTQ5MiwiYnl0ZXMiOls0OSw1Nyw1M119XX0seyJ0b2tlbiI6IiAiLCJsb2dwcm9iIjotMi4xNDU3NDQxMTA3NDg2Mzc1ZS0wNSwiYnl0ZXMiOlszMl0sInRvcF9sb2dwcm9icyI6W3sidG9rZW4iOiIgIiwibG9ncHJvYiI6LTIuMTQ1NzQ0MTEwNzQ4NjM3NWUtMDUsImJ5dGVzIjpbMzJdfSx7InRva2VuIjoiIEZpZnR5IiwibG9ncHJvYiI6LTExLjc1MDAyMDk4MDgzNDk2MSwiYnl0ZXMiOlszMiw3MCwxMDUsMTAyLDExNiwxMjFdfSx7InRva2VuIjoiIE5pbiIsImxvZ3Byb2IiOi0xMi41MDAwMjA5ODA4MzQ5NjEsImJ5dGVzIjpbMzIsNzgsMTA1LDExMF19XX0seyJ0b2tlbiI6IjIxIiwibG9ncHJvYiI6LTAuMDAwMTI1MDQyNzMwMzYwMjkxOSwiYnl0ZXMiOls1MCw0OV0sInRvcF9sb2dwcm9icyI6W3sidG9rZW4iOiIyMSIsImxvZ3Byb2IiOi0wLjAwMDEyNTA0MjczMDM2MDI5MTksImJ5dGVzIjpbNTAsNDldfSx7InRva2VuIjoiMTk1IiwibG9ncHJvYiI6LTkuMTI1MTI0OTMxMzM1NDUsImJ5dGVzIjpbNDksNTcsNTNdfSx7InRva2VuIjoiMTk2IiwibG9ncHJvYiI6LTExLjI1MDEyNDkzMTMzNTQ1LCJieXRlcyI6WzQ5LDU3LDU0XX1dfSx7InRva2VuIjoiLCIsImxvZ3Byb2IiOi0wLjAwMjQ5NTE1MzU3NDI3Mjk5MDIsImJ5dGVzIjpbNDRdLCJ0b3BfbG9ncHJvYnMiOlt7InRva2VuIjoiLCIsImxvZ3Byb2IiOi0wLjAwMjQ5NTE1MzU3NDI3Mjk5MDIsImJ5dGVzIjpbNDRdfSx7InRva2VuIjoiICIsImxvZ3Byb2IiOi02LjYyNzQ5NTI4ODg0ODg3NywiYnl0ZXMiOlszMl19LHsidG9rZW4iOiJzdCIsImxvZ3Byb2IiOi02Ljc1MjQ5NTI4ODg0ODg3NywiYnl0ZXMiOlsxMTUsMTE2XX1dfSx7InRva2VuIjoiICIsImxvZ3Byb2IiOi0zLjg3NDIyNjk2ODAzNjk2NDVlLTA1LCJieXRlcyI6WzMyXSwidG9wX2xvZ3Byb2JzIjpbeyJ0b2tlbiI6IiAiLCJsb2dwcm9iIjotMy44NzQyMjY5NjgwMzY5NjQ1ZS0wNSwiYnl0ZXMiOlszMl19LHsidG9rZW4iOiIxOTUiLCJsb2dwcm9iIjotMTAuMjUwMDM5MTAwNjQ2OTczLCJieXRlcyI6WzQ5LDU3LDUzXX0seyJ0b2tlbiI6IjE5NiIsImxvZ3Byb2IiOi0xMi42MjUwMzkxMDA2NDY5NzMsImJ5dGVzIjpbNDksNTcsNTRdfV19LHsidG9rZW4iOiIxOTUiLCJsb2dwcm9iIjotMS4xOTIwOTI4MjQ0NTM1Mzg5ZS0wNywiYnl0ZXMiOls0OSw1Nyw1M10sInRvcF9sb2dwcm9icyI6W3sidG9rZW4iOiIxOTUiLCJsb2dwcm9iIjotMS4xOTIwOTI4MjQ0NTM1Mzg5ZS0wNywiYnl0ZXMiOls0OSw1Nyw1M119LHsidG9rZW4iOiIxOTYiLCJsb2dwcm9iIjotMTYuMjUsImJ5dGVzIjpbNDksNTcsNTRdfSx7InRva2VuIjoiNTkiLCJsb2dwcm9iIjotMjEuNjI1LCJieXRlcyI6WzUzLDU3XX1dfSx7InRva2VuIjoiOSIsImxvZ3Byb2IiOjAuMCwiYnl0ZXMiOls1N10sInRvcF9sb2dwcm9icyI6W3sidG9rZW4iOiI5IiwibG9ncHJvYiI6MC4wLCJieXRlcyI6WzU3XX0seyJ0b2tlbiI6IjgiLCJsb2dwcm9iIjotMTcuNSwiYnl0ZXMiOls1Nl19LHsidG9rZW4iOiIwIiwibG9ncHJvYiI6LTE5LjEyNSwiYnl0ZXMiOls0OF19XX0seyJ0b2tlbiI6Ii4iLCJsb2dwcm9iIjotMC42OTMxNTE0NzM5OTkwMjM0LCJieXRlcyI6WzQ2XSwidG9wX2xvZ3Byb2JzIjpbeyJ0b2tlbiI6Ii4iLCJsb2dwcm9iIjotMC42OTMxNTE0NzM5OTkwMjM0LCJieXRlcyI6WzQ2XX0seyJ0b2tlbiI6IiIsImxvZ3Byb2IiOi0wLjY5MzE1MTQ3Mzk5OTAyMzQsImJ5dGVzIjpbXX0seyJ0b2tlbiI6IiEiLCJsb2dwcm9iIjotMTIuNDQzMTUxNDczOTk5MDIzLCJieXRlcyI6WzMzXX1dfSx7InRva2VuIjoiIiwibG9ncHJvYiI6MC4wLCJieXRlcyI6W10sInRvcF9sb2dwcm9icyI6W3sidG9rZW4iOiIiLCJsb2dwcm9iIjowLjAsImJ5dGVzIjpbXX0seyJ0b2tlbiI6IiIsImxvZ3Byb2IiOi0yMC44NzUsImJ5dGVzIjpbXX0seyJ0b2tlbiI6IiBcblxuIiwibG9ncHJvYiI6LTIxLjMxMjUsImJ5dGVzIjpbMzIsMTAsMTBdfV19XX0sImZpbmlzaF9yZWFzb24iOiJzdG9wIiwic3RvcF9yZWFzb24iOm51bGx9XSwidXNhZ2UiOnsicHJvbXB0X3Rva2VucyI6NDcsInRvdGFsX3Rva2VucyI6NTYsImNvbXBsZXRpb25fdG9rZW5zIjo5fX0oLzAJOi1jb3Ntb3MxcGQ5NG42ZHA4ZXQyZ3JlcG1hbmg5cXNhamNjbjdtdDc2ZGgwczcSWApQCkYKHy9jb3Ntb3MuY3J5cHRvLnNlY3AyNTZrMS5QdWJLZXkSIwohAnkusSfhJjW2/HaFCAhLIcBCO9v5wwNltPSBcWSWcZDlEgQKAggBGBISBBDgpxIaQDsr5O6FQYSljBZrscHhc8adG8Ox1FpS+Qpeen4td+iaVItMVCoRrl4pOq6o6TkyNlwoRjSYCKXQDW3O9sv14WQ=",
          "result": {
            "data": "EjEKLy9pbmZlcmVuY2UuaW5mZXJlbmNlLk1zZ0ZpbmlzaEluZmVyZW5jZVJlc3BvbnNl",
            "gas_wanted": "300000",
            "gas_used": "175479",
            "events": [
              {
                "type": "tx",
                "attributes": [
                  {
                    "key": "fee",
                    "value": "",
                    "index": true
                  },
                  {
                    "key": "fee_payer",
                    "value": "cosmos1pd94n6dp8et2grepmanh9qsajccn7mt76dh0s7",
                    "index": true
                  }
                ]
              },
              {
                "type": "tx",
                "attributes": [
                  {
                    "key": "acc_seq",
                    "value": "cosmos1pd94n6dp8et2grepmanh9qsajccn7mt76dh0s7/18",
                    "index": true
                  }
                ]
              },
              {
                "type": "tx",
                "attributes": [
                  {
                    "key": "signature",
                    "value": "Oyvk7oVBhKWMFmuxweFzxp0bw7HUWlL5Cl56fi136JpUi0xUKhGuXik6rqjpOTI2XChGNJgIpdANbc72y/XhZA==",
                    "index": true
                  }
                ]
              },
              {
                "type": "message",
                "attributes": [
                  {
                    "key": "action",
                    "value": "/inference.inference.MsgFinishInference",
                    "index": true
                  },
                  {
                    "key": "sender",
                    "value": "cosmos1pd94n6dp8et2grepmanh9qsajccn7mt76dh0s7",
                    "index": true
                  },
                  {
                    "key": "module",
                    "value": "inference",
                    "index": true
                  },
                  {
                    "key": "msg_index",
                    "value": "0",
                    "index": true
                  }
                ]
              },
              {
                "type": "inference_finished",
                "attributes": [
                  {
                    "key": "inference_id",
                    "value": "3cdfa7b3-ec1d-45d4-99b2-1fe708a9dda7",
                    "index": true
                  },
                  {
                    "key": "msg_index",
                    "value": "0",
                    "index": true
                  }
                ]
              }
            ]
          }
        }
      }
    },
    "events": {
      "tm.event": [
        "Tx"
      ],
      "tx.height": [
        "20483"
      ],
      "tx.fee": [
        ""
      ],
      "tx.acc_seq": [
        "cosmos1pd94n6dp8et2grepmanh9qsajccn7mt76dh0s7/18"
      ],
      "tx.signature": [
        "Oyvk7oVBhKWMFmuxweFzxp0bw7HUWlL5Cl56fi136JpUi0xUKhGuXik6rqjpOTI2XChGNJgIpdANbc72y/XhZA=="
      ],
      "message.sender": [
        "cosmos1pd94n6dp8et2grepmanh9qsajccn7mt76dh0s7"
      ],
      "message.module": [
        "inference"
      ],
      "inference_finished.msg_index": [
        "0"
      ],
      "tx.fee_payer": [
        "cosmos1pd94n6dp8et2grepmanh9qsajccn7mt76dh0s7"
      ],
      "message.action": [
        "/inference.inference.MsgFinishInference"
      ],
      "message.msg_index": [
        "0"
      ],
      "inference_finished.inference_id": [
        "3cdfa7b3-ec1d-45d4-99b2-1fe708a9dda7"
      ],
      "tx.hash": [
        "21090B3646B234ADA4A27B9D81B999158FAEE87085CADDC74988CAE8D8CF28BE"
      ]
    }
  }
}
`
)

func Test(t *testing.T) {
	var res chainevents.JSONRPCResponse
	err := json.Unmarshal([]byte(e3), &res)
	if err != nil {
		t.Fatalf("error unmarshalling: %v", err)
	}

	log.Printf("res = %v", res)

	ids := res.Result.Events["inference_finished.inference_id"]
	if len(ids) == 0 {
		t.Fatalf("no inference ids found")
	}
}
