<!-- Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License. -->

# The Fleet Autoscaler

CCO can keep spare host VMs running so that a device create request finds a host
already available instead of waiting several minutes for one to be provisioned.
Those spare hosts are called standing capacity, and the background job that
manages them is the fleet autoscaler.

The autoscaler does not reserve hosts for particular requests. Placement already
prefers any existing host with room, so a spare host is used by the next request
that fits on it. The autoscaler only keeps the number of spare hosts close to a
target that it recalculates on every cycle.

It runs every 30 seconds on whichever instance holds the leader lease for
background jobs. The job is always present in the deployment. The mode setting
described below controls what it is allowed to do, and changing that setting
takes effect without a redeploy.

## Modes

The mode setting controls how much of the autoscaler is active. Setting it to
`off` stops all autoscaler activity.

| Mode | Renew a lease | Buy a host | Shorten a lease |
|---|---|---|---|
| `off` (default) | no | no | no |
| `provision_only` | yes | yes | no |
| `on` | yes | yes | yes |

In `off` mode the autoscaler still calculates and publishes the target, which
lets you measure demand before enabling any buffering. Only `on` mode may
shorten a host's lease, which is the only difference between `on` and
`provision_only`.

## How one cycle works

```
  every 30 seconds, on the instance leading the background jobs

  ┌ the settings ───┐  ┌ the fleet ──────┐  ┌ the demand ─────┐
  │ mode            │  │ standing        │  │ requests that   │
  │ bounds          │  │ draining        │  │ found no host,  │
  │ windows, shapes │  │ total, per pool │  │ inside the      │
  │                 │  │                 │  │ horizon         │
  └────────┬────────┘  └────────┬────────┘  └────────┬────────┘
           └────────────────────┼────────────────────┘
                    read once, at one moment
                                ▼
       ┌─────────────────────────────────────────────────┐
       │  for each pool, using only that pool's hosts    │
       │                                                 │
       │    target    the largest of three inputs        │
       │    shortage  how far below either bound it is   │
       │    surplus   what neither bound requires        │
       └────────────────────────┬────────────────────────┘
                ┌───────────────┴───────────────┐
                ▼ short                         ▼ spare
                renew a host whose lease        shorten the leases with
                is running out, then buy        the fewest cached builds
                needs provision_only or on      needs on
                │                               │
                ▼                               ▼
                the pool grows, within          the reaper deletes each
                this cycle's budget             VM as its lease ends
```

Both directions can happen in the same cycle. With more than one pool it is
normal for one pool to be short while another has spare capacity.

The stages run in this order:

| | Stage | Runs when |
|---|---|---|
| 1 | prefetch popular builds onto spare hosts | the target is above zero |
| 2 | reclaim dedicated hosts whose group has ended | mode `on` |
| 3 | close each pool's shortage: renew, then buy | mode `provision_only` or `on` |
| 4 | return each pool's surplus | mode `on` |

If any of the three reads fails, the cycle buys nothing and logs the failure.
The autoscaler never acts on incomplete information. If an action fails, the
rest of the cycle continues, because the reads that produced the other pools'
numbers succeeded. All errors are reported together at the end.

## Pools

The fleet is divided into pools, each holding a different kind of machine. Every
bound is evaluated against that pool's own hosts, so a GPU pool below its floor
buys a GPU machine while the default pool sits at its target. One pool's
machines never satisfy another pool's bounds.

| Pool | Holds | A create request is routed here when |
|---|---|---|
| `default` | the x86 CPU path | it says nothing about capacity |
| `gpu` | machines with an accelerator | it requires a GPU. A GPU preference is served by the default pool, since it can fall back to a CPU host |
| `arm` | arm64 bare metal | it requires the arm architecture |

The list of pools is fixed. A pool exists because the platform can route
requests to it and knows which machine type to buy for it, and neither of those
is something you can define per deployment.

A pool is enabled by configuring it. Give it bounds and requests start being
routed to it. Remove the bounds and it is disabled again, and its remaining
machines are cleaned up by normal reclamation. The default pool is always
present, because it is where every host without an explicit pool belongs, so it
cannot be disabled.

The arm pool has an additional requirement. The autoscaler will not buy for it
unless the deployment can build an arm64 host, which needs an arm machine family
with an enabled host image. Without that, the autoscaler reports the pool as
short, logs that no machine can be built for it, and buys nothing. Buying anyway
would create the machine in a different pool. The shortage would still be there
on the next cycle, the new machine would count as surplus in whatever pool it
joined, and the fleet would create and delete real VMs indefinitely without
reporting an error.

## The two bounds

Each pool has two independent lower bounds. They constrain different properties
of the same set of hosts, so they are never added together. Two free hosts
satisfy a floor of 2 and a standby of 2 at the same time.

- **standby** keeps N hosts **free**. This limits latency. A request that takes
  one of them triggers a replacement, at the cost of a pool that is always N
  hosts larger than the work running on it.
- **floor** keeps N hosts **running**, whether or not devices are on them. This
  limits cost. A request landing on one triggers nothing, and scale-in stops at
  this number.

The standby bound is a target calculated from three inputs, and the largest of
the three wins.

```
                       0   1   2   3   4   5   6  hosts
                       ├───┼───┼───┼───┼───┼───┤
   what you asked  2   ████████
   an open window  5   ████████████████████          08:00-18:00, Mon-Fri
   observed demand 3   ████████████
                       ─────────────────────────
   the target      5   ████████████████████          the window is spending
```

The three inputs are alternative answers to the same question, which is how many
idle hosts should be available, so the autoscaler takes the largest instead of
the sum. Adding them would count a scheduled window twice, because a window is
usually configured in anticipation of demand that the demand signal also
measures.

The result is capped at 16 hosts, or one eighth of the hosts already standing in
that pool, whichever is larger. Only the demand input can reach this cap, since
the other two are operator-entered values that are already limited to 16 when
stored. The autoscaler page reports when the cap is applied, so a pool held at
its cap while demand asks for more is visible.

### A worked example

```
   default pool                        gpu pool
   ┌───┬───┬───┬───┬───┬───┐           ┌───┬───┐
   │ # │ # │ # │ . │ . │ . │           │ # │ . │      # occupied
   └───┴───┴───┴───┴───┴───┘           └───┴───┘      . free

   floor 3  wants 3 running, has 6     floor 1  wants 1 running, has 2
   free  2  wants 2 free,    has 3     free  2  wants 2 free,    has 1
   ───────────────────────────────     ───────────────────────────────
   slack of 3 and of 1                 slack of 1, but short by 1
   SURPLUS 1: shorten a lease          DEFICIT 1: renew or buy

   both happen in the same cycle; one pool's hosts never count for another
```

The shortage is the larger of the two individual shortfalls, not their sum.
Both bounds describe the same hosts, so adding them would buy twice the
capacity needed. The surplus is the smaller of the two amounts of slack, and
only counts when both are positive. A host can be released only if the fleet
would still meet its floor without it and the standby target would still be
met. This is what it means for scale-in to stop at the floor.

The page also reports which input produced the current target: the configured
standby value, an open window, measured demand, or the floor. The floor is not
one of the target's inputs. When the page names the floor, the standby target
was already satisfied and a host is being bought because the floor requires more
running hosts than the pool has. When two inputs produce the same number, the
page credits the simpler one, so a target that happens to equal the configured
standby value is reported as the configured value.

## Adding capacity

The autoscaler has two ways to close a shortage and prefers the one that costs
nothing.

```
   a pool is short
        │
        ├── 1 ── RENEW ─── a host in this pool whose lease is running out,
        │                  most cached builds first. It is already running,
        │                  so keeping it costs nothing extra
        │                  · a fresh full term of that host's own lease
        │                  · never past 24 hours from when it was created
        │                  · no separate limit; the shortage bounds it
        │
        └── 2 ── BUY ───── whatever renewal did not cover
                           · nothing for a pool whose machines this
                             deployment cannot build
                           · a few per cycle, counted across the whole fleet
                           · never the host quota's last slot
                           · the largest shortage is served first
```

Renewal returns usable capacity within one cycle, while buying a host takes
several minutes. The 24-hour limit on renewal exists because renewal prefers the
host with the most cached build artifacts, and hosts only accumulate cache over
time. Without the limit the fleet would keep renewing its oldest hosts and never
replace them with hosts running newer host images. Lease expiry is the only
mechanism that rolls new images into the fleet.

Both purchase limits apply to the whole fleet, not to each pool, because they
ration the host quota, which is a single number for the deployment. What the
pools affect is the allocation. The pool with the largest shortage is served
first, so a pool short by one host cannot consume a cycle that a pool short by
ten needs. Pools with equal shortages take turns, and when there is nothing left
to choose between them the autoscaler buys in pool name order, so the same
situation always produces the same result.

If a purchase for one pool fails, that pool is skipped for the rest of the
cycle, since an immediate retry would fail the same way. Other pools are
unaffected.

### The quota reserve

The autoscaler never takes the last slot of the host quota. The quota is a hard
limit, and a create request that exceeds it is rejected with an error the caller
sees, so using the final slot for a spare host would make a user's next request
fail. A spare host costs a few minutes of drain time, while a rejected request
cannot be undone. Near the quota limit the autoscaler stops buying, and both the
page and the metrics report why. See [Quotas](quotas.md).

## Removing capacity

Two separate things are reclaimed, both only in `on` mode. Neither of them
deletes a VM directly. Shortening a lease brings the host forward in the queue
for the reaper, which is the job that deletes expired hosts, so every check the
reaper already performs still applies.

```
   SURPLUS               hosts the target no longer needs
     · at most two leases per pool per cycle
     · fewest cached builds first, then nearest expiry. This is the
       reverse of the renewal order, so the host most likely to be
       renewed is the last one to be shortened
     · never a host younger than ten minutes, because an empty new host
       is still filling up, not drained
     · never a host that is part-way through caching a build

   EMPTY DEDICATED       a host reserved for one group, where the group
                         has ended
     · at most two per cycle, largest remaining lease first
     · nothing else reclaims these, because placement can never reuse a
       reserved host, so it would otherwise bill for its whole lease
       while serving nobody
```

This limit applies per pool, while the purchase budget applies to the whole
fleet. The purchase budget rations the shared host quota, whereas this limit
controls how fast each pool gives capacity back, which is a per-pool concern. It
is a fixed two per cycle instead of a proportion of the fleet, because a
proportional limit would remove more capacity the larger the fleet was. A target
that drops from 16 to 0 therefore converges over several cycles instead of
shortening 16 leases at once, so a target set incorrectly and corrected a minute
later costs two hosts.

```
   a fleet host's lease

   created ──────────────────────────────────────────────▶ expiry
           ├─────── counted as STANDBY ───────┼─ 10 min ─┤
                                              └ DRAINING ┘
                                                too little lease left to be
                                                usable

   renewed    ───────────────────────────────────────────────────▶ later
              a fresh full term, never past 24 hours from creation

   shortened  ───────────▶ about 8 minutes from now, then the reaper
                           deletes the VM. The write is re-checked at the
                           last moment and refuses if a device has landed
                           since, if the host is protected, or if the lease
                           is already shorter than that
```

## Which hosts are counted

Both bounds are measured against subsets of the same host list. The page shows,
for each host, the first check it failed.

```
   every host row in the pool
    │
    ├─ not the fleet's ......... created by a user or a batch, or reserved
    │                            for a single group
    ├─ not placeable ........... a status placement will not use, or a host
    │                            still coming up whose provision GCE refused
    │                            for capacity (it clears once the VM exists)
    ├─ degraded ................ excluded by health checks
    ├─ exclusive ............... placement may not add other devices to it
    ▼
   TOTAL ── what the floor is measured against: every host the fleet is
    │       running, occupied or free
    │
    ├─ carrying devices ........ running real work, not spare capacity
    ▼
   IDLE
    │
    ├─ lease under ten minutes . DRAINING: the reaper would delete it before
    ▼                            a replacement could be provisioned
   STANDBY ── what the standby bound is measured against
```

Standby and draining are shown next to each other so that a drop in the buffer
can be attributed to leases expiring rather than to devices arriving. Hosts that
users created by hand are excluded from all of these counts. The autoscaler
neither counts them nor replaces them.

## How many devices a host holds

A target measured in hosts is only meaningful if you know how many devices one
host runs. That number comes from the machine type itself, calculated from its
core count and its memory.

```
   n4-standard-48         48 cores, 192 GB

     by memory    (192 GB − 4 GB kept for the host) ÷ 8 GB per guest  = 23
     by cores     (48 − 2 kept for the host) ÷ 2 per guest            = 23
     the fit      the smaller of the two                              = 23
     admitted     bounded by the slots the host image prepares        = 10
```

The two limits have different failure modes. Too little memory per guest kills
guests, while too little CPU slows all of them down. The autoscaler uses the
smaller of the two, so a machine with plenty of RAM but few cores is limited by
its cores. The per-guest CPU figure is a measured steady-state cost, not the
guest's configured vCPU count, because guest vCPUs are oversubscribed.

Large machines are still limited to ten devices. The host image prepares a fixed
number of network interfaces at boot, and a host is never admitted above that
count. Raising it requires a new host image, not a configuration change.

If the calculated value is wrong for your workload, override it per machine type
on the console's catalog page. The override is used exactly as entered, and
clearing it restores the calculated value.

To convert a target into device capacity, multiply the target by the smaller of
the machine type's fit and the ten slots the image provides. The autoscaler page
reports the fit of the default machine type and names that machine type.

## Machine type fallback

Each pool can have an ordered list of machine types, called its shape ladder.
The autoscaler tries the first entry and moves down the list when Compute Engine
has no capacity for the one above. An empty list means the catalog's default
machine type with normal zone failover.

```
   gpu pool, in the order you configured   zones  out of stock   result
   ┌── g2-standard-8    L4 x1                 3         3        skipped
   │   n1-standard-8    T4 x1                 5         2        BUILD HERE
   └── n1-standard-16   T4 x1                 5         0        not reached
```

An entry is skipped only when every zone it could be built in has a recent
out-of-stock record, since one available zone is enough to be worth trying.
Those records are stored per machine family, so two entries from the same family
share them. Each attempt starts again from the first entry, so a temporary
shortage does not keep the fleet on a fallback machine type after capacity
returns.

Which machine types a list may contain depends on the pool that owns it. Bare
metal is valid only in the arm pool. A machine type with an integrated GPU is
rejected in a CPU pool, because it would attach an accelerator to a host that
does not need one. A machine type without a GPU is rejected in the GPU pool,
because hosts built from it could not serve the requests routed there. The same
list can therefore be valid in one pool and rejected in another, which is why
each pool has its own. If the machine type you want is not in the catalog,
enable its machine family first.

## Scheduled windows

A pool can have up to eight recurring wall-clock windows that raise its target
while they are open. Use them for demand you know is coming, since the demand
signal can only report demand that has already been missed.

Each window has a name, a host count, a start time, a duration, an optional set
of weekdays, and an IANA time zone. The start time is wall clock in that zone
instead of a fixed instant, because the demand it covers is usually human. A CI
shift starts at 08:00 local time whatever the current UTC offset is. The zone
must be named explicitly, because using the server's own time zone would make
the same window open at different times depending on where the process runs.
Daylight-saving transitions are resolved so that a window never opens earlier
than its stated time.

When windows overlap, the largest host count applies. Windows are created on the
console's Settings page. The CLI can clear them, which is the recovery path when
the console is unavailable.

## The demand signal

The autoscaler counts create requests that found no available host and had to
provision one. It reads them over a configurable horizon, one hour by default.
Only requests that a spare host could have served raise the target.

```
   every miss in the horizon
    │
    ├── SERVABLE      a standby host would have taken it
    │                 ──▶ RAISES THE TARGET
    │
    └── UNSERVABLE    no amount of standing capacity would have helped
        │             ──▶ reported, never acted on
        ├── the create required a GPU
        ├── the default machine type could not hold it
        └── the group needed more slots than a spare host has
```

Buying hosts for the unservable requests would not help, so the target ignores
them. They are still reported, because an operator who sees many misses and no
change in the fleet has to be able to tell that case apart from a demand signal
that has stopped working. A request can fail more than one of the three checks
and is counted under each, since each one identifies a separate problem to fix.

The reported figure is a peak concurrent count, not a total. It is how many
misses were outstanding at the same time, measured over a window as long as one
host build takes. Two misses that far apart could have shared a host, because
the first request's host existed by the time the second arrived, and counting
them as concurrent would double the figure for a steady arrival rate. If the
horizon holds more misses than a single read returns, the page reports the
figure as a lower bound.

### The recommended standby value

The recommendation suggests a value for the standby setting. It uses the same
statistic as the target but over seven days instead of the demand horizon, and
it returns the time range that produced the peak, the servable and unservable
counts, and an hourly series.

If the period contains fewer than five servable misses it returns no
recommendation. Below that, a single incident determines the peak, and
recommending capacity for a week on the evidence of one afternoon would be
misleading.

## Build prefetch

While a pool's target is above zero, the autoscaler also runs one prefetch step
per cycle. It creates a temporary device from a frequently used build on each
spare host and deletes it as soon as it boots. The downloaded artifacts stay in
the host's cache, so a later real request for the same build pays only for the
boot, saving roughly 90 seconds.

Prefetching creates no VMs and costs nothing, so the mode setting does not
control it. It runs over whatever spare hosts exist, including a fleet that is
exactly at its target. A target of zero disables it, because it would otherwise
prefetch onto idle hosts that users created.

## Fixed limits

These values are compiled into the deployment and cannot be configured. The
autoscaler page displays them.

| Limit | Value | Purpose |
|---|---|---|
| Cycle | 30 seconds | |
| Lease a standby host must hold | 10 minutes | below it the reaper deletes the host before a replacement is ready |
| Time a host build takes | about 2½ minutes | the width of the demand window |
| Cap on the target | 16, or an eighth of the pool | only demand can reach it |
| Bounds you can configure | 0-16 each | per pool, for both bounds |
| Hosts bought per cycle | 2, or a thirty-second of the fleet | growth is fast without being abrupt |
| Leases shortened per cycle | 2 per pool | capacity is returned gradually |
| Dedicated hosts reclaimed per cycle | 2 | |
| Host quota held in reserve | 1 host | so a purchase cannot make a user's request fail |
| Age before a host may be shortened | 10 minutes | a new empty host is still filling |
| Age at which renewal stops | 24 hours | keeps host images rolling through the fleet |
| Device slots a host image prepares | 10 | raising it needs a new image |

The two proportional limits converge instead of growing without bound. If the
demand signal stays at its maximum, the buffer settles at about one seventh of
the hosts occupied by real work, so the fleet cannot exceed roughly 8/7 of the
size it would have had without a buffer. The purchase budget is tighter: at one
cycle every 30 seconds it grows the fleet by a few percent per cycle, which
reaches thousands of hosts within an hour or two while keeping any single cycle
small.

## Operations

### Where to look

The console's autoscaler page shows the complete state, and its numbers come
from the same calculation the autoscaler itself uses, so the page and the
running job cannot disagree. It shows each pool's two bounds and its shortage or
surplus, every host with the check that excluded it from the counts, each pool's
machine type list evaluated against the same out-of-stock records the autoscaler
uses, the fleet's composition by machine type, and what the autoscaler did over
the last 24 hours. The same data is available from `GET /v2/admin/autoscaler`,
and the seven-day recommendation from
`GET /v2/admin/autoscaler/recommendation`.

### Changing the settings

Fields without a prefix apply to the default pool. Other pools are named with a
prefix.

```bash
cfctl admin config get fleet

cfctl admin config set fleet mode=provision_only standby=2  # buffer, no reclaim
cfctl admin config set fleet mode=on floor=1                # allow scale-in
cfctl admin config set fleet gpu.standby=1 gpu.floor=1      # enable the gpu pool
cfctl admin config set fleet gpu.ladder=g2-standard-8,n1-standard-8
cfctl admin config set fleet demand_horizon_sec=3600 drain_grace_sec=480
cfctl admin config set fleet gpu.enabled=false              # disable the pool
cfctl admin config set fleet mode=off                       # stop everything
```

Each of these values is stored separately and validated as a whole. A stored
value that cannot be parsed, or that is outside its allowed range, is treated as
unset, and none of it is applied. Unset values buy nothing: no bounds means no
machines are requested, and no machine type list means the catalog default.

Disabling a pool removes its bounds, its windows and its machine type list
together. Create requests are no longer routed to it, and its existing machines
are cleaned up by normal reclamation. The default pool cannot be disabled.

### Diagnosing an idle autoscaler

The page reports one reason at a time, chosen in the order below. An autoscaler
that is doing nothing has several possible causes, and listing all of them at
once would not help.

| Reason | Meaning |
|---|---|
| no leader | nothing holds the background job leader lease, so no cycle is running |
| mode off | the mode allows nothing. The target is still published. This is not a fault |
| provision only | there is a surplus and the mode may only add capacity |
| quota reserved | the host quota's last slots are held back for user requests |
| capped | the gap is wider than one cycle may move, so it converges over several |
| acting | nothing is limiting it |
| target met | the fleet holds what the target asks for |

Each pool reports its own reason for its own shortage or surplus. Leader status,
the mode setting and the quota headroom are reported once for the whole
deployment, since there is one of each.

### Metrics and logs

Counters are published for hosts bought, leases renewed, leases shortened,
dedicated hosts reclaimed, cycles blocked by the quota reserve, and failed
purchases.

Each of those actions also writes an audit log entry, and the console's activity
figures are read from the audit log, not from the counters. The counters
are held in memory on whichever instance is currently leading, so they say
nothing about activity from before it took over. The audit entries also outlive
the hosts they describe, which matters because those host records are already
deleted by the time anyone investigates.

The logs record the pool and the bound responsible for each purchase, and the
count and the shortage for each renewal and reclamation. Three warnings appear
only in the logs: a purchase blocked by the quota
reserve, a pool that is short while no machine can be built for it, and a demand
horizon holding more misses than a single read returns.

### Enabling it for the first time

1. Leave the mode set to `off` for about a week. The target is calculated and
   published throughout, and demand is measured without any buffering, which is
   what the recommendation needs to be meaningful.
2. Read the recommendation. If it returns none, the period did not contain
   enough servable misses, so wait instead of using the number shown.
3. Set the standby value and change the mode to `provision_only`. In this mode
   the autoscaler cannot shorten a lease, so the worst case is hosts running to
   the end of their own leases.
4. Change the mode to `on` once the fleet behaves as you expect. This is the
   only mode that removes capacity, and setting the mode back to `off` stops it.
