# Threat model

Written before the first line of code and updated with every change that moves a
trust boundary.

## What we protect

1. **The bill.** Nobody should be able to keep the stack awake forever, or make
   it flap between states.
2. **The stack.** The guardian may only start and stop the machines listed in
   its manifest. It never creates, modifies or deletes anything.
3. **The cloud credentials** of the guardian.
4. **The application's origin.** The waiting page is served on the app's own
   domain: an injection there would be an injection in the app.

## Trust boundaries

| Boundary | Who is on the other side | Rule |
|---|---|---|
| Internet → load balancer → guardian | Anyone | Only the waiting page, its assets and a minimal status. No admin endpoint, no internal detail (no machine id, no IP, no error text). |
| Guardian → cloud API | The provider | TLS, dedicated key limited to reading, starting and stopping machines. |
| Guardian → stack machines | The stack | Plain HTTP or TCP probes on the private network, plus the activity probe. |
| bige-ops → guardian | The operator | Manifest and configuration at deploy time; the secret over SSH stdin, never in user data or Terraform state. |
| Docker socket (local driver) | The local Docker engine | Root-equivalent. Local tests only, never in a cloud deployment. |

## Threats and mitigations

| Threat | Mitigation |
|---|---|
| Robots or attackers keep the stack awake | Wake-ups per day and awake hours per month are capped; a capped stack stays asleep and says so. Technical URLs never wake it. |
| Wake storm from many visitors | A wake is started once; concurrent visits only read its progress. |
| Guardian drives a machine it should not | Every call goes through an allowlist built from the manifest; ids are validated (`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`). |
| Stolen guardian key | Least privilege (read, start, stop, tag). Worst case: an attacker can stop or start machines of the account, not read data nor delete anything. Keys expire and are rotated at each deploy. Whether the provider can restrict the key to specific machines is checked per driver. |
| XSS through the customised page | Fixed template, every value escaped by `html/template`, colours validated as `#rrggbb`, contact limited to `https:` and `mailto:`, no user JavaScript, strict CSP (`default-src 'none'`). |
| Script in a custom SVG logo | The logo is served with `Content-Security-Policy: sandbox` and `nosniff`; size limited to 64 KiB; PNG or SVG only. |
| Slow or oversized requests | Read, write and idle timeouts, 16 KiB headers, 4 KiB form bodies. |
| Information leak in status | Status only exposes phase, step, step count, estimate and whether a wake is allowed. |
| Database damage on sleep | Databases are stopped gracefully by the provider stop action; the bige-ops data containers have a 60 s stop timeout. Machines are never force-stopped. |
| Guardian down | The stack keeps serving when awake (the guardian is out of the path). When asleep, the operator can always resume from bige-ops with their own rights. |

## Residual risks

- A wake costs money even when triggered by a robot; the caps bound it.
- While the load balancer switches backends, a few requests may land on the
  waiting page although the app is ready; the page reloads itself.
