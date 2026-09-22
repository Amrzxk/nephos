# SP2: ENI plumbing through an OCI hook

- **Status:** **PASS** (18 of 18 assertions on WSL2 and native Ubuntu)
- **Date:** 2026-09-18; native Ubuntu validation 2026-09-22
- **Validates:** [ADR-0004](../adr/0004-instances-as-system-containers.md) R3 (a network namespace wired by Nephos before PID 1, owned by the instance's user namespace)
- **Mitigates:** [RISKS](../RISKS.md) T1
- **Reproduce:** `./spikes/run.sh sp2`

## Question

M1's instance launch depends on one thing working: Podman must let Nephos wire
an instance's interface **after** its namespaces exist but **before** its first
process runs, and the resulting network namespace must belong to the instance's
user namespace so the learner can run `ufw` inside it.

If the hook cannot do that, instances race their own network at boot, cloud-init
fetches metadata before there is a route, and the lockout lessons in
[LABS](../LABS.md) cannot be built at all.

## Shape

- `plumbd` — the stand-in for `nephosd`'s hook endpoint. Listens on
  `/run/nephos/hook.sock`, creates the veth pair, puts the router end in the VPC
  namespace with proxy ARP and a `/32` route, and moves the instance end into
  the container's namespace by PID.
- `nephos-hook` — registered in Podman's `hooks.d` at the `createRuntime` stage,
  filtered on the `io.nephos.instance-id` annotation. Reads the OCI state from
  stdin, calls `plumbd`, and **exits non-zero if that fails**.

The hook contains no logic of its own, by design: every decision belongs to the
network engine, which is the only component that can be held to ADR-0005's
honesty rule.

## Results

| Criterion | Verdict | Evidence |
|---|---|---|
| The hook is registered and filtered by annotation | PASS | `hooks.d/nephos.json`, `when.annotations` |
| **The first process already sees `eth0`** | **PASS** | a container whose only command is `ip -o addr show eth0` printed `10.50.1.4/24` |
| `eth0` carries its private address | PASS | `10.50.1.4/24` |
| The default route via the subnet gateway is installed | PASS | `default via 10.50.1.1` |
| The router end is in the VPC namespace with a `/32` route | PASS | `10.50.1.5 dev vesp2b scope link` |
| Proxy ARP is on at the router end | PASS | `proxy_arp=1` |
| ICMP redirects are off at the router end | PASS | `send_redirects=0` |
| The instance has its own user namespace | PASS | instance root maps to appliance uid 201024 |
| Instance root can add an nftables rule in its own netns | PASS | `nft add table inet sp2test` |
| Instance root can bring `eth0` down and up | PASS | real `CAP_NET_ADMIN` in its own namespace |
| None of that reaches the appliance namespace | PASS | appliance ruleset unaffected |
| Stop/start re-plumbs with the same address | PASS | `10.50.1.5` again after restart |
| The router end is re-created on start | PASS | host route restored |
| **An unplumbable instance does not start** | **PASS** | `podman run` exited 126, state `created` |

The native Ubuntu 24.04 rerun passed the same 18 assertions in
[`Spikes` run 35698867324](https://github.com/Amrzxk/nephos/actions/runs/35698867324),
including `eth0` before PID 1, stop/start re-plumbing, `CAP_NET_ADMIN` confined
to the instance namespace, and fail-closed handling of an unplumbable instance.

### The two that matter most

**No boot race.** The check is deliberately blunt: a container whose *only*
command is `ip -o addr show eth0`. It printed the address, so the hook had
already completed before PID 1 existed. That is what lets sshd and cloud-init
assume a working `eth0` rather than polling for one.

**It fails closed.** An instance whose ENI cannot be plumbed — here, one whose
ID has no registered ENI — does not start. `podman run` exits 126 and the
container stays in `created`. This is the honesty rule in executable form: an
instance that booted without its network would look healthy and be unreachable,
and Nephos would be lying about what it had configured.

### Instance root really does own its namespace

Instance root added an nftables table and took `eth0` down and back up. That is
genuine `CAP_NET_ADMIN`, scoped to the instance's own network namespace, and it
is what makes "you locked yourself out, now recover through the serial console"
a real lesson rather than a simulated one.

The appliance's own ruleset was untouched throughout, which is the boundary
[RISKS S3](../RISKS.md#s3-kernel-attack-surface-through-instance-owned-network-namespaces)
depends on.

### Teardown is automatic, and that has a consequence

When a container exits, its network namespace goes with it and **both** ends of
the veth pair disappear — the router end included. After stopping the instance,
zero `vesp2b` interfaces remained in the VPC namespace.

That is correct, and it is also why M1's reconciler cannot assume the router end
still exists. Re-plumbing on start is not an optimisation; it is the only
correct behaviour.

## Findings to carry into M1

1. **The hook must be filtered by annotation, not by directory.** Podman applies
   every hook in `hooks.d` to every container it runs. `when.annotations` on
   `io.nephos.instance-id` is what keeps the hook off anything that is not a
   Nephos instance.

2. **`createRuntime` is the right stage.** It is the only one where the
   container's namespaces exist (so there is a PID to target) and PID 1 has not
   yet started. `prestart` is deprecated; `createContainer` runs inside the
   container's namespaces and cannot move interfaces into them.

3. **The instance end must be renamed after the move.** A veth peer cannot be
   called `eth0` while it is still in the appliance namespace, where that name
   is taken. Create it as `<router-name>p`, move it, then rename.

4. **The gateway needs an explicit on-link route first.** The subnet gateway
   lives on the router's dummy interface, outside the instance's own prefix, so
   `ip route add default via <gw>` fails until a `/32` on-link route to the
   gateway exists. This will bite anyone reimplementing it from the ADR alone.

5. **`send_redirects=0` is load-bearing.** Without it the router can tell an
   instance to bypass it for same-subnet traffic, which would bypass every
   security group. It is not a tuning knob.

## Verdict

**ADR-0004 R3 is validated on WSL2 and native Ubuntu.** An OCI `createRuntime`
hook wires the ENI before PID 1, the namespace is owned by the instance's user
namespace, stop/start re-plumbs, and an instance that cannot be plumbed refuses
to boot.

`internal/compute` and `cmd/nephos-hook` can be written against this shape in M1.

## Outstanding

- [ ] The hook talks to `plumbd` over HTTP on a unix socket. M1 should decide
      whether `nephosd` keeps that shape or uses a smaller framing; the hook's
      contract (state on stdin, non-zero exit on failure) is what matters.
- [ ] Nothing here exercises **concurrent** launches. M1 should confirm that
      simultaneous plumb requests do not collide over interface-name allocation.
