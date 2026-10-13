# wiretest

Test helpers for the provider clients: an `httptest` server that replays
recorded vendor responses in order and records each request (method, URL,
headers, body), `Options` pointing a client at it with a canary API key, and
an `Events` collector for stream events. Used only by tests; it never
reaches the network.
