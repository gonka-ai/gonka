# Manual test plan for devshard v6.
# Companion to release-0.2.16-v6.md.
# proxy is the nginx ingress. proxy-router is the HAProxy ingress.
# On join the HAProxy service may still be named proxy; confirm the image.
# Count connections from the client while N calls are in flight:
#   ss -tn dst :443     InferenceUrl; the same port on every cell

Feature: v6 peer RPC follows the wrapping, not the binary

  The v6 devshard binary is auto-added onto the wrapping that is already
  running. Peer RPC normally uses Connect over HTTP/1.1. With an HTTP URL
  and both-mode nginx, the client hop may use h2c but forwarding stays H1. After versiond, versiond-router, proxy, and
  proxy-router move to the v6 images, proxy-router serves the same RPC over
  HTTP/2 on the same InferenceUrl port, one connection per peer: it
  recognises a peer by the HTTP/2 preface on port 80 or ALPN gonka-rpc/1 on
  port 443 and keeps nginx out of that connection. A peer whose host still
  runs the old wrapping uses Connect; a successful h2c probe to nginx does
  not establish end-to-end multiplexing. JSON on ports 80 and 443 stays in place for v4 and v4.2.

  A chat that works on one cell and fails on the next is a release failure.

  Background:
    Given two peers with different keys and different source addresses
    And one approved host version
    And a long chat can be held open while short RPCs run
    And DEVSHARD_RPC_GRPC is unset unless native gRPC is explicitly under test

  # Cells:
  #   S   versiond + proxy           single
  #   SR  versiond + proxy-router    single
  #   H   versiond-router + proxy    HA, two versiond replicas
  #   HR  versiond-router + proxy-router  HA, two versiond replicas

  @old-wrapping @http11
  Scenario Outline: old nginx without h2c uses HTTP/1.1 and does not multiplex
    Given the devshardd and the gateway are v6
    And the wrapping images are <wrapping>
    And the cell is <cell>
    And InferenceUrl uses HTTP with HTTP-only nginx or uses HTTPS
    When both peers open N concurrent chats
    And one peer gossips and reads /healthz
    Then every call succeeds
    And peer RPC is Connect over HTTP/1.1 on InferenceUrl
    And the established connection count to the InferenceUrl port tracks N, not 1
    And an explicit protocol miss is cached per scheme, host, and port for 30 minutes with jitter
    And an ambiguous connection close instead uses short exponential backoff
    And the wire has no application/grpc content type
    And the nginx or policy-worker access log shows /rpc/
    And the public connection-rate limits do not fire

    Examples:
      | wrapping | cell |
      | v4       | S    |
      | v4       | H    |
      | v5       | SR   |
      | v5       | HR   |
      | v4 versiond behind v5 proxy-router | SR |
      | v5 versiond behind nginx proxy     | S  |

  @old-wrapping @ha @http11
  Scenario Outline: an HA cell on the old wrapping keeps the escrow on one versiond
    Given the devshardd and the gateway are v6
    And the wrapping images are <wrapping>
    And the cell is <cell>
    And two versiond replicas sit behind versiond-router
    When one peer chats twice on the same escrow
    Then both chats succeed
    And both chats are served by the same versiond

    Examples:
      | wrapping | cell |
      | v4       | H    |
      | v5       | HR   |

  @old-wrapping @http11
  Scenario: a v6 peer and a v5 peer share an escrow on the old wrapping
    Given the wrapping images are v5
    And the cell is HR
    And one participant runs v6 and another runs v5
    When both peers chat on the same escrow
    Then both chats succeed

  @v6-wrapping @http2
  Scenario Outline: v6 proxy-router serves peer RPC on one HTTP/2 connection
    Given the devshardd, the gateway, versiond, versiond-router, proxy, and proxy-router are v6
    And the cell is <cell>
    And the ingress image is proxy-router
    When one peer opens N concurrent RPCs, including a long Chat and Watch
    Then the client dials the InferenceUrl host on the InferenceUrl port and no other
    And those N RPCs share one TCP connection on every hop from the client to the child
    And a second peer has its own connection
    And the nginx access log has no /rpc/
    And ports 80 and 443 still serve JSON, /v1, and /healthz
    And the version in the URL still selects that child

    Examples:
      | cell |
      | SR   |
      | HR   |

  @v6-wrapping @http2 @tls
  Scenario: an HTTPS InferenceUrl terminates HTTP/2 with the nginx certificate
    Given the cell is SR
    And InferenceUrl is HTTPS
    And the wrapping images are v6
    When the peer dials the InferenceUrl port
    Then the handshake is TLS with ALPN gonka-rpc/1
    And the certificate is the same material nginx serves
    And a browser on the same port still negotiates h2 with nginx

  @v6-wrapping @http2
  Scenario: a cleartext InferenceUrl uses h2c on the InferenceUrl port
    Given the cell is SR
    And InferenceUrl is HTTP
    And the wrapping images are v6
    When the peer dials the InferenceUrl port
    Then the connection opens with the HTTP/2 client preface and is cleartext HTTP/2
    And an HTTP/1.1 request on the same port still reaches nginx

  @v6-wrapping @ha @http2
  Scenario: HA placement on HTTP/2 matches the HTTP/1.1 path
    Given the cell is HR
    And the wrapping images are v6
    And two versiond replicas sit behind versiond-router
    When one peer chats on an escrow that was already placed over HTTP/1.1
    Then the chat is served by the same versiond
    And the version in the URL still selects that child

  @v6-wrapping @http2
  Scenario: turning the probe off returns the peer to HTTP/1.1
    Given the cell is SR
    And the wrapping images are v6
    And the peer was using HTTP/2
    When DEVSHARD_RPC_H2_UPGRADE is false
    And the peer opens N concurrent chats
    Then peer RPC is Connect over HTTP/1.1 on InferenceUrl
    And the established connection count tracks N, not 1

  @v6-wrapping @http2 @fallback
  Scenario: a host that loses HTTP/2 is reached over HTTP/1.1 and recovered later
    Given the cell is SR
    And the wrapping images are v6
    And the peer was using HTTP/2
    When the host's proxy image is swapped for one that does not recognise the preface
    Then the peer's next Attach misses HTTP/2, is logged once, and completes over HTTP/1.1
    And chats keep completing
    When the v6 proxy image is restored
    Then within about 30 minutes the peer's next Attach or refresh is HTTP/2 again
    And no process was restarted for either change

  @v6-wrapping @http11
  Scenario Outline: a v6 nginx proxy without h2c retains HTTP/1.1
    Given the devshardd, the gateway, and versiond are v6
    And the cell is <cell>
    And the ingress image is nginx, not proxy-router
    And InferenceUrl uses HTTP with HTTP-only nginx or uses HTTPS
    When both peers open N concurrent chats
    Then peer RPC is Connect over HTTP/1.1 on InferenceUrl
    And the established connection count tracks N, not 1
    And the host needs no port beyond InferenceUrl's
    And JSON is still served

    Examples:
      | cell |
      | S    |
      | H    |

  @compat @nginx @h2c
  Scenario Outline: both-mode nginx multiplexes only the client hop
    Given the devshardd and gateway are v6
    And the wrapping images are <wrapping>
    And the cell is <cell>
    And nginx has NGINX_MODE=both
    And InferenceUrl uses HTTP
    When one peer opens N concurrent RPCs
    Then every call succeeds using Connect
    And the client may keep a successful h2c connection to nginx without a protocol miss
    And concurrent RPCs use separate HTTP/1.1 connections downstream of nginx
    And nginx logs /rpc/ requests
    And the dedicated public peer-RPC connection policy is not exercised
    And the child still enforces peer limits

    Examples:
      | wrapping | cell |
      | v4       | S    |
      | v4       | H    |
      | v5       | SR   |
      | v5       | HR   |
      | v6 with nginx ingress | S |
      | v6 with nginx ingress | H |

  @rate-limit @both-wrappings
  Scenario: peer budget isolates peers
    Given a non-production v6 child with DEVSHARD_RPC_MSGS_PER_MIN 60 and DEVSHARD_RPC_MSGS_BURST 60
    And the variables are restored after this scenario
    When peer A sends GetDiffs until the child refuses
    Then the refusal is resource_exhausted and carries a retry hint
    And the second GetDiffs is enough to trip the lab burst of 60 at weight 60
    And peer B's Chat in the same window succeeds
    And GET /devshard/stats/rpc shows the ban on peer A and not on peer B
    And this holds on the old wrapping and on the v6 wrapping

  @rate-limit @both-wrappings
  Scenario: the stream cap is per peer
    Given a non-production v6 child with DEVSHARD_RPC_MAX_STREAMS_PER_PEER 3
    And the variable is restored after this scenario
    When peer A holds 2 chats
    Then the next Chat from peer A is resource_exhausted
    And that refusal does not spend chat weight
    And Watch for peer A still opens
    And peer B opens a chat at the same time
    And this holds on the old wrapping and on the v6 wrapping

  @rate-limit @v6-wrapping @http2
  Scenario Outline: path zones on one connection do not starve Chat
    Given the cell is <cell>
    And the wrapping images are v6
    And the ingress image is proxy-router
    And peer A has one established HTTP/2 connection to the InferenceUrl port
    When peer A sends more than 400 GetDiffs in 10 seconds on that connection
    Then further GetDiffs return HTTP 429
    And a Chat on that same connection still completes
    And Attach from that address keeps its own 100-per-10-seconds zone
    And the established connection count stays 1 for the whole run

    Examples:
      | cell |
      | SR   |
      | HR   |

  @rate-limit @v6-wrapping @http2
  Scenario: connection rate is per source address and is absent on HTTP/1.1
    Given the cell is SR
    And the wrapping images are v6
    When one address opens connections that start with the HTTP/2 preface faster than 200 per second
    Then further handshakes die before Attach
    And the child logs no signature work for the refused handshakes
    And a second address opening one connection in the same second attaches and chats
    When the same flood is aimed at InferenceUrl on the old wrapping
    Then this reject does not appear

  @rate-limit @v6-wrapping @http2
  Scenario: a forged X-Real-IP does not move the bucket
    Given the cell is SR
    And the wrapping images are v6
    When peer A sets X-Real-IP to peer B's address and floods GetDiffs
    Then the flood counts against peer A's real address
    And peer B is not pushed over its zone
    When peer A clears X-Real-IP
    Then peer A does not receive a second bucket

  @rate-limit @v6-wrapping @ha @http2
  Scenario: HA does not merge two peers into one zone
    Given the cell is HR
    And the wrapping images are v6
    And two clients have two addresses through versiond-router
    And each client has one connection
    When client A sends more than 400 GetDiffs in 10 seconds
    Then client A's further GetDiffs return HTTP 429
    And client B's Chat succeeds
    And client B's GetDiffs succeeds

  @rate-limit @both-wrappings
  Scenario Outline: an Attach flood stays on one address
    Given the cell is <cell>
    And the wrapping images are <wrapping>
    And peer A holds a live session and Watch stays up
    When anonymous Attach is flooded from a second address until that address is refused
    Then the refusal is the Attach zone or the child origin bucket
    And the refused calls do no signature work in the child
    And peer A's renewal, well under 4 per minute, still succeeds
    And the previous token still admits in-flight calls during its overlap window
    And peer A's Watch does not drop
    And a third address with no session header completes a first Attach
    And that third address completes a later Attach with no session header while the flood address is still refused

    Examples:
      | wrapping                  | cell |
      | v4                        | S    |
      | v5                        | SR   |
      | v6                        | SR   |
      | v6                        | HR   |
      | v6 with nginx ingress     | S    |
      | v6 with nginx ingress     | H    |

  @catch-up @both-wrappings
  Scenario Outline: a diff read wider than one page is rejected
    Given a v6 child
    And the transport is <transport>
    When a peer requests diffs from nonce 1 through nonce 65
    Then the call is rejected
    And the error names at most 64 nonces and 8388608 bytes
    When the peer requests diffs from nonce 1 through nonce 64 and each diff is small
    Then the call succeeds
    And on HTTP/2 that page does not open a second connection

    Examples:
      | transport                         |
      | HTTP/1.1 on InferenceUrl          |
      | HTTP/2 on InferenceUrl after section 2 |

  @catch-up @both-wrappings
  Scenario: one oversized diff is its own page
    Given a v6 child
    And one stored diff is larger than 8 MiB
    When a peer requests that diff alone
    Then the diff is returned
    And the call is not rejected for size

  @catch-up @both-wrappings
  Scenario: a host more than 200 diffs behind catches up in chunks
    Given a v6 gateway
    And one host is more than 200 diffs behind
    When that host catches up
    Then the gateway logs show sendCatchUp in more than one chunk
    And each diffs_in_chunk is at most 200
    And the next chat to that host succeeds
    And the process does not hold the whole journal for that catch-up

  @compat @v6-wrapping
  Scenario Outline: v6 wrapping still serves an older devshard over JSON
    Given versiond, versiond-router, proxy, and proxy-router are v6
    And the cell is <cell>
    And the child and the gateway are <binary>
    When a peer chats, gossips, reads diffs, and reads /healthz on InferenceUrl
    Then every call succeeds over HTTP/1.1
    And the old binary is not required to answer /rpc/ or HTTP/2
    And recognising the HTTP/2 preface does not break JSON on port 80 or 443
    And a failed /rpc/ against that child does not take down its JSON routes
    And the version in the URL still selects that child

    Examples:
      | binary | cell |
      | v4     | SR   |
      | v4     | HR   |
      | v4.2   | SR   |
      | v4.2   | HR   |
      | v4     | S    |
      | v4.2   | H    |

  @compat @v6-wrapping @ha
  Scenario Outline: an older child keeps HA stickiness behind v6 wrapping
    Given the cell is HR
    And the wrapping images are v6
    And the child is <binary>
    And two versiond replicas sit behind versiond-router
    When one peer chats twice on the same escrow
    Then both chats succeed on InferenceUrl
    And both chats are served by the same versiond

    Examples:
      | binary |
      | v4     |
      | v4.2   |

  @compat @v6-wrapping
  Scenario Outline: a v6 gateway completes a chat against an older host
    Given the wrapping images are v6
    And the remote host is <binary>
    When a v6 gateway chats with that host
    Then the chat succeeds
    And the call is recorded as JSON or as a failed /rpc/ that fell back to JSON
    And the chat does not hang
    And the chat does not succeed only when the old child speaks /rpc/

    Examples:
      | binary |
      | v4     |
      | v4.2   |

  @compat @v6-wrapping
  Scenario: a mixed escrow keeps HTTP/2 and JSON apart
    Given the wrapping images are v6
    And one child is v6 and another child is v4.2
    And both children serve the same escrow
    When the v6 peer chats with the v6 child
    And the v4.2 peer chats with the v4.2 child
    Then the v6 peer uses one HTTP/2 connection to the v6 host
    And the v4.2 peer completes the chat on InferenceUrl
    And neither path fails the other

  @compat @v6-wrapping
  Scenario Outline: replacing an older child does not wait on a readiness field it does not send
    Given the wrapping images are v6
    And the running child is <binary>
    When that child is replaced by a newer generation
    Then the swap does not wait forever on a readiness body field the old binary does not send
    And the old generation keeps serving until the new one can

    Examples:
      | binary |
      | v4     |
      | v4.2   |
