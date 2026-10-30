# Permissions

The guardian asks for the least it can work with. Anything not listed here is a
bug.

## Docker driver (local tests only)

Access to the Docker socket. This is root-equivalent on the host: use it only on
a development machine, never in a cloud deployment.

API calls: `GET /containers/{id}/json`, `POST /containers/{id}/start`,
`POST /containers/{id}/stop`.

## Outscale driver

A dedicated EIM user per stack, created by bige-ops, with a key that expires
(90 days, renewed at each deploy). The key pair sits in a JSON file
(`{"access_key": "...", "secret_key": "..."}`) that only its owner can read:
the guardian refuses a file with group or world permissions, and the keys
never appear in the configuration, the logs or the status.

| Action | Why | Since |
|---|---|---|
| `api:ReadVms` | Read the state of the stack machines | now |
| `api:StartVms` | Wake the stack | now |
| `api:StopVms` | Put the stack to sleep, always with `ForceStop: false` | now |
| `api:CreateTags`, `api:DeleteTags` | Hold tag and short status counters on the guardian VM | planned |

Check a configuration and its key without changing anything:
`standby validate --config config.json --live` reads the state of the
manifest machines once.

Never granted: creating, modifying or deleting machines, volumes, networks,
keys, users or policies.

To check in the driver: whether EIM can restrict these actions to the stack
machines. If not, the guardian still refuses any machine absent from its
manifest, and the residual risk is documented in
[`THREAT_MODEL.md`](THREAT_MODEL.md).

## Network

| Direction | Peer | Port |
|---|---|---|
| In | Load balancer | traffic port (`listen`) and health port (`health_listen`) |
| Out | Cloud API | 443 |
| Out | Stack machines | probe ports, activity probe port |

No SSH from the Internet, no public IP.
