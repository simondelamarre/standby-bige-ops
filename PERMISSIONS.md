# Permissions

The guardian asks for the least it can work with. Anything not listed here is a
bug.

## Docker driver (local tests only)

Access to the Docker socket. This is root-equivalent on the host: use it only on
a development machine, never in a cloud deployment.

API calls: `GET /containers/{id}/json`, `POST /containers/{id}/start`,
`POST /containers/{id}/stop`.

## Outscale driver (planned)

A dedicated EIM user per stack, created by bige-ops, with a key that expires
(90 days, renewed at each deploy).

| Action | Why |
|---|---|
| `api:ReadVms` | Read the state of the stack machines and the hold tag |
| `api:StartVms` | Wake the stack |
| `api:StopVms` | Put the stack to sleep (never with force) |
| `api:CreateTags`, `api:DeleteTags` | Publish short status counters on the guardian VM |

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
