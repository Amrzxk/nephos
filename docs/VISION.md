# Nephos: Vision

> **νέφος** (Greek): *cloud*. A local cloud you can break, fix, and understand.

**Status:** planning (September 2026). Nothing here is implemented yet.

---

## 1. The problem

Learning cloud infrastructure is expensive, risky, and hard to see into.

- **Real accounts cost money and cause anxiety.** They need a credit card, free tiers are narrow, and NAT gateways, load balancers, public IPv4 addresses, and forgotten instances all bill by the hour. Beginners are right to be afraid to experiment.
- **Hosted sandboxes are rented time.** Course platforms need subscriptions and an internet connection, and they reset on a timer, usually before you've finished exploring.
- **Networking is where people get stuck, and real clouds give the least feedback there.** A missing route, a wrong security group rule, or a NAT gateway in the wrong subnet doesn't produce an error. The connection just times out.
- **Local emulators are built for a different job.** LocalStack, Floci, MiniStack, fakecloud, and Moto exist so application code can be tested against AWS APIs. They return plausible API responses. Instances are often not machines you can use, and network rules are usually stored without being enforced, or enforced partially and only when you opt in.

## 2. What Nephos is

Nephos is a local, open source cloud simulator for **learning infrastructure**. It runs on your machine as one appliance container and gives you:

- **Instances that are real Linux systems.** systemd, SSH, a package manager, user data. You can SSH in, install nginx, and curl it from another subnet.
- **VPC networking that behaves like the real thing.** Subnets, route tables, internet gateways, NAT gateways, security groups, and network ACLs are enforced on real packets. Break a route and connectivity breaks, the same way it would in AWS.
- **A console with a live topology map** of VPCs, subnets, instances, and gateways.
- **Guided labs with automatic checks.** For example: "Your private web server can't reach the internet. Fix it." Nephos verifies the fix by sending real traffic.
- **Explanations.** `nephos explain` names the exact security group rule, NACL entry, or missing route that blocks a flow.

Nephos uses AWS concepts and names (VPC, subnet, security group, NAT gateway, `t3.micro`, user data) so skills transfer. It has its **own** API, CLI, and, later, Terraform provider. It is deliberately **not** AWS wire compatible.

## 3. Who it's for

**Primary (MVP):**

| Persona | Situation | What they need from Nephos |
|---|---|---|
| **Self-learner** | Student, career switcher, or cert candidate (e.g. AWS Solutions Architect Associate networking topics). May not have or want a cloud account. | No card, no fear of bills, a guided path, immediate feedback on mistakes. |
| **Developer moving into DevOps** | Knows Docker and Linux; wants real intuition for VPC design and debugging; lives in the terminal and IaC. | A fast CLI, realistic failure modes, inspectable internals, and Terraform later. |

**Secondary (after the MVP):**

- **Instructors and bootcamps:** reproducible labs without giving every student a cloud account (see [ROADMAP](ROADMAP.md) M15).
- **Teams and interviewers:** troubleshooting exercises ("prod can't reach the database, find out why").

**Not for:** testing application code against AWS APIs (use fakecloud, Floci, MiniStack, or Moto), or running a production private cloud (use OpenStack or Spinifex).

## 4. Principles

1. **Real behavior over API fidelity.** If it would break in AWS, it breaks in Nephos, in the same way: dropped packets time out, closed ports refuse.
2. **Never fake silently.** If the host can't enforce network rules, Nephos refuses to start and explains why. It never falls back to "stored but not enforced".
3. **Explain, don't just emulate.** Every blocked flow can be traced to the rule, route, or missing gateway that blocked it.
4. **Safe to break.** Everything lives in one container and one volume. One command resets it. Nothing touches the host's network configuration.
5. **Skills transfer.** AWS vocabulary, ID formats, default behaviors, and field names aligned with the Terraform AWS provider. Every deviation from AWS behavior is documented.
6. **Learning drives scope.** A feature ships when a lab or learning scenario needs it, together with tests that pin down its behavior. Nephos does not chase AWS's API surface.
7. **Open by default.** Apache-2.0. No tiers, no telemetry, no account.

## 5. Non-goals

- AWS API or SDK compatibility (`aws --endpoint-url …` will not work, by design).
- IAM, managed databases, serverless, and managed Kubernetes.
- Multi-node or multi-host clustering.
- Billing simulation.
- Acting as a security boundary for untrusted, multi-tenant users (the MVP is single-user and local).
- Cloning the AWS console. Nephos uses AWS vocabulary but its own design, with no AWS logos or trade dress.

## 6. What makes Nephos different

| Differentiator | How Nephos delivers it |
|---|---|
| **Correct VPC semantics end to end** | Nephos runs its own routed network plane: a router namespace per VPC, per-subnet route tables, 1:1 NAT for public IPs at the internet gateway, NAT gateways as real NAT hops, and a local route between subnets. Docker networks are never used, because they can't express these semantics. |
| **Enforcement that can't silently degrade** | Security groups (stateful) and NACLs (stateless) are nftables rules applied on every packet. If enforcement isn't possible, `nephos doctor` fails and the appliance does not start. |
| **Explainability** | `nephos explain` walks the AWS evaluation order and names the blocking component. `nephos probe` sends real traffic and reports reachable, refused, or timeout. Tests keep the two in agreement. |
| **Verified guided labs** | Labs describe the starting infrastructure, injected faults, objectives, and hints in YAML. Checks send real traffic, not just config assertions. Every lab ships a reference solution that CI replays. |
| **Beginner-friendly surfaces** | Web console with a live topology map, a browser terminal, and "My IP" security group rules, alongside a scriptable CLI. |
| **Honest learner access** | Your own SSH session enters through a simulated internet, so a missing IGW route or a closed port 22 locks you out exactly as it would in AWS. A serial-console analog gets you back in. |

## 7. Landscape (as of September 2026)

| Tool | Built for | License / cost | Usable instances | Routing, IGW, NAT behavior | SG / NACL enforcement | Learning features |
|---|---|---|---|---|---|---|
| **Nephos** (planned) | Learning infrastructure | Apache-2.0, free | Yes: system containers with systemd and SSH | Enforced: route tables, IGW 1:1 NAT, NAT gateways, blackhole routes | Always on: SGs stateful, NACLs stateless; refuses to run without enforcement | Console, topology, guided labs with automatic checks, `explain` |
| **LocalStack** | Developing and testing AWS apps | Proprietary; free non-commercial Hobby plan; account and auth token required since 2026-03-23; community repo archived | Docker-backed instances with SSH | Not documented as simulated | SG ingress rules become host port mappings at launch only; later changes don't apply to running instances | Not a focus |
| **Floci** | Dev, test, CI (LocalStack replacement) | MIT, free | Container-backed EC2 workloads | Not claimed ("does not claim VPC routing, NACL, NAT gateway, or peering behavior") | SG enforcement merged 2026-09-15, opt-in, disabled by default; no NACLs | None |
| **MiniStack** | Dev, test (LocalStack replacement) | MIT, free | Metadata by default; opt-in container backing | Metadata only ("packet routing is not simulated") | SG rules stored, never filter traffic | None |
| **fakecloud** | Dev, test, CI (LocalStack replacement, 105 services) | AGPL-3.0, free | Docker/Podman containers | One Docker network per subnet; private subnets are `--internal` networks; subnets "cannot route to each other" | SGs and NACLs rendered to nftables, opt-in, needs CAP_NET_ADMIN; otherwise "tracked-only" | None |
| **Moto** | Unit tests (Python mocks, server mode) | Apache-2.0, free | No (in-memory) | No | No | None |
| **Vyomi** | Multi-cloud "digital twin" for dev and test; also marketed to universities and trainers | BSL 1.1 source-available (converts to Apache-2.0 after 4 years); free individual tier, paid plans from $9/month | Not documented | Not documented | Not documented | Re-implemented AWS/GCP/Azure consoles, in-browser "Nano", reference apps; no guided labs or automatic checks found |

**Adjacent categories:**

- **Hosted lab platforms and sandboxes.** Excellent content, but they need subscriptions and a connection, are time-limited, and often run on real cloud accounts behind the scenes. Nephos is free, local, unlimited, and inspectable. What it can't teach is the real AWS console UI.
- **Network emulators** (containerlab, Kathará, GNS3, Mininet). They use the same Linux primitives, but they teach routers, switches, and protocols, not cloud abstractions. With them you would have to build the cloud yourself.
- **Real private clouds** (OpenStack/DevStack, Mulga's Spinifex). Real VMs and networking, but built for production or edge sites: heavy to install, with different abstractions (OpenStack Neutron) or aimed at AWS API compatibility (Spinifex), and no guided learning.

## 8. Nephos vs Vyomi

Vyomi is the closest project with a learning angle, so the differences are worth stating precisely.

1. **Different layer.** Vyomi emulates *data-plane services behind SDKs* (S3, RDS, SQS, KMS, Secrets Manager, EventBridge, IAM) across three clouds. Nephos simulates *infrastructure behavior* (instances and VPC networking) using one cloud's vocabulary.
2. **Different license.** Vyomi's BSL 1.1 forbids modifying its tier-enforcement code and offering it as a hosted service without a commercial agreement. Nephos is Apache-2.0, so universities, bootcamps, and companies can host, fork, and extend it freely.
3. **Different learning model.** Vyomi provides consoles and reference apps. Nephos provides scenario labs with behavioral checks, hints, debriefs, and a test harness that keeps labs from rotting.
4. **Feedback, not just a replica.** `explain`, `probe`, and the topology overlay show *why* traffic fails. A replica that times out teaches as little as a real cloud that times out.
5. **Depth over breadth.** For this audience, AWS-style networking done correctly beats three clouds done shallowly.

**Nephos will not compete on** SDK compatibility, multi-cloud coverage, or number of services.

**Watch:** Vyomi explicitly targets universities and trainers. If it adds labs, Nephos's advantages remain the realism of its network plane, its open license, and a community lab ecosystem.

**Complementary use:** practice SDK code against S3 or SQS in Vyomi; practice designing and debugging networks in Nephos.

## 9. Why not just…

- **…use the AWS free tier?** It requires a card, and the things learners most need to break (NAT gateways, load balancers, public IPv4 addresses) are billed. Tearing down and rebuilding VPCs dozens of times is slow and nerve-wracking. Learn the concepts in Nephos, then use AWS for the real console and managed services.
- **…use LocalStack, Floci, or fakecloud?** They answer "does my code call AWS correctly?" Nephos answers "does my network design actually work, and if not, why?"
- **…use containerlab or Kathará?** You would have to build route tables, security groups, NAT gateways, and the lab system yourself. That is Nephos.
- **…use OpenStack?** It is a production cloud: multiple gigabytes of RAM, a complex install, different abstractions, and no guided labs.

## 10. Success measures

- Time from install to first SSH session into an instance: **under 10 minutes**, excluding downloads.
- **100% agreement** between `nephos explain` and live probes on the connectivity test matrix.
- Every MVP network feature is covered by at least one lab and one end-to-end test.
- `nephos reset` leaves **zero leaked resources** after 100 randomized create/delete cycles.
- External contributors ship labs. Lab authoring is the main contribution path.
- Someone who completes the five MVP labs can design and debug a two-tier VPC on real AWS.

## 11. Naming note

"Nephos" is also used by the **NeoNephos Foundation** (Linux Foundation Europe, cloud-to-edge) and by an archived Hyperledger Labs project. Since Nephos is a freely downloadable open source learning tool that people run on their own machines, rather than a commercial offering, the project keeps the name and accepts the overlap ([RISKS](RISKS.md) P3). It is unaffiliated with either project.

## Sources

Accessed 2026-09-17.

- fakecloud EC2 documentation: <https://fakecloud.dev/docs/services/ec2/>
- fakecloud PR #2525, security group enforcement: <https://github.com/faiscadev/fakecloud/pull/2525>
- Floci PR #3681, opt-in security group enforcement: <https://github.com/floci-io/floci/pull/3681>
- Floci: <https://github.com/floci-io/floci>
- MiniStack known limitations: <https://ministack.org/docs/limitations>
- LocalStack EC2 documentation: <https://docs.localstack.cloud/aws/services/ec2/>
- LocalStack single image and auth token: <https://blog.localstack.cloud/localstack-single-image-next-steps/>
- LocalStack archived repository: <https://github.com/localstack/localstack>
- Moto: <https://github.com/getmoto/moto>
- Vyomi: <https://vyomi.cloud/> and <https://github.com/vyomi-cloud/appliance>
- Mulga Spinifex: <https://github.com/mulgadc/spinifex>
- NeoNephos Foundation: <https://github.com/neonephos>
