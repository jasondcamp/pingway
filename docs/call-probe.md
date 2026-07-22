# Synthetic call probe

ICMP ping proves loss exists, but an ISP can wave it off two ways: "we
deprioritize ICMP" and "ping isn't a real application." The call probe
removes both dodges by sending traffic literally shaped like a video
call: UDP, 172-byte packets, 50 packets/sec, source port in the RTP
range (10000–20000). A reflector on a host **outside** your network
echoes each packet back, and pingway measures per packet:

- **Loss** — sent but never came back
- **Jitter** — RFC 3550-style EWMA of RTT variation (what empties a
  call's playout buffer)
- **Freezes** — runs of consecutive loss. A ≥200ms run is a visible
  video stall, reported in the units of your complaint: *"47 freezes
  over 200ms in 3 hours"*
- **MOS** — Mean Opinion Score (1–5) via the Cole & Rosenbluth
  simplification of the ITU-T G.107 E-model. The telecom industry's own
  quality metric: >4 good, 3–4 degraded, <3 unusable for calls.

The reflector must be off-network: reflecting off your own router only
tests your LAN. On an external VPS, the stream crosses the exact path
that's broken — your access link, the ISP's network, the internet.
Bandwidth is ~9KB/s each way per reflector; it runs continuously.

Probe traffic runs alongside the ICMP probes with shared timestamps, so
a freeze lines up against the loss burst at the ISP's first hop in the
same report. Samples taken during speed tests are flagged and excluded
from freeze evidence (self-inflicted congestion doesn't count).

## Running a reflector

The reflector is a stateless UDP echo, safe to run publicly:

- **Anti-spoofing handshake** — senders must complete a HELLO→TOKEN
  exchange first. The token is an HMAC of the observed source address,
  delivered only to that address, so a spoofed source never learns a
  valid token and never produces a single echo toward a third party.
  The reflector keeps zero per-client state (tokens are recomputed),
  and its HMAC key rotates on restart; senders detect the stale token
  (15s of silence) and re-handshake automatically.
- **No amplification** — echoes are byte-for-byte the request; the
  TOKEN reply (12 bytes) is far smaller than the HELLO (172 bytes).
- **Rate limits** — per source IP (default 120pps, `-per-ip-pps`) and
  global (default 20000pps, `-max-pps`).

Docker:

```sh
docker run -d --name pingway-reflector --restart unless-stopped \
  -p 15000:15000/udp ghcr.io/jasondcamp/pingway-reflector:latest
```

Kubernetes:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: pingway-reflector
spec:
  replicas: 1
  selector:
    matchLabels: { app: pingway-reflector }
  template:
    metadata:
      labels: { app: pingway-reflector }
    spec:
      containers:
        - name: reflector
          image: ghcr.io/jasondcamp/pingway-reflector:latest
          ports:
            - containerPort: 15000
              protocol: UDP
          resources:
            requests: { cpu: 10m, memory: 16Mi }
            limits: { memory: 48Mi }
---
apiVersion: v1
kind: Service
metadata:
  name: pingway-reflector
spec:
  type: LoadBalancer   # or NodePort + open the UDP port
  selector: { app: pingway-reflector }
  ports:
    - port: 15000
      targetPort: 15000
      protocol: UDP
```

Note: the probe measures the reflector's path too, so put it somewhere
network-boring (a major cloud region near you). Two reflectors — one
near, one far — separate "my access link is broken" from "the internet
is having a day."

## Pointing pingway at it

```sh
# .env — Name:host[:port], comma-separated; port defaults to 15000
CALLPROBE_REFLECTORS=DO-NYC:203.0.113.10,DO-AMS:198.51.100.7:15000
CALLPROBE_PPS=50   # optional, default 50
```

or YAML:

```yaml
callprobe:
  pps: 50
  reflectors:
    - name: DO-NYC
      host: 203.0.113.10:15000
```

The dashboard then shows a live **Call quality** panel (MOS per
reflector), a **MOS over time** chart, and a **Call freezes** log in the
history section. `/api/callprobe/history`, `/api/callprobe/freezes`,
and `/api/reflectors` expose the same data for reports.
