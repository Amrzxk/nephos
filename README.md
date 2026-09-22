# Nephos

**A local cloud you can break, fix, and understand.** Nephos (Greek *νέφος*, "cloud") is an open source cloud simulator for **learning** infrastructure: real instances you can SSH into, and VPC networking that fails the way a real cloud fails.

> **Status: early development.** [M0](docs/ROADMAP.md#m0-foundations-and-spikes) is done — the repository scaffold, CI, and four spikes that prove the design works on real kernels ([reports](docs/spikes/), [demo](docs/demos/M0.md)). There is no usable product yet: `nephos` prints its version and nothing else. [M1](docs/ROADMAP.md#m1-thinnest-end-to-end-slice-two-instances-ping) is the first milestone that does something.

## What it will do

- **Instances that are real machines.** systemd, SSH, `apt`, user data. Install nginx, then curl it from another subnet.
- **Networking that actually enforces.** Subnets, route tables, internet gateways, NAT gateways, security groups, and network ACLs are applied to real packets. Delete a route and connectivity breaks.
- **Guided labs with automatic checks.** "Your private server can't reach the internet, fix it." Nephos verifies the fix by sending real traffic.
- **Answers, not just failures.** `nephos explain` names the exact security group rule, NACL entry, or missing route that blocked a connection, and suggests the fix.
- **A console with a live topology map**, a browser terminal, and a lab panel.
- **One command to install, one to wipe.** Everything lives in a single container and volume. Nothing touches your host's network configuration.

It uses AWS concepts and names so the skills transfer, and it runs for free on your laptop with no cloud account.

## What it is not

Nephos is **not** an AWS API emulator: `aws --endpoint-url` will never work against it, by design. If you need to test application code against AWS APIs, use [fakecloud](https://github.com/faiscadev/fakecloud), [Floci](https://github.com/floci-io/floci), [MiniStack](https://github.com/ministackorg/ministack), or [Moto](https://github.com/getmoto/moto). Nephos answers a different question: *does my network design work, and if not, why?*

Also out of scope: IAM, managed databases, multi-host clustering, and billing simulation. See [the vision](docs/VISION.md) for the full positioning.

## How it will feel

```bash
nephos up
nephos vpc create main --cidr-block 10.0.0.0/16
nephos subnet create public-a --vpc main --cidr-block 10.0.1.0/24 \
    --availability-zone local-1a --map-public-ip-on-launch
nephos instance run web-1 --ami ubuntu-24.04 --instance-type t3.micro \
    --subnet public-a --key-pair me --user-data file://install-nginx.sh --wait
nephos ssh web-1
nephos explain --from internet --to web-1 --port tcp/80
nephos lab start nat-troubleshooting
```

## The design

| Document | What's in it |
|---|---|
| [docs/VISION.md](docs/VISION.md) | The problem, who it's for, and how Nephos compares with LocalStack, Floci, MiniStack, fakecloud, Moto, and Vyomi |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Components, diagrams, how every cloud concept maps onto Linux, AWS fidelity and deviations, repository layout |
| [docs/ROADMAP.md](docs/ROADMAP.md) | Milestones from zero to the MVP and beyond, with acceptance criteria |
| [docs/LABS.md](docs/LABS.md) | The lab format, check types, and the first five labs |
| [docs/RISKS.md](docs/RISKS.md) | Technical, security, and project risks with mitigations |
| [docs/adr/](docs/adr/README.md) | Architecture Decision Records: language, packaging, networking, instances, state, API, console, labs, license |
| [AGENTS.md](AGENTS.md) | Repository guidance for Codex and contributors |

## License

[Apache-2.0](LICENSE). Contributions require a [DCO](https://developercertificate.org/) sign-off, and there is no CLA. See [ADR-0011](docs/adr/0011-apache-2-license-with-dco.md).

Nephos is not affiliated with, endorsed by, or sponsored by Amazon Web Services. AWS service names are used only to describe the concepts Nephos simulates.
