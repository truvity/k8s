# Safety

What is refused, and what must never be done to a cluster's resources. Each
item exists because the failure it prevents is easy and expensive.

## The contract validator refuses a misleading cluster

**Refuses:** an `Outputs` with a name that is not a DNS-1123 label, a
non-https endpoint, a CA that holds no certificate, an unknown capability,
an issuer with no `workload-identity` or `workload-identity` with no issuer.

**Why.** A consumer that trusts a plain-http endpoint sends credentials in
the clear; one that is told workload identity exists and finds no issuer
fails at the first pod, far from the cause. The check is at the boundary so
every provider is held to it once.

## Never apply an adoption that previews a change

**Rule.** When a component is adopted over existing infrastructure, the
preview must show nothing. A create next to a delete is a replacement of a
cluster's network or control plane that was spelled as a rename.

**Why.** Replacing a VPC, a cluster or a bucket takes the workloads, the
addresses and sometimes the data with it. An alias that is one character
wrong looks exactly like that, and the preview is the only place it shows.

## Never destroy a cluster from a stale checkout

**Rule.** A stack that provisions clusters is applied from an up-to-date,
clean checkout. `pulumi-pipeline` refuses otherwise; a hand-run `pulumi up`
has no such guard.

**Why.** An old program converges the world on an old shape.

## Inputs left empty open nothing

**Rule.** A component's access inputs (endpoint exposure, peering routes,
policies) default to closed. Opening is always an explicit input.
