```sh
rm -f wait-for-build && touch wait-for-build && make build WHAT=./cmd/kcp && rm -rf .kcp && rm -f kcp-output.log && rm -f wait-for-build && ./bin/kcp start --bind-address=127.0.0.1 2>&1 | tee -a kcp-output.log
```

```text
[2026-02-18T19:33:41Z] starting ready check
Waiting for API server to be ready...
[...]
Waiting for API server to be ready...
[2026-02-18T19:34:09Z] readyz returned ok
.
└── root
[2026-02-18T19:34:10Z] API server is responsive
```

29s startup

```text
[2026-02-18T19:39:27Z] starting ready check
Waiting for API server to be ready...
[...]
Waiting for API server to be ready...
[2026-02-18T19:39:50Z] readyz returned ok
.
└── root

[2026-02-18T19:39:50Z] API server is responsive
```

12s startup

```text
[2026-02-18T21:53:04Z] starting ready check
Waiting for API server to be ready...
[...]
Waiting for API server to be ready...
[2026-02-18T21:53:29Z] readyz returned ok
.
└── root

[2026-02-18T21:53:29Z] API server is responsive
```

```text
[2026-02-18T21:54:03Z] starting ready check
Waiting for API server to be ready...
[...]
Waiting for API server to be ready...
[2026-02-18T21:54:28Z] readyz returned ok
.
└── root

[2026-02-18T21:54:28Z] API server is responsive
```
