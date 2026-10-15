---
title: "myriarchd healthcheck"
weight: 80
description: "Check that a running myriarchd is ready and serves its OpAMP endpoint."
---

## myriarchd healthcheck

Check that a running myriarchd is ready and serves its OpAMP endpoint

### Synopsis

Checks a running myriarchd and tells the result by the exit code. The container image has neither a shell nor curl, so
this command is the health check of the container and of "helm test". Example for a container health check, in the form
that runs the program without a shell:

  ["CMD", "/usr/local/bin/myriarchd", "healthcheck"]

Two requests, one after the other, within --timeout:

1. GET /readyz must answer 200. The server answers 503 while its database does not answer, the schema does not fit or its event
   bus is not connected.
2. A WebSocket upgrade at --opamp-path: the request of an OpAMP agent, without its first message. Without --token-file the
   server must refuse it with 401, which shows that the route and the authentication are in place; a server that takes
   connections without a token (the development mode) may accept it with 101. With --token-file the server must accept it
   with 101. After 101 the command closes the connection with a normal WebSocket close. It sends no OpAMP message, so no
   agent is registered and nothing is stored.

The token is read from the file only, never from an argument or from the environment, and it is never printed. The file must
hold one enrollment token (the prefix myre_, then 32 to 120 letters, digits, underscores or hyphens); anything else is
refused before a request is made. Use a token that exists for this check only. The server counts a check without a token as an unauthorized
connect (myriarch_opamp_connect_rejected_total, reason unauthorized) and a check with a token as a use of that token.

The command follows no redirects and uses no proxy; it needs no loopback address. With --ca-file the certificate of an https
server must be signed by one of the CAs of that PEM file and by no other; without it the system roots apply.

Output: one line per successful request on stdout ("readyz ok: ...", "opamp ok: ..."), the reason of a failure on stderr.
Exit codes: 0 healthy, 1 not healthy (the server answered, but not as expected), 2 wrong usage (an invalid value, a token file
or CA file that cannot be used), 3 no answer (connection refused, name not resolved, TLS verification failed, time out).

```text
myriarchd healthcheck [--url URL] [--opamp-path PATH] [--token-file FILE] [--ca-file FILE] [--timeout DURATION] [flags]
```

### Options

```text
      --ca-file string      PEM file with the CAs that may have signed the certificate of an https server (only these; default: the system roots)
  -h, --help                help for healthcheck
      --opamp-path string   path of the OpAMP endpoint (opamp.path of the server) (default "/v1/opamp")
      --timeout duration    time allowed for both requests together (default 10s)
      --token-file string   file with an enrollment token: the check then expects the server to accept the upgrade (101); without it the server must refuse it (401). The token is read from the file only
      --url string          base URL of the server: http or https, a host and an optional port, no path (the path of the OpAMP endpoint is --opamp-path) (default "http://127.0.0.1:8080")
```

### SEE ALSO

- [myriarchd](myriarchd.md) - Myriarch server - OpAMP based management of OpenTelemetry Collectors
